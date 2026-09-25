// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// lifetime_test.go — сроки артефактов: каждый выданный артефакт несёт свой
// срок, и граница, названная службой, держит ВСЁ семейство гранта.
//
// Предмет — срок, записанный у артефакта (интроспекция и запись хранилища), а
// не только ответ обмена: движок отказывает по сроку, записанному у артефакта,
// и токен обновления без записанного срока им считается бессрочным.
package oauthceremony_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// slack — допуск на ход часов между замером и выдачей и на округление движка
// до секунды.
const slack = 5 * time.Second

func requireWithin(t *testing.T, what string, got, want time.Time) {
	t.Helper()
	if got.IsZero() {
		t.Errorf("%s: срок не записан у артефакта (нулевое время), ожидался около %s", what, want.Format(time.RFC3339))
		return
	}
	if d := got.Sub(want); d > slack || d < -slack {
		t.Errorf("%s: срок %s, ожидался %s ± %s", what, got.Format(time.RFC3339), want.Format(time.RFC3339), slack)
	}
}

func requireNotAfter(t *testing.T, what string, got, bound time.Time) {
	t.Helper()
	if got.IsZero() {
		t.Errorf("%s: срок не записан у артефакта, а граница %s названа", what, bound.Format(time.RFC3339))
		return
	}
	if got.After(bound) {
		t.Errorf("%s: срок %s позже границы семейства %s на %s", what,
			got.Format(time.RFC3339), bound.Format(time.RFC3339), got.Sub(bound))
	}
}

// exchangeGrant проходит церемонию с решением grant до первой пары.
func exchangeGrant(t *testing.T, ceremony *oauthceremony.Ceremony, grant oauthceremony.AuthorizationGrant) oauthceremony.TokenResult {
	t.Helper()
	result, err := completeWith(t, ceremony, authorizeRequest(), grant)
	if err != nil {
		t.Fatalf("CompleteAuthorization отказал: %v", err)
	}
	tokens, err := ceremony.Exchange(context.Background(), codeExchange(result.Parameters["code"][0]))
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	return tokens
}

// TestEveryArtifactCarriesItsLifetimeFromTheSettings — граница не названа:
// сроки из настроек (testAccessLifespan и сутки), и они ЗАПИСАНЫ у каждого
// артефакта — у первой пары и у пары оборота.
func TestEveryArtifactCarriesItsLifetimeFromTheSettings(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	issuedAt := time.Now().UTC()
	first := exchangeGrant(t, ceremony, grantOfScopes("openid", "offline"))
	if first.ExpiresIn <= testAccessLifespan-slack || first.ExpiresIn > testAccessLifespan {
		t.Errorf("первая пара: ответ называет срок %s, ожидался %s", first.ExpiresIn, testAccessLifespan)
	}
	requireWithin(t, "первый токен доступа", introspect(t, ceremony, first.AccessToken, oauthceremony.TokenKindAccess).ExpiresAt,
		issuedAt.Add(testAccessLifespan))
	requireWithin(t, "первый токен обновления", introspect(t, ceremony, first.RefreshToken, oauthceremony.TokenKindRefresh).ExpiresAt,
		issuedAt.Add(24*time.Hour))

	rotatedAt := time.Now().UTC()
	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("оборот отказал: %v", err)
	}
	requireWithin(t, "токен доступа оборота", introspect(t, ceremony, second.AccessToken, oauthceremony.TokenKindAccess).ExpiresAt,
		rotatedAt.Add(testAccessLifespan))
	requireWithin(t, "токен обновления оборота", introspect(t, ceremony, second.RefreshToken, oauthceremony.TokenKindRefresh).ExpiresAt,
		rotatedAt.Add(24*time.Hour))
}

