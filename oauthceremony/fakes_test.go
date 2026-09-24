// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// fakes_test.go — подставки портов для проб ЭТОГО пакета.
//
// Они живут в `_test.go` намеренно: реализация хранилища в фундаменте
// немедленно стала бы тем, на что сошлются в рабочем пути, а здесь её нет ни
// в графе сборки потребителя, ни в перечне экспортированного.
//
// Подставка держит СЕМАНТИКУ ПОРТА, а не удобство пробы: погашение кода
// исполняется как одна операция под замком и возвращает честное число
// затронутых строк. Подставка, всегда отвечающая RowsTouched(1), сделала бы
// пробу повторного предъявления бессмысленной.
package oauthceremony_test

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

// codeRow — строка кода авторизации так, как её держит служба: грант, привязка
// к доказательству владения ключом (вызов и метод — колонки той же строки, как
// `code_challenge` и `code_challenge_method` у `authorization_codes` службы
// доступа) и отметка погашения.
type codeRow struct {
	grant     oauthceremony.GrantRecord
	challenge string
	method    string
	consumed  bool
}

type refreshRow struct {
	grant     oauthceremony.GrantRecord
	accessSig string
	rotated   bool
}

// memoryPorts — хранилище службы в памяти, реализующее ВСЕ порты церемонии.
type memoryPorts struct {
	mu      sync.Mutex
	clients map[string]oauthceremony.ClientRegistration
	codes   map[string]*codeRow
	access  map[string]oauthceremony.GrantRecord
	refresh map[string]*refreshRow

	// secrets — проверочные значения секретов клиентов, как их держит служба:
	// клиент → секрет. У службы здесь лежит значение хеш-функции, у подставки —
	// сам секрет; сверка — постоянного времени и у той, и у другой.
	secrets map[string]string
	// verifications — каждый вызов порта сверки секрета в порядке прихода: так
	// проба считает вызовы и видит, что порт получил.
	verifications []verifierCall
	// verifyOverride подменяет вердикт сверки. Пусто — сверка как есть.
	verifyOverride func(clientID string) (oauthceremony.SecretVerdict, error)

	// revoked — гранты, чьё семейство отозвано. Ведётся подставкой, чтобы
	// проба утверждала СОСТОЯНИЕ гранта после отказа, а не только текст
	// отказа.
	revoked map[string]bool

	// Подмены на время пробы контракта порта. Пусто — обычное поведение.
	consumeOverride func(signature string) (oauthceremony.StoreOutcome, error)
	storeOverride   func(signature string) (oauthceremony.StoreOutcome, error)
	lookupDelay     time.Duration
	// fetchRefreshOverride подменяет исход выборки токена обновления.
	fetchRefreshOverride func(signature string) (oauthceremony.GrantRecord, error)
	// fetchCodeOverride подменяет исход выборки кода авторизации.
	fetchCodeOverride func(signature string) (oauthceremony.AuthorizationCodeRecord, error)

	// refreshFetchGate — точка встречи одновременных выборок токена
	// обновления. Пусто — выборка не ждёт никого.
	refreshFetchGate *rendezvous

	// codeFetchGate, consumeGate — точки встречи одновременных обменов одного
	// кода: на выборке кода и перед его погашением. Пусто — не ждёт никто.
	codeFetchGate *rendezvous
	consumeGate   *rendezvous

	// revokeFailure — отказ порта отзыва. Пусто — отзыв исполняется.
	revokeFailure error

	// revocations — каждый вызов порта отзыва в порядке прихода: метод,
	// грант и ПРИЧИНА, которую назвала церемония. Ведётся подставкой, чтобы
	// проба утверждала причину, полученную портом, а не только факт вызова.
	revocations []revocationCall

	// issuer — порт выпуска токена доступа: у службы он живёт рядом с её
	// хранилищем и подписантом, здесь — рядом с подставкой хранилища.
	issuer *recordingIssuer
}

// verifierCall — один вызов порта сверки секрета так, как его увидела служба:
// о ком спросили, что предъявлено, какой срок пришёл с контекстом вызова и что
// порт ответил. limited — у контекста был срок; remaining — сколько от него
// оставалось в миг вызова; verdict — вердикт, который порт отдал церемонии.
type verifierCall struct {
	clientID  string
	presented string
	limited   bool
	remaining time.Duration
	verdict   oauthceremony.SecretVerdict
}

// decoySecret — приманка подставки: против неё сверяется секрет клиента,
// которого у службы нет, чтобы отказ стоил столько же, сколько отказ неверному
// секрету. Исход этой сверки выбрасывается.
const decoySecret = "decoy-verifier-of-the-declared-class"

