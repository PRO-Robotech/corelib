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
	mu         sync.Mutex
	clients    map[string]oauthceremony.ClientRegistration
	codes      map[string]*codeRow
	access     map[string]oauthceremony.GrantRecord
	refresh    map[string]*refreshRow
	proof      map[string]oauthceremony.GrantRecord
	assertions map[string]time.Time

	// Подмены на время пробы контракта порта. Пусто — обычное поведение.
	consumeOverride func(signature string) (oauthceremony.StoreOutcome, error)
	storeOverride   func(signature string) (oauthceremony.StoreOutcome, error)
	lookupDelay     time.Duration
}

func newMemoryPorts() *memoryPorts {
	return &memoryPorts{
		clients:    map[string]oauthceremony.ClientRegistration{},
		codes:      map[string]*codeRow{},
		access:     map[string]oauthceremony.GrantRecord{},
		refresh:    map[string]*refreshRow{},
		proof:      map[string]oauthceremony.GrantRecord{},
		assertions: map[string]time.Time{},
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
		Assertions:         m,
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

func (m *memoryPorts) FetchAuthorizationCode(_ context.Context, signature string) (oauthceremony.GrantRecord, error) {
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
func (m *memoryPorts) ConsumeAuthorizationCode(_ context.Context, signature string) (oauthceremony.StoreOutcome, error) {
	if m.consumeOverride != nil {
		return m.consumeOverride(signature)
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
	if !found {
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

func (m *memoryPorts) FetchRefreshToken(_ context.Context, signature string) (oauthceremony.GrantRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	row, found := m.refresh[signature]
	if !found {
		return oauthceremony.GrantRecord{}, oauthceremony.ErrGrantNotFound
	}
	return row.grant, nil
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
	if !found || row.rotated || row.grant.GrantID != grantID {
		return oauthceremony.RowsTouched(0), nil
	}
	row.rotated = true
	return oauthceremony.RowsTouched(1), nil
}

// ── GrantRevoker ────────────────────────────────────────────────────────────

func (m *memoryPorts) RevokeGrantRefreshTokens(_ context.Context, grantID string) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

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

func (m *memoryPorts) FetchProofKeyRequest(_ context.Context, signature string) (oauthceremony.GrantRecord, error) {
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
	return oauthceremony.RowsTouched(1), nil
}

// ── AssertionReplayGuard ────────────────────────────────────────────────────

func (m *memoryPorts) ClaimAssertionID(_ context.Context, assertionID string, expiresAt time.Time) (oauthceremony.StoreOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, taken := m.assertions[assertionID]; taken {
		return oauthceremony.RowsTouched(0), nil
	}
	m.assertions[assertionID] = expiresAt
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
	_ oauthceremony.AssertionReplayGuard   = (*memoryPorts)(nil)
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