// TestGrantBoundHoldsTheWholeFamily — граница названа службой (две минуты для
// токена доступа и для токена обновления): её держит и первая пара, и КАЖДАЯ
// пара оборота. Оборот, перештамповывающий сроки из настроек, продлевал бы
// семейство за границу на каждом шаге.
func TestGrantBoundHoldsTheWholeFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	bound := time.Now().UTC().Add(2 * time.Minute)
	grant := grantOfScopes("openid", "offline")
	grant.ExpiresAt = map[oauthceremony.TokenKind]time.Time{
		oauthceremony.TokenKindAccess:  bound,
		oauthceremony.TokenKindRefresh: bound,
	}

	first := exchangeGrant(t, ceremony, grant)
	if first.ExpiresIn > 2*time.Minute {
		t.Errorf("первая пара: ответ называет срок %s, граница — две минуты", first.ExpiresIn)
	}
	requireNotAfter(t, "первый токен доступа", introspect(t, ceremony, first.AccessToken, oauthceremony.TokenKindAccess).ExpiresAt, bound)
	requireNotAfter(t, "первый токен обновления", introspect(t, ceremony, first.RefreshToken, oauthceremony.TokenKindRefresh).ExpiresAt, bound)

	tokens := first
	for step := 1; step <= 2; step++ {
		next, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
		if err != nil {
			t.Fatalf("оборот №%d отказал: %v", step, err)
		}
		if next.ExpiresIn > 2*time.Minute {
			t.Errorf("оборот №%d: ответ называет срок %s, граница — две минуты", step, next.ExpiresIn)
		}
		requireNotAfter(t, "токен доступа оборота", introspect(t, ceremony, next.AccessToken, oauthceremony.TokenKindAccess).ExpiresAt, bound)
		requireNotAfter(t, "токен обновления оборота", introspect(t, ceremony, next.RefreshToken, oauthceremony.TokenKindRefresh).ExpiresAt, bound)
		tokens = next
	}
}

// TestGrantBoundHoldsTheAuthorizationCode — граница кода: код живёт не дольше
// названного, даже если настройки дают ему больше (testCodeLifespan).
//
// Запись сроков обязана называть ТОЛЬКО объявленные виды (TokenKind). Вид,
// переведённый в язык движка приведением строки, а не словарём, расходится с
// ним у кода (`authorization_code` против `authorize_code` движка): граница
// ложится под одним ключом, срок движка — под другим, и граница не действует,
// а проба, читающая наш ключ, видит границу и зеленеет.
func TestGrantBoundHoldsTheAuthorizationCode(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	const boundIn = 10 * time.Second
	if boundIn >= testCodeLifespan {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: граница через %s не короче срока кода из настроек %s — проба не отличила бы границу от срока настроек",
			boundIn, testCodeLifespan)
	}
	bound := time.Now().UTC().Add(boundIn)
	grant := grantOfScopes("openid", "offline")
	grant.ExpiresAt = map[oauthceremony.TokenKind]time.Time{oauthceremony.TokenKindAuthorizationCode: bound}
	if _, err := completeWith(t, ceremony, authorizeRequest(), grant); err != nil {
		t.Fatalf("CompleteAuthorization отказал: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.codes) != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: кодов в хранилище %d шт, ожидался 1", len(store.codes))
	}
	for _, row := range store.codes {
		for kind := range row.grant.Session.ExpiresAt {
			if !kind.Declared() {
				t.Errorf("запись сроков называет необъявленный вид %q: %v", kind, row.grant.Session.ExpiresAt)
			}
		}
		requireNotAfter(t, "код авторизации", row.grant.Session.ExpiresAt[oauthceremony.TokenKindAuthorizationCode], bound)
	}
}

// ── Нулевое время границы ──────────────────────────────────────────────────
//
// Движок читает нулевой срок токена обновления как «без срока»
// (ValidateRefreshToken: нулевое время — бессрочно). Граница, равная нулевому
// времени, сжимала бы к нулю каждый назначаемый срок токена обновления — то
// есть делала бы семейство БЕССРОЧНЫМ, хотя служба, назвав границу, просила
// обратного. Нулевое время границы — не граница, и ни один путь церемонии не
// имеет права его принять.