// revocationCall — один вызов порта отзыва так, как его увидела служба.
type revocationCall struct {
	method  string
	grantID string
	reason  oauthceremony.RevocationReason
}

// recordingIssuer — подставка порта выпуска токена доступа.
//
// Держит СЕМАНТИКУ порта так, как её держит подписант службы: момент выпуска
// берётся с часов, которые проба вправе остановить — но не раньше начала
// секунды вызова, иначе выпуск нарушает контракт порта; срок — не дальше
// границы, названной церемонией (Session.ExpiresAt[TokenKindAccess]), и не
// длиннее своего потолка, в целых секундах. Опознание отвечает идентификатором
// ТОЛЬКО на токен, выпущенный этой подставкой: чужое значение —
// ErrGrantNotFound, как токен с чужой подписью у настоящего подписанта.
// Подставка, опознающая всякое предъявленное, сделала бы пробу подделки
// бессмысленной. Срока опознание не судит — как велит контракт: истёкший
// токен, выпущенный здесь, опознаётся своим jti.
type recordingIssuer struct {
	mu sync.Mutex

	// now — часы выпуска. Проба, утверждающая срок в ответе точным
	// равенством, останавливает их.
	now func() time.Time
	// ceiling — потолок срока, как MaxTokenTTL у подписанта службы.
	ceiling time.Duration

	// byToken — выпущенное: значение токена → выпуск.
	byToken map[string]oauthceremony.IssuedAccessToken
	// log — выпуски по порядку, с грантом, который церемония назвала.
	log []issuance

	// reshape правит выпуск перед возвратом: так проба контракта порта
	// меняет в выпуске РОВНО ОДИН факт. Пусто — выпуск как есть.
	reshape func(grant oauthceremony.GrantRecord, issued *oauthceremony.IssuedAccessToken)
	// issueFailure — отказ выпуска. Пусто — выпуск исполняется.
	issueFailure error
	// identifyFailure — отказ опознания. Пусто — опознание исполняется.
	identifyFailure error
	// identifyEmpty — опознание отвечает пустым идентификатором без отказа.
	identifyEmpty bool
	// hold задерживает выпуск ДО того, как взяты часы выпуска, и без замка
	// подставки: так проба ставит выпуск после события в других портах.
	// Отказ hold — отказ выпуска. Пусто — выпуск не ждёт.
	hold func(ctx context.Context) error
}

// issuance — один выпуск: что церемония назвала и что подставка вернула.
type issuance struct {
	grant  oauthceremony.GrantRecord
	issued oauthceremony.IssuedAccessToken
}

func newRecordingIssuer() *recordingIssuer {
	return &recordingIssuer{
		now:     time.Now,
		ceiling: tokenpolicy.MaxTokenTTL,
		byToken: map[string]oauthceremony.IssuedAccessToken{},
	}
}

// IssueAccessToken выпускает токен сроком не дальше границы церемонии.
func (f *recordingIssuer) IssueAccessToken(ctx context.Context, grant oauthceremony.GrantRecord) (oauthceremony.IssuedAccessToken, error) {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		if err := hold(ctx); err != nil {
			return oauthceremony.IssuedAccessToken{}, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.issueFailure != nil {
		return oauthceremony.IssuedAccessToken{}, f.issueFailure
	}
	notAfter, named := grant.Session.ExpiresAt[oauthceremony.TokenKindAccess]
	if !named {
		return oauthceremony.IssuedAccessToken{}, errors.New("recording issuer: the ceremony named no expiry bound for the access token")
	}
	issuedAt := f.now()
	lifetime := min(f.ceiling, notAfter.Sub(issuedAt)).Truncate(time.Second)
	if lifetime <= 0 {
		return oauthceremony.IssuedAccessToken{}, errors.New("recording issuer: the expiry bound has already passed")
	}

	token, id := randomHex(24), "jti-"+randomHex(12)
	issued := oauthceremony.IssuedAccessToken{
		Token:     "at." + token,
		ID:        id,
		IssuedAt:  issuedAt,
		ExpiresAt: issuedAt.Add(lifetime),
	}
	if f.reshape != nil {
		f.reshape(grant, &issued)
	}
	f.byToken[issued.Token] = issued
	f.log = append(f.log, issuance{grant: grant, issued: issued})
	return issued, nil
}

// IdentifyAccessToken отвечает идентификатором выпущенного здесь токена.
func (f *recordingIssuer) IdentifyAccessToken(_ context.Context, token string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case f.identifyFailure != nil:
		return "", f.identifyFailure
	case f.identifyEmpty:
		return "", nil
	}
	issued, found := f.byToken[token]
	if !found {
		return "", oauthceremony.ErrGrantNotFound
	}
	return issued.ID, nil
}

