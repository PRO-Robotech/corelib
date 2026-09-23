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
	"encoding/hex"
	"errors"
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
// Сдвигаются ВСЕ мгновения записи гранта (agedGrantInstants), а не один срок:
// запись со сдвинутым сроком и прежним мигом выдачи — не прошедшее время, а
// другая запись, и проверка, судящая срок от мига выдачи, её не узнала бы.
// Записи нет, или у записи гранта появилось мгновение, которого подставка не
// старит, — проба не создала своего условия.
func (m *memoryPorts) ageCodeRecord(t *testing.T, signature string, by time.Duration) {
	t.Helper()
	requireGrantInstantsAged(t)
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.codes[signature]
	if !found {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: под подписью %s записи кода нет — старить нечего", signature)
	}
	if row.grant.IssuedAt.IsZero() {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: у записи кода под подписью %s нет мига выдачи — сдвигать не от чего", signature)
	}
	row.grant.IssuedAt = row.grant.IssuedAt.Add(-by)
	row.grant.Session.ExpiresAt = shiftedInstants(row.grant.Session.ExpiresAt, -by)
	row.grant.Session.NotAfter = shiftedInstants(row.grant.Session.NotAfter, -by)
}

// shiftedInstants — копия карты мгновений, сдвинутых на by. Копия, а не
// правка на месте: карта записи могла прийти из сеанса движка, и правка на
// месте тронула бы его.
func shiftedInstants(instants map[oauthceremony.TokenKind]time.Time, by time.Duration) map[oauthceremony.TokenKind]time.Time {
	if instants == nil {
		return nil
	}
	shifted := make(map[oauthceremony.TokenKind]time.Time, len(instants))
	for kind, at := range instants {
		shifted[kind] = at.Add(by)
	}
	return shifted
}

// agedGrantInstants — поля GrantRecord, несущие мгновения, которые старит
// ageCodeRecord, в порядке объявления.
var agedGrantInstants = []string{"IssuedAt", "Session.ExpiresAt", "Session.NotAfter"}

// requireGrantInstantsAged — предпосылка ageCodeRecord: ageCodeRecord старит
// ровно те поля, что несут мгновения. Перечень берётся обходом типа
// GrantRecord, а не по памяти: поле, в чьём типе где угодно есть time.Time —
// само, под указателем, в срезе, в ключе или значении карты, во вложенной
// структуре, — несёт мгновение. Значения под `any` (Claims) статически не
// видны; сроков по ним церемония не судит.
func requireGrantInstantsAged(t *testing.T) {
	t.Helper()
	if carried := grantInstantFields(); !slices.Equal(carried, agedGrantInstants) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: мгновения GrantRecord — %v, ageCodeRecord старит %v: состаренная запись "+
			"не была бы прошедшим временем", carried, agedGrantInstants)
	}
}

func grantInstantFields() []string {
	var carried []string
	var walk func(typ reflect.Type, prefix string)
	walk = func(typ reflect.Type, prefix string) {
		for field := range typ.Fields() {
			path := prefix + field.Name
			switch {
			case field.Type.Kind() == reflect.Struct && field.Type != timeType:
				walk(field.Type, path+".")
			case carriesInstant(field.Type, map[reflect.Type]bool{}):
				carried = append(carried, path)
			}
		}
	}
	walk(reflect.TypeFor[oauthceremony.GrantRecord](), "")
	return carried
}

var timeType = reflect.TypeFor[time.Time]()

// carriesInstant — есть ли в типе typ time.Time где угодно по его составу.
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
