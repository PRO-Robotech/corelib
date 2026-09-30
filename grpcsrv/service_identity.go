// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package grpcsrv — service_identity.go: звено идентичности служб.
//
// # Что это и чем отличается от круга пересылающих
//
// Круг пересылающих ([TrustedForwarders]) отвечает на вопрос «вправе ли этот пир
// говорить за ДРУГОГО». Звено идентичности отвечает на другой вопрос: «кем
// является САМ пир, когда за другого он не говорит». Ответ даётся только на
// закрытом перечне методов и только по точной строке таблицы `{SAN → имя
// службы}`; вне перечня и вне таблицы пир остаётся безымянным, как и до звена.
//
// Имя кладётся во ВТОРОЙ носитель — под собственным ключом контекста, а не в
// носитель принципала операций. Засев первого носителя сделал бы службу
// владельцем операций и дал бы ей системные полномочия второго слоя
// ([operations.CheckRecordedOwnership]); второй носитель этого не делает по
// построению: [operations.PrincipalFromContext] его не читает.
//
// Читатель второго носителя в фундаменте один — [ServiceNameFromContext], и
// зовёт его одна функция субъекта (`authz.CallerSubject`). Строку
// `service:<имя>` производит тоже одна функция (`authz.ServiceSubject`).
//
// # Сравнение
//
// Ключ таблицы и SAN сертификата приводятся ОДНОЙ функцией — [CanonicalSAN], — и
// сравниваются строки после приведения. Разбора SAN на сегменты при сравнении
// нет: домен доверия, пространство и учётка входят в ключ целиком, и совпадение
// по началу совпадением не является. Ключ, литерал которого отличается от своей
// канонической формы, — отказ старта, а не молчаливое приведение: таблица,
// записанная не в той форме, в которой её сравнивают, — место, где оператор
// видит одно, а звено сравнивает другое.
//
// Сертификат с двумя и более SPIFFE-идентификаторами служебного субъекта не
// даёт: у X.509-SVID идентификатор ровно один, и выбирать «первый подходящий»
// значило бы решать о личности порядком записи в сертификате.
package grpcsrv

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"google.golang.org/grpc"
)

// ServiceName — имя службы в служебном субъекте `service:<имя>`.
//
// Форма — DNS label (RFC 1123): строчные латинские буквы, цифры и дефис, не на
// краях, от 1 до 63 знаков. Разделителей модели прав (`:`, `#`, `@`, пробелов)
// в форме нет by construction — имя не сдвигает границу «тип:идентификатор» и не
// становится ссылкой на набор.
type ServiceName string

// serviceNameMaxLen — предел длины DNS label.
const serviceNameMaxLen = 63

// Valid — имя в форме DNS label.
func (n ServiceName) Valid() bool {
	s := string(n)
	if s == "" || len(s) > serviceNameMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}

// ServiceIdentityNotApplicable — метка самоотчёта у процесса без звена: пустая
// строка неотличима от «самоотчёт не заполнили», поэтому неприменимость
// называется словом.
const ServiceIdentityNotApplicable = "n/a"

// ServiceIdentityRow — строка таблицы звена: точный SAN и имя службы.
type ServiceIdentityRow struct {
	SAN  string
	Name ServiceName
}

// ServiceIdentity — принятое звено: перечень полных имён методов и таблица
// `{канонический SAN → имя службы}`.
//
// Нулевое значение законно и не опознаёт никого: перечень пуст. Получить
// непустое звено можно только через [NewServiceIdentity] — то есть пройдя все
// стражи старта.
type ServiceIdentity struct {
	methods map[string]struct{}
	table   map[string]ServiceName
}

// spiffeSchemeName — схема SPIFFE-идентификатора без разделителя.
const spiffeSchemeName = "spiffe"