// issuances отдаёт копию журнала выпусков.
func (f *recordingIssuer) issuances() []issuance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]issuance(nil), f.log...)
}

// setIdentifyFailure меняет отказ опознания под замком: проба ставит и снимает
// его между операциями.
func (f *recordingIssuer) setIdentifyFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identifyFailure = err
}

// setIssueFailure — то же для отказа выпуска.
func (f *recordingIssuer) setIssueFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issueFailure = err
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("НЕ ВЫПОЛНИЛОСЬ: источник случайности подставки отказал: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// rendezvous — встреча ровно n участников. Пока не собрались все, каждый
// пришедший ждёт; собрались — проходят все, и последующие приходы не ждут.
//
// Зачем: одновременный повтор токена обновления воспроизводится ТОЛЬКО если
// оба запроса прошли выборку до того, как хоть один обернул токен. Без встречи
// запросы почти всегда исполнялись бы по очереди, и проба «одновременного»
// повтора проверяла бы последовательный.
//
// Встреча ставится ПОСЛЕ чтения строки, а не до него: встреча до чтения
// создаёт одновременность прихода, но не одновременность прочитанного, и
// met() сообщал бы о созданном условии, которого проба не создала.
type rendezvous struct {
	mu      sync.Mutex
	need    int
	arrived int
	all     chan struct{}
}

func newRendezvous(n int) *rendezvous {
	return &rendezvous{need: n, all: make(chan struct{})}
}

// meet отмечает приход и ждёт остальных. Срок ожидания — срок вызова порта:
// не собравшаяся встреча кончается отказом, а не зависанием пробы.
func (r *rendezvous) meet(ctx context.Context) error {
	r.mu.Lock()
	if r.arrived >= r.need {
		r.mu.Unlock()
		return nil
	}
	r.arrived++
	if r.arrived == r.need {
		close(r.all)
	}
	r.mu.Unlock()

	select {
	case <-r.all:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// met отвечает, собрались ли все участники. Проба, у которой встреча не
// состоялась, одновременности не создала — её исход «не выполнилось», а не
// зелёный и не красный.
func (r *rendezvous) met() bool {
	select {
	case <-r.all:
		return true
	default:
		return false
	}
}

// liveArtifactsOf — сколько у гранта живых артефактов: токенов доступа и
// токенов обновления, которые выборка отдала бы как годные.
func (m *memoryPorts) liveArtifactsOf(grantID string) (access, refresh int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.revoked[grantID] {
		return 0, 0
	}
	for _, grant := range m.access {
		if grant.GrantID == grantID {
			access++
		}
	}
	for _, row := range m.refresh {
		if row.grant.GrantID == grantID && !row.rotated {
			refresh++
		}
	}
	return access, refresh
}

// familyRevoked — отозвано ли семейство гранта.
func (m *memoryPorts) familyRevoked(grantID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revoked[grantID]
}

// revocationsOf — вызовы порта отзыва по гранту, в порядке прихода.
func (m *memoryPorts) revocationsOf(grantID string) []revocationCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	var calls []revocationCall
	for _, call := range m.revocations {
		if call.grantID == grantID {
			calls = append(calls, call)
		}
	}
	return calls
}

// storedCodeView — единственная выданная запись кода так, как её видит служба.
type storedCodeView struct {
	signature string
	challenge string
	method    string
	form      map[string][]string
}

// storedCode отдаёт единственную запись кода. Запись не одна — проба не
// создала своего условия, и это «не выполнилось», а не красное.
func (m *memoryPorts) storedCode(t *testing.T) storedCodeView {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.codes) != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: в хранилище кодов %d шт, ожидался 1", len(m.codes))
	}
	for signature, row := range m.codes {
		return storedCodeView{signature: signature, challenge: row.challenge, method: row.method, form: row.grant.Form}
	}
	return storedCodeView{}
}

// rebindStoredCode переписывает привязку единственной записи кода — так, как её
// переписала бы служба (или порча строки) между выдачей и обменом.
func (m *memoryPorts) rebindStoredCode(t *testing.T, challenge, method string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.codes) != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: в хранилище кодов %d шт, ожидался 1", len(m.codes))
	}
	for _, row := range m.codes {
		row.challenge, row.method = challenge, method
	}
}

// sweepConsumedCode снимает запись погашенного кода под подписью signature —
// так, как её сняла бы уборка службы. Под подписью нет погашенного кода —
// проба не создала своего условия, и это «не выполнилось», а не красное.
func (m *memoryPorts) sweepConsumedCode(t *testing.T, signature string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.codes[signature]
	if !found || !row.consumed {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: под подписью %s погашенного кода нет (запись есть: %t) — снимать нечего",
			signature, found)
	}
	delete(m.codes, signature)
}

