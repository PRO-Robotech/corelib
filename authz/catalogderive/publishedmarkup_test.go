// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// publishedmarkup_test.go — ГЕЙТ: каждый RPC контракта, чьи заглушки публикует
// ЭТОТ модуль, несёт разметку доступа `corelib.authz.v1`.
//
// # Зачем гейт живёт ЗДЕСЬ, а не у края
//
// Строку каталога прав производит плагин края
// (`gateway/cmd/protoc-gen-kacho-permissions`): он читает разметку с
// ДЕСКРИПТОРА, а дескриптор приезжает к нему модулем фундамента. Значит
// разметку снимают в ЭТОМ дереве, а краснеет ТОТ прогон — и не раньше, чем
// потребитель поднимет пин. Между снятием и краснотой умещается целый выпуск.
//
// # Что наблюдалось (2026-09-14, отозванная правка `lane/w7-homes-lib`)
//
// Разметка была снята с двух контрактов фундамента как «словарь уезжает к
// службе доступа». Прогон настоящего плагина по дереву без неё:
//
//	protoc-gen-kacho-permissions: 3 warning(s)
//	corelib.subscription.InternalSubscriptionService/Subscribe: missing required option (corelib.authz.v1.permission)
//	kacho.cloud.operation.OperationService/Cancel: missing required option (corelib.authz.v1.permission)
//	kacho.cloud.operation.OperationService/Get: missing required option (corelib.authz.v1.permission)
//
// Пустая строка НЕ равна `<exempt>`: `CatalogEntry.IsExempt()` сравнивает
// именно с литералом, поэтому метод уходит в полосу отношения с пустыми
// отношением и областью — то есть в отказ. Наблюдаемое следствие для
// арендатора: опрос исхода асинхронных мутаций отказывает у ВСЕХ доменов.
//
// В самом фундаменте это НЕ краснело ничем: `MustDerive` зовут его потребители,
// а не он сам, и отсутствие разметки читается `Annotations.Exempt()` как
// «проверки нет».
//
// # Почему предикат «несёт непустой permission», а не «несёт хоть что-нибудь»
//
// Это тот же предикат, которым судит производитель каталога в строгом режиме
// (`KACHO_PERMISSIONS_STRICT=1` → код 1). Второго правила здесь не заводится:
// разошлись бы молча.
//
// # ВТОРОЙ ДОМ КОНТРАКТА — назван числом, предикатом и событием истечения
//
// Контракты этого дома лежат ОДНОВРЕМЕННО в дереве платформы (а три из них — и
// в дереве службы доступа) как ВХОД их генераторов: заглушек из них не
// порождает ни одна, публикует только этот модуль. Запрет ядра #20 такое
// состояние называет копией, и совпадение содержимого его не смягчает.
//
// ПЕРЕПИСЬ 2026-09-14 (единица счёта — отслеживаемый `.proto`, адресуемый ОДНИМ
// путём регистрации; вендорный `google/**` в счёт не входит — его файлы
// принадлежат googleapis и заглушки берутся из чужих модулей):
//
//	своих путей более чем в одном дереве  8
//	пар (путь × пара деревьев)           14
//	из путей разошлось содержимым         1
//
// ПРЕДИКАТ (повторяется за минуту; `LC_ALL=C` обязателен — иначе `comm` врёт;
// существование пути спрашивается `git cat-file -e`, а НЕ `git show`: последний
// на отсутствующем пути отдаёт пустой вывод, и отпечаток пустоты читается как
// отпечаток файла — этот промах здесь уже случился и дал 121 путь вместо 8,
// объявив дерево службы доступа держателем контрактов vpc):
//
//	for t in "<платформа>:origin/release/kc-w5" "<служба>:origin/release/iam-w6"; do
//	  git -C <дерево> ls-tree -r <ревизия> --name-only proto | grep '\.proto$' |
//	    sed 's|^proto/||' | grep -v '^google/' | sort
//	done
//	# и то же по этому дереву: find proto -name '*.proto' | sed 's|^proto/||' | grep -v '^google/' | sort
//	# пересечения — comm -12; расхождение — sha256 каждого держателя
//
// ЕДИНСТВЕННОЕ РАСХОЖДЕНИЕ — `corelib/api/v1/operation.proto`, и оно
// ПРОЗАИЧЕСКОЕ И НЕУСТРАНИМОЕ, пока домов два: абзац того файла говорит о СВОЁМ
// доме («заглушки здесь порождаются» против «здесь не порождаются»), а
// утверждение о доме в двух домах истинно по-разному. Это не небрежность — это
// свойство пары, и оно названо в самом файле.
//
// СОБЫТИЕ ИСТЕЧЕНИЯ — МАШИННОЕ И УЖЕ ОБЪЯВЛЕНО В ДЕРЕВЕ ПЛАТФОРМЫ, дата ни при
// чём: объявление корня `corelib` приезжающим модулем
// (`KACHO_PROTO_ROOT_MODULES` в её `gateway/scripts/lib/stage-proto-tree.sh`,
// сегодня там один `kaname`) плюс её же `internal/contractsource`. С этого
// момента платформа собирает вход генераторов ИЗ МОДУЛЯ, а её гейт объявляет
// находкой каждый отслеживаемый файл под своим `proto/corelib/**`. Второй дом
// после этого не «нежелателен», а красный.
//
// ЧЕГО СОБЫТИЕ НЕ ЗАКРЫВАЕТ, и это сказано прямо: механизм переносит КОРЕНЬ
// целиком, а `kacho/cloud/operation/**` (3 пути из 8) лежит под корнем
// ПЛАТФОРМЫ, хотя публикует его этот модуль. Для них истечения сегодня нет
// вовсе — предмет назван остатком в отчёте линии, а не обещанием здесь.
package catalogderive

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	authzv1 "github.com/PRO-Robotech/corelib/api/corelib/authz/v1"
	"github.com/PRO-Robotech/corelib/treecorpus"

	// Слепые импорты — это и есть объявление «какие контракты дома судятся».
	// Перечень не проверяется памятью: контракт, лежащий в `proto/` и сюда не
	// попавший, объявляется находкой (`contractInTreeIsNotJudged`), потому что
	// несудимый контракт молчит ровно так же, как исправный.
	_ "github.com/PRO-Robotech/corelib/api/corelib/api/v1"
	_ "github.com/PRO-Robotech/corelib/api/corelib/quota/v1"
	_ "github.com/PRO-Robotech/corelib/api/corelib/subscription"
	_ "github.com/PRO-Robotech/corelib/api/kacho/cloud/operation"
)