// NewServiceIdentity — единственная дверь к непустому звену.
//
// Отказы собираются разом и называют часть звена и значение (NTF1-M09):
//   - перечень непуст ⇔ таблица непуста;
//   - метод перечня не в форме полного имени grpc-go (`/<пакет>.<служба>/<метод>`)
//     либо повторён;
//   - ключ таблицы не SPIFFE-идентификатор либо не в канонической форме;
//   - один SAN записан дважды (две записи, равные после приведения);
//   - одно имя службы у двух SAN;
//   - имя службы не DNS label.
//
// Метод перечня вне каталога прав процесса этой функцией не судится: каталог
// выводится после регистрации служб, и этот отказ исполняет носитель
// (`servicehost`) в шаге отказов старта. Имя ручки, из которой пришло значение,
// называет корень, оборачивая эту ошибку: сама функция своих ручек не знает.
func NewServiceIdentity(methods []string, table map[string]ServiceName) (ServiceIdentity, error) {
	var findings []string
	add := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

	switch {
	case len(methods) > 0 && len(table) == 0:
		add("перечень методов непуст (%s), а таблица SAN → имя службы пуста: методы объявлены "+
			"для служб, которых звено не опознает никогда", strings.Join(methods, ", "))
	case len(methods) == 0 && len(table) > 0:
		add("таблица SAN → имя службы непуста (%s), а перечень методов пуст: службы объявлены, "+
			"а открыть им нечего", strings.Join(sortedKeys(table), ", "))
	}

	out := ServiceIdentity{
		methods: make(map[string]struct{}, len(methods)),
		table:   make(map[string]ServiceName, len(table)),
	}
	for _, m := range methods {
		if !fullMethodForm(m) {
			add("метод перечня %q не в форме полного имени grpc-go «/<пакет>.<служба>/<метод>»", m)
			continue
		}
		if _, dup := out.methods[m]; dup {
			add("метод повторён в перечне: %s", m)
			continue
		}
		out.methods[m] = struct{}{}
	}

	// Обход в порядке ключей: текст отказа не зависит от порядка обхода карты.
	byCanon := make(map[string]string, len(table))
	byName := make(map[ServiceName]string, len(table))
	for _, key := range sortedKeys(table) {
		name := table[key]
		parsed, err := url.Parse(key)
		canon := ""
		if err == nil {
			canon = CanonicalSAN(parsed)
		}
		switch {
		case canon == "":
			add("ключ таблицы %q не является SPIFFE-идентификатором (%s)", key, SANShape)
		case canon != key:
			add("ключ таблицы %q не в канонической форме — сравнение идёт с %q; запишите "+
				"ключ в канонической форме", key, canon)
		}
		if canon != "" {
			if prev, dup := byCanon[canon]; dup {
				add("SAN повторён: %q и %q — одна и та же личность %s", prev, key, canon)
			} else {
				byCanon[canon] = key
			}
		}
		if !name.Valid() {
			add("имя службы %q у ключа %q не DNS label (строчные латинские буквы, цифры и дефис "+
				"не на краях, до %d знаков)", string(name), key, serviceNameMaxLen)
		} else if prev, dup := byName[name]; dup {
			add("имя службы повторено: %q у %q и у %q — две личности стали бы одним субъектом",
				string(name), prev, key)
		} else {
			byName[name] = key
		}
		if canon != "" && canon == key {
			out.table[canon] = name
		}
	}

	if len(findings) > 0 {
		return ServiceIdentity{}, errors.New("grpcsrv: звено идентичности служб не собирается (" +
			fmt.Sprint(len(findings)) + " находок):\n  · " + strings.Join(findings, "\n  · "))
	}
	return out, nil
}

// fullMethodForm — `/<пакет>.<служба>/<метод>` без пробелов и лишних слэшей.
func fullMethodForm(m string) bool {
	if !strings.HasPrefix(m, "/") || strings.ContainsAny(m, " \t\n") {
		return false
	}
	svc, method, ok := strings.Cut(m[1:], "/")
	if !ok || method == "" || strings.Contains(method, "/") {
		return false
	}
	dot := strings.LastIndex(svc, ".")
	return dot > 0 && dot < len(svc)-1
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CanonicalSAN — ОДНА функция приведения SPIFFE-идентификатора: разбор и
// пересборка. Ею приводятся и ключ таблицы при старте, и SAN сертификата при
// сравнении.
//
// Приводятся регистр схемы и узла (они регистронезависимы у URI). Путь
// регистрозависим и не трогается. Всё, что делает идентификатор неоднозначным
// либо не SPIFFE-формой, даёт пустую строку, а не «похожее»: порт, учётные данные,
// запрос, фрагмент, пустой, точечный или закодированный сегмент пути,
// завершающий слэш, пустой узел или путь, символ вне словаря SPIFFE.
func CanonicalSAN(u *url.URL) string {
	if u == nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawFragment != "" {
		return ""
	}
	if !strings.EqualFold(u.Scheme, spiffeSchemeName) {
		return ""
	}
	host := strings.ToLower(u.Host)
	if host == "" || !spiffeChars(host, false) {
		return ""
	}
	path := u.EscapedPath()
	if !strings.HasPrefix(path, "/") || len(path) == 1 {
		return ""
	}
	for _, seg := range strings.Split(path[1:], "/") {
		if seg == "" || seg == "." || seg == ".." || !spiffeChars(seg, true) {
			return ""
		}
	}
	return spiffeSchemeName + "://" + host + path
}

// spiffeChars — словарь SPIFFE: латинские буквы, цифры, `.`, `-`, `_`. Узел
// после приведения — только строчные.
func spiffeChars(s string, upperAllowed bool) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		case upperAllowed && c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}