// codeExpiresAt — срок, записанный у кода под подписью signature: его движок
// читает, решая, истёк ли код. Записи нет или срока в ней нет — проба не
// создала своего условия.
func (m *memoryPorts) codeExpiresAt(t *testing.T, signature string) time.Time {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.codes[signature]
	if !found {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: под подписью %s записи кода нет", signature)
	}
	expiresAt := row.grant.Session.ExpiresAt[oauthceremony.TokenKindAuthorizationCode]
	if expiresAt.IsZero() {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: у записи кода под подписью %s срока нет: %v", signature, row.grant.Session.ExpiresAt)
	}
	return expiresAt
}

// ageCodeRecord старит запись кода под подписью signature на by: каждое
// мгновение записи сдвигается на by назад. Всякий, кто сравнивает записанное
// мгновение с нынешним — движок, мост, служба, — видит у этой записи by
// прошедшего времени. Истечение кода поэтому наблюдается без паузы на часах, и
// исход пробы не зависит от того, сколько процессора досталось её шагам.
//
// Сдвигаются ВСЕ мгновения записи гранта, а не один срок: запись со сдвинутым
// сроком и прежним мигом выдачи — не прошедшее время, а другая запись, и
// проверка, судящая срок от мига выдачи, её не узнала бы. Перечня полей у
// подставки нет: мгновения находит обход самой записи (agedCopy), и поле,
// которое запись получит завтра, состарится вместе с прочими. Перечень,
// выписанный рукой, однажды уже не узнал поля, пришедшего в запись позже него.
//
// Состаренность судится исходом, а не устройством сдвига: мгновения записи
// снимаются до и после (instantsOf) и сверяются поштучно. Записи нет, мига
// выдачи нет, форма записи сдвигу не поддаётся, или хоть одно мгновение не
// сдвинулось ровно на by, — проба не создала своего условия.
func (m *memoryPorts) ageCodeRecord(t *testing.T, signature string, by time.Duration) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.codes[signature]
	if !found {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: под подписью %s записи кода нет — старить нечего", signature)
	}
	if row.grant.IssuedAt.IsZero() {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: у записи кода под подписью %s нет мига выдачи — сдвигать не от чего", signature)
	}
	aged, err := agedCopy(row.grant, -by)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: запись кода под подписью %s не состарить: %v", signature, err)
	}
	off, err := instantsNotShifted(row.grant, aged, -by)
	switch {
	case err != nil:
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: мгновения записи кода под подписью %s не снять: %v", signature, err)
	case len(off) > 0:
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: мгновения %v записи кода не сдвинуты на %s: состаренная запись не была бы "+
			"прошедшим временем", off, -by)
	}
	row.grant = aged
}

// agedCopy — копия value, в которой каждое ненулевое мгновение (time.Time где
// угодно по составу значения: само, под указателем и под `any`, в срезе, в
// массиве, в значении карты, во вложенной структуре) сдвинуто на by.
//
// Копия, а не правка на месте: карты и срезы записи могли прийти из сеанса
// движка, и правка на месте тронула бы его. Нулевое мгновение — «значения
// нет», а не момент, и остаётся нулевым: состаренное отсутствие стало бы
// значением. Две формы сдвигу не поддаются, и обе — отказ, а не пропуск:
// неэкспортированное поле, чей тип несёт мгновение (его не записать), и ключ
// карты, чей тип несёт мгновение (сдвиг ключа менял бы саму карту).
func agedCopy[T any](value T, by time.Duration) (T, error) {
	aged, err := agedValue(reflect.ValueOf(&value).Elem(), by, reflect.TypeFor[T]().Name())
	if err != nil {
		var zero T
		return zero, err
	}
	return aged.Interface().(T), nil
}