// protoHome — дом контрактов этого модуля относительно каталога пакета.
const protoHome = "../../proto"

// vendoredInputPrefix — вендорный вход: чужие файлы, чьи заглушки публикуют
// чужие модули. Из счёта дома они исключены by construction.
const vendoredInputPrefix = "google/"

// markupFinding — одна находка с КООРДИНАТОЙ. Симптом без координаты посылает
// читателя искать не там.
type markupFinding struct {
	Contract string
	Method   string
	Why      string
}

func (f markupFinding) String() string {
	if f.Method == "" {
		return fmt.Sprintf("%s: %s", f.Contract, f.Why)
	}
	return fmt.Sprintf("%s: %s: %s", f.Contract, f.Method, f.Why)
}

// markupCensus — ВЕРДИКТ ВМЕСТЕ С ОБЪЁМОМ ОСМОТРЕННОГО. «Ноль находок» обязано
// быть отличимо от «ноль прочитанного», поэтому обе величины возвращаются
// всегда и печатаются в любом исходе.
type markupCensus struct {
	ContractsInTree int
	ContractsJudged int
	Services        int
	Methods         int
	Findings        []markupFinding
}

func (c markupCensus) String() string {
	return fmt.Sprintf("перепись: контрактов в доме %d, из них судится %d, служб %d, методов %d, находок %d",
		c.ContractsInTree, c.ContractsJudged, c.Services, c.Methods, len(c.Findings))
}