// Methods — перечень методов звена, по возрастанию.
func (s ServiceIdentity) Methods() []string {
	out := make([]string, 0, len(s.methods))
	for m := range s.methods {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Rows — строки таблицы звена в порядке SAN.
func (s ServiceIdentity) Rows() []ServiceIdentityRow {
	out := make([]ServiceIdentityRow, 0, len(s.table))
	for _, san := range sortedKeys(s.table) {
		out = append(out, ServiceIdentityRow{SAN: san, Name: s.table[san]})
	}
	return out
}

// IsEmpty — звено не опознаёт никого.
func (s ServiceIdentity) IsEmpty() bool { return len(s.methods) == 0 }

// Report — строка самоотчёта посадки: перечень методов и строки таблицы.
// У пустого звена — [ServiceIdentityNotApplicable].
func (s ServiceIdentity) Report() string {
	if s.IsEmpty() {
		return ServiceIdentityNotApplicable
	}
	rows := s.Rows()
	pairs := make([]string, 0, len(rows))
	for _, r := range rows {
		pairs = append(pairs, r.SAN+"="+string(r.Name))
	}
	return "methods=" + strings.Join(s.Methods(), ",") + "; table=" + strings.Join(pairs, ",")
}

// serviceNameCtxKey — ключ второго носителя. Не экспортируется: положить имя
// вправе только звено, иначе «опознано по сертификату» подделывалось бы одной
// строкой кода.
type serviceNameCtxKey struct{}

// ServiceNameFromContext — единственный читатель второго носителя.
func ServiceNameFromContext(ctx context.Context) (ServiceName, bool) {
	if ctx == nil {
		return "", false
	}
	name, ok := ctx.Value(serviceNameCtxKey{}).(ServiceName)
	return name, ok
}

// recognize кладёт имя службы, если метод в перечне, сертификат пира проверен и
// несёт ровно один SPIFFE-идентификатор, равный после приведения ключу таблицы.
// Во всех прочих случаях контекст возвращается нетронутым.
func (s ServiceIdentity) recognize(ctx context.Context, fullMethod string) context.Context {
	if _, listed := s.methods[fullMethod]; !listed {
		return ctx
	}
	_, leaf := peerTLSState(ctx)
	san, single := singleSPIFFEID(leaf)
	if !single {
		return ctx
	}
	name, known := s.table[san]
	if !known {
		return ctx
	}
	return context.WithValue(ctx, serviceNameCtxKey{}, name)
}

// singleSPIFFEID — единственный SPIFFE-идентификатор ПРОВЕРЕННОГО листа, уже
// приведённый. Нет листа, нет идентификатора или их больше одного — «нет».
func singleSPIFFEID(leaf *x509.Certificate) (string, bool) {
	if leaf == nil {
		return "", false
	}
	found := ""
	count := 0
	for _, u := range leaf.URIs {
		if c := CanonicalSAN(u); c != "" {
			found = c
			count++
		}
	}
	return found, count == 1
}

// Unary — звено для unary-вызовов. В носителе стоит сразу за извлечением
// личности и до решения о доступе, одинаково на обоих слушателях.
func (s ServiceIdentity) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(s.recognize(ctx, info.FullMethod), req)
	}
}

// Stream — то же звено для потоковых вызовов.
func (s ServiceIdentity) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		recognized := s.recognize(ctx, info.FullMethod)
		if recognized == ctx {
			return handler(srv, ss)
		}
		return handler(srv, &certIdentityStream{ServerStream: ss, ctx: recognized})
	}
}