func agedValue(v reflect.Value, by time.Duration, path string) (reflect.Value, error) {
	if v.Type() == timeType {
		at := v.Interface().(time.Time)
		if at.IsZero() {
			return v, nil
		}
		return reflect.ValueOf(at.Add(by)), nil
	}
	out := reflect.New(v.Type()).Elem()
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v, nil
		}
		elem, err := agedValue(v.Elem(), by, path)
		if err != nil {
			return reflect.Value{}, err
		}
		out.Set(reflect.New(v.Type().Elem()))
		out.Elem().Set(elem)
	case reflect.Interface:
		if v.IsNil() {
			return v, nil
		}
		elem, err := agedValue(v.Elem(), by, path)
		if err != nil {
			return reflect.Value{}, err
		}
		out.Set(elem)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice {
			if v.IsNil() {
				return v, nil
			}
			out = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		}
		for i := range v.Len() {
			elem, err := agedValue(v.Index(i), by, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(elem)
		}
	case reflect.Map:
		if v.IsNil() {
			return v, nil
		}
		if carriesInstant(v.Type().Key(), map[reflect.Type]bool{}) {
			return reflect.Value{}, fmt.Errorf("ключ карты %s (%s) несёт мгновение: сдвиг ключа менял бы саму карту",
				path, v.Type().Key())
		}
		out = reflect.MakeMapWithSize(v.Type(), v.Len())
		for iter := v.MapRange(); iter.Next(); {
			elem, err := agedValue(iter.Value(), by, fmt.Sprintf("%s[%v]", path, iter.Key()))
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(iter.Key(), elem)
		}
	case reflect.Struct:
		out.Set(v)
		for i := range v.NumField() {
			field := v.Type().Field(i)
			fieldPath := path + "." + field.Name
			if !field.IsExported() {
				if carriesInstant(field.Type, map[reflect.Type]bool{}) {
					return reflect.Value{}, fmt.Errorf("поле %s не экспортировано, а его тип несёт мгновение: "+
						"записать сдвинутое нечем", fieldPath)
				}
				continue
			}
			elem, err := agedValue(v.Field(i), by, fieldPath)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Field(i).Set(elem)
		}
	default:
		return v, nil
	}
	return out, nil
}

// instantsOf снимает все ненулевые мгновения значения v по путям — поле через
// точку, элемент среза и массива в [i], значение карты в [ключ] — в into.
// Обход по составу тот же, что у agedValue, но код другой: сверка сдвига не
// судит сдвиг им же самим. Отказ — те же формы, что не поддаются agedValue.
func instantsOf(v reflect.Value, path string, into map[string]time.Time) error {
	if v.Type() == timeType {
		if at := v.Interface().(time.Time); !at.IsZero() {
			into[path] = at
		}
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return instantsOf(v.Elem(), path, into)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if err := instantsOf(v.Index(i), fmt.Sprintf("%s[%d]", path, i), into); err != nil {
				return err
			}
		}
	case reflect.Map:
		if carriesInstant(v.Type().Key(), map[reflect.Type]bool{}) {
			return fmt.Errorf("ключ карты %s (%s) несёт мгновение", path, v.Type().Key())
		}
		for iter := v.MapRange(); iter.Next(); {
			if err := instantsOf(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()), into); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := range v.NumField() {
			switch field := v.Type().Field(i); {
			case field.IsExported():
				if err := instantsOf(v.Field(i), path+"."+field.Name, into); err != nil {
					return err
				}
			case carriesInstant(field.Type, map[reflect.Type]bool{}):
				return fmt.Errorf("поле %s.%s не экспортировано, а его тип несёт мгновение", path, field.Name)
			}
		}
	}
	return nil
}

// instantsNotShifted сверяет before и after поштучно по мгновениям и называет
// пути, где мгновение не сдвинуто ровно на by: у after его нет, оно иное, или
// у after есть мгновение, которого не было у before. Пусто — сдвинуто всё.
// Сверка, не нашедшая у before ни одного мгновения, — не «сдвинуто всё», а
// «сверять нечего», и это отказ.
func instantsNotShifted[T any](before, after T, by time.Duration) ([]string, error) {
	name := reflect.TypeFor[T]().Name()
	was, now := map[string]time.Time{}, map[string]time.Time{}
	if err := instantsOf(reflect.ValueOf(before), name, was); err != nil {
		return nil, err
	}
	if err := instantsOf(reflect.ValueOf(after), name, now); err != nil {
		return nil, err
	}
	if len(was) == 0 {
		return nil, fmt.Errorf("у %s нет ни одного мгновения — сверять нечего", name)
	}
	var off []string
	for path, at := range was {
		if got, found := now[path]; !found || !got.Equal(at.Add(by)) {
			off = append(off, path)
		}
	}
	for path := range now {
		if _, found := was[path]; !found {
			off = append(off, path)
		}
	}
	slices.Sort(off)
	return off, nil
}

var timeType = reflect.TypeFor[time.Time]()

// carriesInstant — есть ли в типе typ time.Time где угодно по его составу.
// Значения под `any` статически не видны; их мгновения agedValue и instantsOf
// находят по самому значению.
func carriesInstant(typ reflect.Type, seen map[reflect.Type]bool) bool {
	if typ == timeType {
		return true
	}
	if seen[typ] {
		return false
	}
	seen[typ] = true
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return carriesInstant(typ.Elem(), seen)
	case reflect.Map:
		return carriesInstant(typ.Key(), seen) || carriesInstant(typ.Elem(), seen)
	case reflect.Struct:
		for field := range typ.Fields() {
			if carriesInstant(field.Type, seen) {
				return true
			}
		}
	}
	return false
}