const (
	// zeroBoundLifespan — срок токена обновления из настроек в пробах
	// нулевой границы: короткий, чтобы истечение наблюдалось в пробе.
	zeroBoundLifespan = time.Second
	// zeroBoundWait — сколько проба ждёт перед оборотом. Больше срока с
	// запасом на округление движка до секунды (срок назначается
	// `Round(time.Second)`, то есть до полусекунды позже).
	zeroBoundWait = 2500 * time.Millisecond
)

func shortRefreshLifespan(c *oauthceremony.Config) { c.RefreshTokenLifespan = zeroBoundLifespan }

// requireRefreshExpired — оборот отвергнут истечением токена обновления: на
// проводе `invalid_grant`, в тексте — истечение, а не иной отказ.
func requireRefreshExpired(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("оборот через %s при сроке токена обновления %s принят", zeroBoundWait, zeroBoundLifespan)
	}
	if !errors.Is(err, oauthceremony.ErrInvalidGrant) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("оборот через %s отвергнут не истечением: случай %v, текст %q", zeroBoundWait, oauthceremony.CodeOf(err), err)
	}
}

// TestGSR_ZeroRefreshBound — граница токена обновления, равная нулевому
// времени, отвергается при выдаче по имени поля; законный близнец без границы
// показывает, что при этих же настройках токен обновления истекает.
//
// Предмет — ИСХОД, а не текст: до правки нулевая граница принималась, и
// оборот через 2,5 с при сроке 1 с проходил — семейство было бессрочным. Проба
// в этом случае доводит семейство до оборота, чтобы красное называло вред.
func TestGSR_ZeroRefreshBound(t *testing.T) {
	t.Run("близнец без границы истекает по настройкам", func(t *testing.T) {
		t.Parallel()
		store := newMemoryPorts()
		registerTestClient(t, store)
		ceremony := newTestCeremony(t, store.ports(), shortRefreshLifespan)

		first := exchangeGrant(t, ceremony, grantOfScopes("openid", "offline"))
		time.Sleep(zeroBoundWait)
		_, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
		requireRefreshExpired(t, err)
	})

	t.Run("нулевая граница токена обновления отвергнута", func(t *testing.T) {
		t.Parallel()
		store := newMemoryPorts()
		registerTestClient(t, store)
		ceremony := newTestCeremony(t, store.ports(), shortRefreshLifespan)

		grant := grantOfScopes("openid", "offline")
		grant.ExpiresAt = map[oauthceremony.TokenKind]time.Time{oauthceremony.TokenKindRefresh: {}}
		result, err := completeWith(t, ceremony, authorizeRequest(), grant)
		if err == nil {
			tokens, xerr := ceremony.Exchange(context.Background(), codeExchange(result.Parameters["code"][0]))
			if xerr != nil {
				t.Fatalf("нулевая граница принята при выдаче, обмен кода отказал: %v", xerr)
			}
			time.Sleep(zeroBoundWait)
			_, rerr := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
			if rerr == nil {
				t.Fatalf("нулевая граница принята при выдаче, и оборот через %s при сроке %s прошёл — семейство бессрочно",
					zeroBoundWait, zeroBoundLifespan)
			}
			t.Fatalf("нулевая граница принята при выдаче; оборот через %s отказал: %v", zeroBoundWait, rerr)
		}
		requireZeroBoundRefused(t, store, oauthceremony.TokenKindRefresh, err)
	})
}