// inspectPublishedMarkup — ЕДИНСТВЕННОЕ место решения. Настоящий обход и
// инъекция зовут ЕЁ ЖЕ: проверка, чью способность падать доказывают на другой
// функции, не доказана вовсе.
func inspectPublishedMarkup(files []protoreflect.FileDescriptor, contractsInTree []string) markupCensus {
	c := markupCensus{ContractsInTree: len(contractsInTree)}

	judged := map[string]protoreflect.FileDescriptor{}
	for _, fd := range files {
		judged[fd.Path()] = fd
	}

	// Контракт лежит в доме и не слинкован сюда — ОН НЕ СУДИТСЯ, и молчание о
	// нём неотличимо от исправности.
	for _, path := range contractsInTree {
		if _, ok := judged[path]; !ok {
			c.Findings = append(c.Findings, markupFinding{Contract: path,
				Why: "контракт лежит в доме, но в эту пробу не слинкован — значит не судится; " +
					"добавьте слепой импорт его заглушки"})
		}
	}
	inTree := map[string]bool{}
	for _, path := range contractsInTree {
		inTree[path] = true
	}

	paths := make([]string, 0, len(judged))
	for path := range judged {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		fd := judged[path]
		c.ContractsJudged++
		if !inTree[path] {
			c.Findings = append(c.Findings, markupFinding{Contract: path,
				Why: "заглушка публикуется этим модулем, а контракта под этим путём в доме нет — " +
					"вход и производное разошлись"})
		}
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			c.Services++
			for j := 0; j < sd.Methods().Len(); j++ {
				md := sd.Methods().Get(j)
				c.Methods++
				if permissionOf(md) == "" {
					c.Findings = append(c.Findings, markupFinding{
						Contract: path,
						Method:   string(sd.FullName()) + "/" + string(md.Name()),
						Why: "нет непустой разметки (corelib.authz.v1.permission); производитель каталога " +
							"края эмитит пустую строку, а пустая строка НЕ равна \"<exempt>\" — " +
							"метод уходит в полосу отношения с пустым отношением, то есть в отказ",
					})
				}
			}
		}
	}

	// Пустой обход — ОТКАЗ, а не пустой успех: у этого модуля есть службы, и
	// ноль методов означает, что смотреть было не на что.
	if c.Methods == 0 {
		c.Findings = append(c.Findings, markupFinding{Contract: "(весь дом)",
			Why: "методов осмотрено НОЛЬ — обход беспредметен; «находок нет» здесь означало бы " +
				"«ничего не прочитано»"})
	}
	return c
}

// permissionOf читает разметку с дескриптора — узлом, а не текстом.
func permissionOf(md protoreflect.MethodDescriptor) string {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	if opts == nil {
		return ""
	}
	if !proto.HasExtension(opts, authzv1.E_Permission) {
		return ""
	}
	s, _ := proto.GetExtension(opts, authzv1.E_Permission).(string)
	return s
}

func stringOptionOf(md protoreflect.MethodDescriptor, xt protoreflect.ExtensionType) string {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	if opts == nil || !proto.HasExtension(opts, xt) {
		return ""
	}
	s, _ := proto.GetExtension(opts, xt).(string)
	return s
}

func boolOptionOf(md protoreflect.MethodDescriptor, xt protoreflect.ExtensionType) bool {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	if opts == nil || !proto.HasExtension(opts, xt) {
		return false
	}
	b, _ := proto.GetExtension(opts, xt).(bool)
	return b
}

// publishedByThisModule — файлы, чьи заглушки публикует ЭТОТ модуль. Признак
// ВЫВОДИТСЯ из `go_package`, а не выписывается перечнем.
func publishedByThisModule(t *testing.T) []protoreflect.FileDescriptor {
	t.Helper()
	prefix := modulePath(t) + "/api/"
	var out []protoreflect.FileDescriptor
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		opts, _ := fd.Options().(*descriptorpb.FileOptions)
		if opts != nil && strings.HasPrefix(opts.GetGoPackage(), prefix) {
			out = append(out, fd)
		}
		return true
	})
	return out
}

