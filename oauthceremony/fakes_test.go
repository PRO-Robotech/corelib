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
	"sync"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

type codeRow struct {
	grant    oauthceremony.GrantRecord
	consumed bool
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
	proof   map[string]oauthceremony.GrantRecord

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
	fetchCodeOverride func(signature string) (oauthceremony.GrantRecord, error)

	// refreshFetchGate — точка встречи одновременных выборок токена
	// обновления. Пусто — выборка не ждёт никого.
	refreshFetchGate *rendezvous

	// codeFetchGate, proofFetchGate, consumeGate — точки встречи
	// одновременных обменов одного кода: на выборке кода (до выборки PKCE),
	// на выборке запроса PKCE и перед погашением кода. Пусто — не ждёт никто.
	codeFetchGate  *rendezvous
	proofFetchGate *rendezvous
	consumeGate    *rendezvous

	// proofTaken — если задан, ВТОРАЯ и последующие выборки запроса PKCE ждут,
	// пока первую запись PKCE не снимут. Так воспроизводится ровно тот
	// порядок, в котором проигравший обмен находит запись PKCE уже снятой.
	proofTaken     chan struct{}
	proofTakenOnce sync.Once
	proofFetches   int

	// revokeFailure — отказ порта отзыва. Пусто — отзыв исполняется.
	revokeFailure error
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

func newMemoryPorts() *memoryPorts {
	return &memoryPorts{
		clients: map[string]oauthceremony.ClientRegistration{},
		codes:   map[string]*codeRow{},
		access:  map[string]oauthceremony.GrantRecord{},
		refresh: map[string]*refreshRow{},
		proof:   map[string]oauthceremony.GrantRecord{},
		revoked: map[string]bool{},
	}
}

func (m *memoryPorts) ports() oauthceremony.Ports {
	return oauthceremony.Ports{
		Clients:            m,
		AuthorizationCodes: m,
		AccessTokens:       m,
		RefreshTokens:      m,
		Grants:             m,
		ProofKeys:          m,
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

func (m *memoryPorts) StoreAuthorizationCode(_ context.Context, signature string, grant oauthceremony.GrantRecord) (oauthceremony.StoreOutcome, error) {
	if m.storeOverride != nil {
		return m.storeOverride(signature)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, taken := m.codes[signature]; taken {
		return oauthceremony.StoreOutcome{}, oauthceremony.ErrStorageConflict
	}
	m.codes[signature] = &codeRow{grant: grant}
	return oauthceremony.RowsTouched(1), nil
}

// FetchAuthorizationCode читает строку ДО встречи — по той же причине, что
// FetchRefreshToken.
func (m *memoryPorts) FetchAuthorizationCode(ctx context.Context, signature string) (oauthceremony.GrantRecord, error) {
	rec, err := m.readCodeRow(signature)
	if m.codeFetchGate != nil {
		if meetErr := m.codeFetchGate.meet(ctx); meetErr != nil {
			return oauthceremony.GrantRecord{}, meetErr
		}
	}
	return rec, err
}

func (m *memoryPorts) readCodeRow(signature string) (oauthceremony.GrantRecord, error) {
	if m.fetchCodeOverride != nil {
		return m.fetchCodeOverride(signature)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.codes[signature]
	switch {
	case !found:
		return oauthceremony.GrantRecord{}, oauthceremony.ErrGrantNotFound
	case row.consumed:
		// Грант отдаётся ВМЕСТЕ с отказом: по нему движок отзывает
		// выданные артефакты.
		return row.grant, oauthceremony.ErrAuthorizationCodeConsumed
	default:
		return row.grant, nil
	}
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

func (m *memoryPorts) RevokeGrantRefreshTokens(_ context.Context, grantID string) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.revokeFailure != nil {
		return oauthceremony.StoreOutcome{}, m.revokeFailure
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

func (m *memoryPorts) RevokeGrantAccessTokens(_ context.Context, grantID string) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.revokeFailure != nil {
		return oauthceremony.StoreOutcome{}, m.revokeFailure
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

// ── ProofKeyVault ───────────────────────────────────────────────────────────

func (m *memoryPorts) StoreProofKeyRequest(_ context.Context, signature string, grant oauthceremony.GrantRecord) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, taken := m.proof[signature]; taken {
		return oauthceremony.StoreOutcome{}, oauthceremony.ErrStorageConflict
	}
	m.proof[signature] = grant
	return oauthceremony.RowsTouched(1), nil
}

// FetchProofKeyRequest читает строку ДО встречи — по той же причине, что
// FetchRefreshToken.
func (m *memoryPorts) FetchProofKeyRequest(ctx context.Context, signature string) (oauthceremony.GrantRecord, error) {
	m.mu.Lock()
	m.proofFetches++
	later := m.proofFetches > 1
	m.mu.Unlock()
	if later && m.proofTaken != nil {
		select {
		case <-m.proofTaken:
		case <-ctx.Done():
			return oauthceremony.GrantRecord{}, ctx.Err()
		}
	}

	rec, err := m.readProofRow(signature)
	if m.proofFetchGate != nil {
		if meetErr := m.proofFetchGate.meet(ctx); meetErr != nil {
			return oauthceremony.GrantRecord{}, meetErr
		}
	}
	return rec, err
}

func (m *memoryPorts) readProofRow(signature string) (oauthceremony.GrantRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	grant, found := m.proof[signature]
	if !found {
		return oauthceremony.GrantRecord{}, oauthceremony.ErrGrantNotFound
	}
	return grant, nil
}

func (m *memoryPorts) DropProofKeyRequest(_ context.Context, signature string) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, found := m.proof[signature]; !found {
		return oauthceremony.RowsTouched(0), nil
	}
	delete(m.proof, signature)
	if m.proofTaken != nil {
		m.proofTakenOnce.Do(func() { close(m.proofTaken) })
	}
	return oauthceremony.RowsTouched(1), nil
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
	_ oauthceremony.ProofKeyVault          = (*memoryPorts)(nil)
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