// recordsUnder — сколько записей ВСЕХ хранилищ подставки лежит под подписью
// signature. У выданного кода она ровно одна — его собственная.
func (m *memoryPorts) recordsUnder(signature string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0
	if _, found := m.codes[signature]; found {
		n++
	}
	if _, found := m.access[signature]; found {
		n++
	}
	if _, found := m.refresh[signature]; found {
		n++
	}
	return n
}

func newMemoryPorts() *memoryPorts {
	return &memoryPorts{
		clients: map[string]oauthceremony.ClientRegistration{},
		secrets: map[string]string{},
		codes:   map[string]*codeRow{},
		access:  map[string]oauthceremony.GrantRecord{},
		refresh: map[string]*refreshRow{},
		revoked: map[string]bool{},
		issuer:  newRecordingIssuer(),
	}
}

func (m *memoryPorts) ports() oauthceremony.Ports {
	return oauthceremony.Ports{
		Clients:            m,
		AuthorizationCodes: m,
		AccessTokens:       m,
		RefreshTokens:      m,
		Grants:             m,
		AccessTokenIssuer:  m.issuer,
		ClientSecrets:      m,
	}
}

// ── ClientDirectory ─────────────────────────────────────────────────────────

func (m *memoryPorts) LookupClient(ctx context.Context, clientID string) (oauthceremony.ClientRegistration, error) {
	if m.lookupDelay > 0 {
		select {
		case <-time.After(m.lookupDelay):
		case <-ctx.Done():
			return oauthceremony.ClientRegistration{}, ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	reg, found := m.clients[clientID]
	if !found {
		return oauthceremony.ClientRegistration{}, oauthceremony.ErrGrantNotFound
	}
	return reg, nil
}

// ── ClientSecretVerifier ────────────────────────────────────────────────────

// VerifyClientSecret сверяет секрет так, как сверяет служба: постоянным
// временем, а для клиента без проверочного значения — против приманки, чей
// исход выбрасывается. Каждый вызов записывается вместе со сроком контекста.
func (m *memoryPorts) VerifyClientSecret(ctx context.Context, clientID string, presented oauthceremony.PresentedSecret) (oauthceremony.SecretVerdict, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	call := verifierCall{clientID: clientID, presented: presented.Reveal()}
	if deadline, limited := ctx.Deadline(); limited {
		call.limited, call.remaining = true, time.Until(deadline)
	}
	verdict, err := m.secretVerdict(clientID, presented)
	call.verdict = verdict
	m.verifications = append(m.verifications, call)
	return verdict, err
}

// secretVerdict — вердикт сверки: подменённый пробой либо по проверочному
// значению. Зовётся под замком m.mu.
func (m *memoryPorts) secretVerdict(clientID string, presented oauthceremony.PresentedSecret) (oauthceremony.SecretVerdict, error) {
	if m.verifyOverride != nil {
		return m.verifyOverride(clientID)
	}
	want, found := m.secrets[clientID]
	if !found {
		want = decoySecret
	}
	matched := subtle.ConstantTimeCompare([]byte(want), []byte(presented.Reveal())) == 1
	if !found || !matched {
		return oauthceremony.SecretMismatched, nil
	}
	return oauthceremony.SecretMatched, nil
}

// verificationLog отдаёт копию журнала вызовов порта сверки.
func (m *memoryPorts) verificationLog() []verifierCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]verifierCall(nil), m.verifications...)
}