// modulePath берётся из `go.mod`, а не из литерала: литерал рядом с объявлением
// есть второе место об одном предмете.
func modulePath(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("go.mod не прочитан: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("в go.mod нет строки module — предикат вывода пути модуля беспредметен")
	return ""
}

// contractsInHome — свои контракты дома, путями регистрации (относительно
// `proto/`). Состав берётся из ИНДЕКСА git, а не обходом диска: иначе вердикт
// стал бы свойством рабочего каталога.
func contractsInHome(t *testing.T) []string {
	t.Helper()
	abs, err := filepath.Abs(protoHome)
	if err != nil {
		t.Fatalf("абсолютный путь дома контрактов: %v", err)
	}
	files, err := treecorpus.UnderWithSuffix(abs, ".proto")
	if err != nil {
		t.Fatalf("состав дома контрактов не прочитан: %v", err)
	}
	var out []string
	for _, f := range files {
		rel, err := filepath.Rel(abs, f)
		if err != nil {
			t.Fatalf("относительный путь для %s: %v", f, err)
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, vendoredInputPrefix) {
			continue
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func TestEveryPublishedRPCCarriesAuthzMarkup(t *testing.T) {
	census := inspectPublishedMarkup(publishedByThisModule(t), contractsInHome(t))
	t.Log(census.String())
	if len(census.Findings) == 0 {
		return
	}
	for _, f := range census.Findings {
		t.Errorf("НАХОДКА %s", f)
	}
	t.Fatalf("разметка снята в ЭТОМ дереве, а краснеет прогон ПОТРЕБИТЕЛЯ и не раньше подъёма пина; %s",
		census.String())
}

// TestPublishedMarkupValuesAreTheContractTheCatalogPublishes — замок на ЗНАЧЕНИЯ.
//
// Гейт выше требует «разметка есть». Значения он не сторожит, а они часть
// контракта: их читает производитель каталога, и смена их побочным эффектом
// меняет решение о доступе, не роняя ничего. Перечень поимённый НАМЕРЕННО —
// это регрессионный замок, а не второе объявление: метод, исчезнувший из
// дерева, называется здесь по имени, то есть замок истекает громко.
func TestPublishedMarkupValuesAreTheContractTheCatalogPublishes(t *testing.T) {
	type want struct {
		permission   string
		exemptReason string
		acrMin       string
		scopeFilter  bool
	}
	expected := map[string]want{
		"kacho.cloud.operation.OperationService/Get":    {permission: "<exempt>", exemptReason: "HANDLER_DECIDES"},
		"kacho.cloud.operation.OperationService/Cancel": {permission: "<exempt>", exemptReason: "HANDLER_DECIDES"},
		"corelib.subscription.InternalSubscriptionService/Subscribe": {
			permission: "platform.subscription.subscribe", acrMin: "1", scopeFilter: true},
	}

	seen := map[string]bool{}
	for _, fd := range publishedByThisModule(t) {
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				md := sd.Methods().Get(j)
				key := string(sd.FullName()) + "/" + string(md.Name())
				w, ok := expected[key]
				if !ok {
					continue
				}
				seen[key] = true
				if got := permissionOf(md); got != w.permission {
					t.Errorf("%s: permission %q, ожидалось %q", key, got, w.permission)
				}
				if got := stringOptionOf(md, authzv1.E_ExemptReason); got != w.exemptReason {
					t.Errorf("%s: exempt_reason %q, ожидалось %q", key, got, w.exemptReason)
				}
				if got := stringOptionOf(md, authzv1.E_RequiredAcrMin); got != w.acrMin {
					t.Errorf("%s: required_acr_min %q, ожидалось %q", key, got, w.acrMin)
				}
				if got := boolOptionOf(md, authzv1.E_ScopeFiltered); got != w.scopeFilter {
					t.Errorf("%s: scope_filtered %v, ожидалось %v", key, got, w.scopeFilter)
				}
			}
		}
	}
	t.Logf("перепись: замков %d, найдено в дереве %d", len(expected), len(seen))
	for key := range expected {
		if !seen[key] {
			t.Errorf("НАХОДКА %s: метода нет в опубликованных контрактах — замок на значения потерял предмет; "+
				"снимите его ВМЕСТЕ с методом, а не молча", key)
		}
	}
}

// syntheticPublished — синтетический контракт дома: одна служба, два метода.
// marked несёт разметку, bare — нет. Различие между ними ОДНО и названо.
func syntheticPublished(t *testing.T, path string, markedPermission string) protoreflect.FileDescriptor {
	t.Helper()
	markedOpts := &descriptorpb.MethodOptions{}
	if markedPermission != "" {
		proto.SetExtension(markedOpts, authzv1.E_Permission, markedPermission)
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String(path),
		Package: proto.String("corelib.synthetic"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("github.com/PRO-Robotech/corelib/api/corelib/synthetic;syntheticv1"),
		},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Ping")}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("SyntheticService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       proto.String("Marked"),
					InputType:  proto.String(".corelib.synthetic.Ping"),
					OutputType: proto.String(".corelib.synthetic.Ping"),
					Options:    markedOpts,
				},
				{
					Name:       proto.String("Bare"),
					InputType:  proto.String(".corelib.synthetic.Ping"),
					OutputType: proto.String(".corelib.synthetic.Ping"),
				},
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("синтетический контракт не построен: %v", err)
	}
	return fd
}

// TestPublishedMarkupGateFindsTheDefectAndSparesTheLegalTwin — ИНЪЕКЦИЯ В ОБЕ
// СТОРОНЫ на ОДНОМ обходе: дефект находится с координатой, законный близнец
// молчит. Один прогон, а не два, потому что оба метода лежат в одном
// синтетическом контракте и различаются РОВНО разметкой.
func TestPublishedMarkupGateFindsTheDefectAndSparesTheLegalTwin(t *testing.T) {
	const path = "corelib/synthetic/synthetic.proto"
	fd := syntheticPublished(t, path, "synthetic.pings.ping")

	census := inspectPublishedMarkup([]protoreflect.FileDescriptor{fd}, []string{path})
	t.Log(census.String())

	if census.Methods != 2 {
		t.Fatalf("методов осмотрено %d, ожидалось 2 — обход не дошёл до предмета", census.Methods)
	}
	if len(census.Findings) != 1 {
		t.Fatalf("находок %d, ожидалась ровно одна: %v", len(census.Findings), census.Findings)
	}
	got := census.Findings[0].String()
	if !strings.Contains(got, "SyntheticService/Bare") {
		t.Fatalf("находка не называет координату неразмеченного метода: %s", got)
	}
	if strings.Contains(got, "SyntheticService/Marked") {
		t.Fatalf("находка задевает ЗАКОННОГО близнеца — гейт ловит форму, а не существо: %s", got)
	}
}

// TestPublishedMarkupGateRefusesAnEmptyWalk — «ноль находок» на пустом обходе
// обязано быть ОТКАЗОМ. Без этого гейт, потерявший вход, неотличим от чистого
// дерева.
func TestPublishedMarkupGateRefusesAnEmptyWalk(t *testing.T) {
	census := inspectPublishedMarkup(nil, nil)
	if len(census.Findings) == 0 {
		t.Fatalf("пустой обход прошёл молча: %s", census.String())
	}
	if !strings.Contains(census.Findings[0].String(), "беспредметен") {
		t.Fatalf("отказ на пустом обходе не называет причины: %s", census.Findings[0])
	}
}

// TestPublishedMarkupGateNoticesAContractItDoesNotJudge — контракт дома,
// не слинкованный в пробу, есть НАХОДКА, а не тишина.
func TestPublishedMarkupGateNoticesAContractItDoesNotJudge(t *testing.T) {
	const judged = "corelib/synthetic/synthetic.proto"
	const unlinked = "corelib/synthetic/other.proto"
	fd := syntheticPublished(t, judged, "synthetic.pings.ping")

	census := inspectPublishedMarkup([]protoreflect.FileDescriptor{fd}, []string{judged, unlinked})
	var named bool
	for _, f := range census.Findings {
		if f.Contract == unlinked {
			named = true
		}
	}
	if !named {
		t.Fatalf("несудимый контракт не назван: %s / %v", census.String(), census.Findings)
	}
}
