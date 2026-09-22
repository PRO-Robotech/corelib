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
// сроки из настроек (час и сутки), и они ЗАПИСАНЫ у каждого артефакта — у
// первой пары и у пары оборота.
func TestEveryArtifactCarriesItsLifetimeFromTheSettings(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	issuedAt := time.Now().UTC()
	first := exchangeGrant(t, ceremony, grantOfScopes("openid", "offline"))
	if first.ExpiresIn <= time.Hour-slack || first.ExpiresIn > time.Hour {
		t.Errorf("первая пара: ответ называет срок %s, ожидался час", first.ExpiresIn)
	}
	requireWithin(t, "первый токен доступа", introspect(t, ceremony, first.AccessToken, oauthceremony.TokenKindAccess).ExpiresAt,
		issuedAt.Add(time.Hour))
	requireWithin(t, "первый токен обновления", introspect(t, ceremony, first.RefreshToken, oauthceremony.TokenKindRefresh).ExpiresAt,
		issuedAt.Add(24*time.Hour))

	rotatedAt := time.Now().UTC()
	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("оборот отказал: %v", err)
	}
	requireWithin(t, "токен доступа оборота", introspect(t, ceremony, second.AccessToken, oauthceremony.TokenKindAccess).ExpiresAt,
		rotatedAt.Add(time.Hour))
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
// названного, даже если настройки дают ему десять минут.
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

	bound := time.Now().UTC().Add(time.Minute)
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
	declared := map[oauthceremony.TokenKind]bool{
		oauthceremony.TokenKindAccess: true, oauthceremony.TokenKindRefresh: true,
		oauthceremony.TokenKindAuthorizationCode: true, oauthceremony.TokenKindIdentity: true,
	}
	for _, row := range store.codes {
		for kind := range row.grant.Session.ExpiresAt {
			if !declared[kind] {
				t.Errorf("запись сроков называет необъявленный вид %q: %v", kind, row.grant.Session.ExpiresAt)
			}
		}
		requireNotAfter(t, "код авторизации", row.grant.Session.ExpiresAt[oauthceremony.TokenKindAuthorizationCode], bound)
	}
}