// setVerifyOverride меняет вердикт сверки под замком: проба ставит его между
// подготовкой предмета и операцией.
func (m *memoryPorts) setVerifyOverride(verdict func(clientID string) (oauthceremony.SecretVerdict, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verifyOverride = verdict
}

// ── AuthorizationCodeVault ──────────────────────────────────────────────────

func (m *memoryPorts) StoreAuthorizationCode(_ context.Context, signature string, code oauthceremony.AuthorizationCodeRecord) (oauthceremony.StoreOutcome, error) {
	if m.storeOverride != nil {
		return m.storeOverride(signature)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, taken := m.codes[signature]; taken {
		return oauthceremony.StoreOutcome{}, oauthceremony.ErrStorageConflict
	}
	m.codes[signature] = &codeRow{
		grant:     code.Grant,
		challenge: code.ProofKey.Challenge,
		method:    string(code.ProofKey.Method),
	}
	return oauthceremony.RowsTouched(1), nil
}

// FetchAuthorizationCode читает строку ДО встречи — по той же причине, что
// FetchRefreshToken.
func (m *memoryPorts) FetchAuthorizationCode(ctx context.Context, signature string) (oauthceremony.AuthorizationCodeRecord, error) {
	rec, err := m.readCodeRow(signature)
	if m.codeFetchGate != nil {
		if meetErr := m.codeFetchGate.meet(ctx); meetErr != nil {
			return oauthceremony.AuthorizationCodeRecord{}, meetErr
		}
	}
	return rec, err
}

func (m *memoryPorts) readCodeRow(signature string) (oauthceremony.AuthorizationCodeRecord, error) {
	if m.fetchCodeOverride != nil {
		return m.fetchCodeOverride(signature)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.codes[signature]
	if !found {
		return oauthceremony.AuthorizationCodeRecord{}, oauthceremony.ErrGrantNotFound
	}
	rec := oauthceremony.AuthorizationCodeRecord{
		Grant: row.grant,
		ProofKey: oauthceremony.ProofKeyBinding{
			Challenge: row.challenge,
			Method:    oauthceremony.ProofKeyMethod(row.method),
		},
	}
	if row.consumed {
		// Запись отдаётся ВМЕСТЕ с отказом: по её гранту движок отзывает
		// выданные артефакты.
		return rec, oauthceremony.ErrAuthorizationCodeConsumed
	}
	return rec, nil
}

// ConsumeAuthorizationCode — одна операция под замком, как одна инструкция
// `UPDATE … WHERE consumed_at IS NULL` под замком строки.
func (m *memoryPorts) ConsumeAuthorizationCode(ctx context.Context, signature string) (oauthceremony.StoreOutcome, error) {
	if m.consumeOverride != nil {
		return m.consumeOverride(signature)
	}
	if m.consumeGate != nil {
		if err := m.consumeGate.meet(ctx); err != nil {
			return oauthceremony.StoreOutcome{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.codes[signature]
	if !found || row.consumed {
		return oauthceremony.RowsTouched(0), nil
	}
	row.consumed = true
	return oauthceremony.RowsTouched(1), nil
}

// ── AccessTokenVault ────────────────────────────────────────────────────────

func (m *memoryPorts) StoreAccessToken(_ context.Context, signature string, grant oauthceremony.GrantRecord) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, taken := m.access[signature]; taken {
		return oauthceremony.StoreOutcome{}, oauthceremony.ErrStorageConflict
	}
	m.access[signature] = grant
	return oauthceremony.RowsTouched(1), nil
}

func (m *memoryPorts) FetchAccessToken(_ context.Context, signature string) (oauthceremony.GrantRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	grant, found := m.access[signature]
	if !found || m.revoked[grant.GrantID] {
		// Отзыв семейства необратим: токен отозванного гранта не годен,
		// когда бы он ни был положен.
		return oauthceremony.GrantRecord{}, oauthceremony.ErrGrantNotFound
	}
	return grant, nil
}

func (m *memoryPorts) DropAccessToken(_ context.Context, signature string) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, found := m.access[signature]; !found {
		return oauthceremony.RowsTouched(0), nil
	}
	delete(m.access, signature)
	return oauthceremony.RowsTouched(1), nil
}

// ── RefreshTokenVault ───────────────────────────────────────────────────────

func (m *memoryPorts) StoreRefreshToken(_ context.Context, signature, accessSignature string, grant oauthceremony.GrantRecord) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, taken := m.refresh[signature]; taken {
		return oauthceremony.StoreOutcome{}, oauthceremony.ErrStorageConflict
	}
	m.refresh[signature] = &refreshRow{grant: grant, accessSig: accessSignature}
	return oauthceremony.RowsTouched(1), nil
}

// FetchRefreshToken ЧИТАЕТ СТРОКУ ДО ВСТРЕЧИ. Порядок несущий: встреча,
// поставленная до чтения, пропускает участников к чтению по одному, и второй
// почти всегда читает уже обёрнутый токен — то есть идёт ПОСЛЕДОВАТЕЛЬНЫМ
// путём, хотя встреча состоялась. Замер при встрече до чтения: с дефектом
// «ноль строк оборота не отмечает семейство» проба одновременного повтора
// была зелёной в 19 прогонах из 30. Чтение до встречи — оба участника видят
// токен необёрнутым, и ноль строк оборота получает ровно один из них.
func (m *memoryPorts) FetchRefreshToken(ctx context.Context, signature string) (oauthceremony.GrantRecord, error) {
	rec, err := m.readRefreshRow(signature)
	if m.refreshFetchGate != nil {
		if meetErr := m.refreshFetchGate.meet(ctx); meetErr != nil {
			return oauthceremony.GrantRecord{}, meetErr
		}
	}
	return rec, err
}

func (m *memoryPorts) readRefreshRow(signature string) (oauthceremony.GrantRecord, error) {
	if m.fetchRefreshOverride != nil {
		return m.fetchRefreshOverride(signature)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.refresh[signature]
	switch {
	case !found, m.revoked[row.grant.GrantID]:
		return oauthceremony.GrantRecord{}, oauthceremony.ErrGrantNotFound
	case row.rotated:
		// Повтор: грант отдаётся ВМЕСТЕ с отказом — по нему отзывается
		// семейство.
		return row.grant, oauthceremony.ErrRefreshTokenRotated
	default:
		return row.grant, nil
	}
}

func (m *memoryPorts) DropRefreshToken(_ context.Context, signature string) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, found := m.refresh[signature]; !found {
		return oauthceremony.RowsTouched(0), nil
	}
	delete(m.refresh, signature)
	return oauthceremony.RowsTouched(1), nil
}

func (m *memoryPorts) RotateRefreshToken(_ context.Context, grantID, signature string) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.refresh[signature]
	if !found || row.rotated || row.grant.GrantID != grantID || m.revoked[grantID] {
		return oauthceremony.RowsTouched(0), nil
	}
	row.rotated = true
	return oauthceremony.RowsTouched(1), nil
}

// ── GrantRevoker ────────────────────────────────────────────────────────────

// errReasonOutsideDictionary — отказ службы на причину вне её закрытого
// словаря. Подставка отказывает так же, как отказало бы ограничение таблицы
// службы: причина, которой служба не знает, не записывается молча.
var errReasonOutsideDictionary = errors.New("revoker: the revocation reason is outside the closed dictionary")

// admitRevocation записывает вызов порта отзыва и отвечает, принимает ли его
// служба. Вызывается под замком подставки.
func (m *memoryPorts) admitRevocation(method, grantID string, reason oauthceremony.RevocationReason) error {
	m.revocations = append(m.revocations, revocationCall{method: method, grantID: grantID, reason: reason})
	if m.revokeFailure != nil {
		return m.revokeFailure
	}
	if !reason.Declared() {
		return errReasonOutsideDictionary
	}
	return nil
}

func (m *memoryPorts) RevokeGrantRefreshTokens(_ context.Context, grantID string, reason oauthceremony.RevocationReason) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.admitRevocation("RevokeGrantRefreshTokens", grantID, reason); err != nil {
		return oauthceremony.StoreOutcome{}, err
	}

	m.revoked[grantID] = true
	var touched int64
	for signature, row := range m.refresh {
		if row.grant.GrantID == grantID {
			delete(m.refresh, signature)
			touched++
		}
	}
	return oauthceremony.RowsTouched(touched), nil
}

func (m *memoryPorts) RevokeGrantAccessTokens(_ context.Context, grantID string, reason oauthceremony.RevocationReason) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.admitRevocation("RevokeGrantAccessTokens", grantID, reason); err != nil {
		return oauthceremony.StoreOutcome{}, err
	}

	m.revoked[grantID] = true
	var touched int64
	for signature, grant := range m.access {
		if grant.GrantID == grantID {
			delete(m.access, signature)
			touched++
		}
	}
	return oauthceremony.RowsTouched(touched), nil
}