// requireZeroBoundRefused — выдача с нулевой границей отвергнута как ошибка
// службы, отказ называет поле и вид, и кода нет в хранилище.
func requireZeroBoundRefused(t *testing.T, store *memoryPorts, kind oauthceremony.TokenKind, err error) {
	t.Helper()
	if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Fatalf("нулевая граница %s отвергнута случаем %v, ожидался %v: %v",
			kind, oauthceremony.CodeOf(err), oauthceremony.CodeCeremonyMisuse, err)
	}
	if text := err.Error(); !strings.Contains(text, "AuthorizationGrant.ExpiresAt") || !strings.Contains(text, string(kind)) {
		t.Errorf("отказ не называет поле и вид: %q", text)
	}
	store.mu.Lock()
	stored := len(store.codes)
	store.mu.Unlock()
	if stored != 0 {
		t.Errorf("при отказе в хранилище положено кодов %d шт", stored)
	}
}

// TestZeroBoundOfEveryKindIsRefusedByName — нулевое время границы не граница
// ни у одного объявленного вида: отказ называет поле и вид. Виды — словарь
// TokenKinds, а не выписанный перечень.
func TestZeroBoundOfEveryKindIsRefusedByName(t *testing.T) {
	for _, kind := range oauthceremony.TokenKinds() {
		t.Run(string(kind), func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			grant := grantOfScopes("openid", "offline")
			grant.ExpiresAt = map[oauthceremony.TokenKind]time.Time{
				oauthceremony.TokenKindAccess: time.Now().UTC().Add(time.Minute),
				kind:                          {},
			}
			_, err := completeWith(t, ceremony, authorizeRequest(), grant)
			if err == nil {
				t.Fatalf("выдача с нулевой границей %s принята", kind)
			}
			requireZeroBoundRefused(t, store, kind, err)
		})
	}
}

// TestStoredZeroInstantIsAContractBreach — запись, отданная хранилищем, несёт
// нулевое время в границе семейства либо в сроке артефакта: это нарушение
// контракта порта, и оборот не состоится. Законный близнец — та же запись без
// правки — оборачивается (отличие — одно значение одного ключа).
//
// До правки такая запись принималась: нулевая граница сжимала срок нового
// токена обновления к нулю, нулевой срок записывался как есть, и оба пути
// давали бессрочный токен обновления.
func TestStoredZeroInstantIsAContractBreach(t *testing.T) {
	cases := map[string]func(*oauthceremony.SessionRecord){
		"близнец без правки": nil,
		"SessionRecord.NotAfter": func(s *oauthceremony.SessionRecord) {
			s.NotAfter = map[oauthceremony.TokenKind]time.Time{oauthceremony.TokenKindRefresh: {}}
		},
		"SessionRecord.ExpiresAt": func(s *oauthceremony.SessionRecord) {
			s.ExpiresAt[oauthceremony.TokenKindRefresh] = time.Time{}
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			first := exchangeGrant(t, ceremony, grantOfScopes("openid", "offline"))
			if corrupt != nil {
				store.mu.Lock()
				if len(store.refresh) != 1 {
					store.mu.Unlock()
					t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: токенов обновления в хранилище %d шт, ожидался 1", len(store.refresh))
				}
				for _, row := range store.refresh {
					corrupt(&row.grant.Session)
				}
				store.mu.Unlock()
			}

			next, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
			if corrupt == nil {
				if err != nil {
					t.Fatalf("близнец: оборот отказал: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("запись с нулевым %s принята: оборот прошёл, срок нового токена обновления %v", name,
					introspect(t, ceremony, next.RefreshToken, oauthceremony.TokenKindRefresh).ExpiresAt)
			}
			var pe *oauthceremony.ProtocolError
			if !errors.Is(err, oauthceremony.ErrPortContract) || !errors.As(err, &pe) {
				t.Fatalf("запись с нулевым %s отвергнута случаем %v, ожидался %v: %v",
					name, oauthceremony.CodeOf(err), oauthceremony.CodePortContract, err)
			}
			if !strings.Contains(pe.Debug, name) || !strings.Contains(pe.Debug, string(oauthceremony.TokenKindRefresh)) {
				t.Errorf("отказ не называет поле и вид: %q", pe.Debug)
			}
		})
	}
}