// Утверждения времени сборки: подставка обязана оставаться полной
// реализацией портов. Отпавший метод — отказ сборки, а не красная проба с
// неочевидным текстом.
var (
	_ oauthceremony.ClientDirectory        = (*memoryPorts)(nil)
	_ oauthceremony.AuthorizationCodeVault = (*memoryPorts)(nil)
	_ oauthceremony.AccessTokenVault       = (*memoryPorts)(nil)
	_ oauthceremony.RefreshTokenVault      = (*memoryPorts)(nil)
	_ oauthceremony.GrantRevoker           = (*memoryPorts)(nil)
	_ oauthceremony.AccessTokenIssuer      = (*recordingIssuer)(nil)
	_ oauthceremony.ClientSecretVerifier   = (*memoryPorts)(nil)
)

// ── Единица работы ──────────────────────────────────────────────────────────

// recordingUnitOfWork считает открытия, закрепления и откаты.
type recordingUnitOfWork struct {
	mu        sync.Mutex
	begun     int
	committed int
	rolled    int

	// hang — Begin не возвращается, пока жив контекст. Так ведёт себя
	// база, переставшая отвечать.
	hang bool
}

func (u *recordingUnitOfWork) Begin(ctx context.Context) (context.Context, error) {
	if u.hang {
		<-ctx.Done()
		return ctx, ctx.Err()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.begun++
	return ctx, nil
}

func (u *recordingUnitOfWork) Commit(context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.committed++
	return nil
}

func (u *recordingUnitOfWork) Rollback(context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rolled++
	return nil
}

func (u *recordingUnitOfWork) counts() (begun, committed, rolled int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.begun, u.committed, u.rolled
}

var _ oauthceremony.UnitOfWork = (*recordingUnitOfWork)(nil)
