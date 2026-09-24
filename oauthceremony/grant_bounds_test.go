// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// grant_bounds_test.go — выданное не шире запрошенного.
//
// Решение службы о выдаче (AuthorizationGrant) приходит после согласия, и
// церемония обязана не дать ему выйти за запрос: движок сверяет с записью
// клиента только ЗАПРОШЕННОЕ, а выданное копирует в артефакты без сверки. Без
// сверки здесь первый токен доступа живёт весь свой срок с областью, которой
// клиент не просил и которой нет в его записи.
//
// Каждый отрицательный случай меняет против законного близнеца РОВНО ОДИН
// факт — одну лишнюю область или одного лишнего получателя.
package oauthceremony_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

const (
	testAudience      = "https://api.example.net"
	testOtherAudience = "https://billing.example.net"
)

// completeWith проходит Authorize с запросом req и закрывает намерение выдачей
// grant.
func completeWith(t *testing.T, ceremony *oauthceremony.Ceremony, req oauthceremony.AuthorizationRequest,
	grant oauthceremony.AuthorizationGrant) (oauthceremony.AuthorizationResult, error) {
	t.Helper()

	intent, err := ceremony.Authorize(context.Background(), req)
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}
	return ceremony.CompleteAuthorization(context.Background(), intent, grant)
}

// requireRefusedAsMisuse — выдача отвергнута как ошибка службы, и кода нет ни
// в ответе, ни в хранилище.
func requireRefusedAsMisuse(t *testing.T, store *memoryPorts, result oauthceremony.AuthorizationResult, err error) {
	t.Helper()

	if err == nil {
		t.Fatalf("выдача шире запроса принята: параметры ответа %v", result.Parameters)
	}
	if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Errorf("выдача шире запроса отвергнута случаем %v, ожидался %v",
			oauthceremony.CodeOf(err), oauthceremony.CodeCeremonyMisuse)
	}
	if codes := result.Parameters["code"]; len(codes) != 0 {
		t.Errorf("при отказе в ответе есть код: %v", codes)
	}
	store.mu.Lock()
	stored := len(store.codes)
	store.mu.Unlock()
	if stored != 0 {
		t.Errorf("при отказе в хранилище положено кодов %d шт", stored)
	}
}

func grantOfScopes(scopes ...string) oauthceremony.AuthorizationGrant {
	return loggedIn(oauthceremony.AuthorizationGrant{Subject: testSubject, GrantedScopes: scopes})
}

// TestGrantedScopeBeyondTheRequestIsRefused — запрошено openid и offline,
// выдано ещё и cluster-admin, которого нет ни в запросе, ни в записи клиента.
func TestGrantedScopeBeyondTheRequestIsRefused(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	result, err := completeWith(t, ceremony, authorizeRequest(), grantOfScopes("openid", "offline", "cluster-admin"))
	requireRefusedAsMisuse(t, store, result, err)
}

// TestGrantedScopeAllowedToTheClientButNotRequestedIsRefused — profile есть в
// записи клиента, но не запрошен: согласие давалось на запрошенное.
func TestGrantedScopeAllowedToTheClientButNotRequestedIsRefused(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	result, err := completeWith(t, ceremony, authorizeRequest(), grantOfScopes("openid", "offline", "profile"))
	requireRefusedAsMisuse(t, store, result, err)
}

// TestGrantWithinTheRequestIsIssued — законный близнец: выдано подмножество
// запрошенного (offline не выдан), и именно оно доезжает до токена.
func TestGrantWithinTheRequestIsIssued(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	result, err := completeWith(t, ceremony, authorizeRequest(), grantOfScopes("openid"))
	if err != nil {
		t.Fatalf("выдача подмножества запрошенного отвергнута: %v", err)
	}
	codes := result.Parameters["code"]
	if len(codes) != 1 {
		t.Fatalf("в ответе нет кода: %v", result.Parameters)
	}
	tokens, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant: oauthceremony.GrantAuthorizationCode, ClientID: testClientID, ClientSecret: testSecret,
		AuthMethod: oauthceremony.ClientAuthBasic, Code: codes[0], RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier,
	})
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	if !slices.Equal(tokens.Scopes, []string{"openid"}) {
		t.Errorf("токен несёт области %v, ожидалось [openid]", tokens.Scopes)
	}
}

// TestGrantedScopeIsMatchedByTheCeremonyRule — сверка идёт по правилу
// Config.ScopeMatching: при правиле с образцом запрошенное `tenant.*`
// покрывает выданное `tenant.read` и не покрывает `admin.read`.
func TestGrantedScopeIsMatchedByTheCeremonyRule(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	reg := store.clients[testClientID]
	reg.Scopes = append(reg.Scopes, "tenant.*", "admin.*")
	store.clients[testClientID] = reg
	ceremony := newTestCeremony(t, store.ports(), func(cfg *oauthceremony.Config) {
		cfg.ScopeMatching = oauthceremony.ScopeMatchingWildcard
	})

	req := authorizeRequest()
	req.Scopes = []string{"openid", "tenant.*"}

	if _, err := completeWith(t, ceremony, req, grantOfScopes("openid", "tenant.read")); err != nil {
		t.Fatalf("выдача, покрытая образцом запроса, отвергнута: %v", err)
	}

	other := newMemoryPorts()
	registerTestClient(t, other)
	other.clients[testClientID] = reg
	otherCeremony := newTestCeremony(t, other.ports(), func(cfg *oauthceremony.Config) {
		cfg.ScopeMatching = oauthceremony.ScopeMatchingWildcard
	})
	result, err := completeWith(t, otherCeremony, req, grantOfScopes("openid", "admin.read"))
	requireRefusedAsMisuse(t, other, result, err)
}

// registerAudienceClient — клиенту дозволен один получатель.
func registerAudienceClient(t *testing.T, store *memoryPorts) {
	t.Helper()
	registerTestClient(t, store)
	reg := store.clients[testClientID]
	reg.Audiences = []string{testAudience}
	store.clients[testClientID] = reg
}

// TestGrantedAudienceBeyondTheRequestIsRefused — получатель не запрошен и не
// дозволен клиенту.
func TestGrantedAudienceBeyondTheRequestIsRefused(t *testing.T) {
	store := newMemoryPorts()
	registerAudienceClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	req := authorizeRequest()
	req.Audiences = []string{testAudience}
	grant := grantOfScopes("openid", "offline")
	grant.GrantedAudiences = []string{testAudience, testOtherAudience}

	result, err := completeWith(t, ceremony, req, grant)
	requireRefusedAsMisuse(t, store, result, err)
}

// TestGrantedAudienceWithoutARequestIsRefused — получателей не запрашивали
// вовсе, а выдан один.
func TestGrantedAudienceWithoutARequestIsRefused(t *testing.T) {
	store := newMemoryPorts()
	registerAudienceClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	grant := grantOfScopes("openid", "offline")
	grant.GrantedAudiences = []string{testAudience}

	result, err := completeWith(t, ceremony, authorizeRequest(), grant)
	requireRefusedAsMisuse(t, store, result, err)
}

// TestGrantedAudienceWithinTheRequestIsIssued — законный близнец: выдан
// запрошенный получатель, и он доезжает до интроспекции.
func TestGrantedAudienceWithinTheRequestIsIssued(t *testing.T) {
	store := newMemoryPorts()
	registerAudienceClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	req := authorizeRequest()
	req.Audiences = []string{testAudience}
	grant := grantOfScopes("openid", "offline")
	grant.GrantedAudiences = []string{testAudience}

	result, err := completeWith(t, ceremony, req, grant)
	if err != nil {
		t.Fatalf("выдача запрошенного получателя отвергнута: %v", err)
	}
	tokens, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant: oauthceremony.GrantAuthorizationCode, ClientID: testClientID, ClientSecret: testSecret,
		AuthMethod: oauthceremony.ClientAuthBasic, Code: result.Parameters["code"][0], RedirectURI: testRedirectURI,
		CodeVerifier: testVerifier,
	})
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	if got := introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Audiences; !slices.Equal(got, []string{testAudience}) {
		t.Errorf("токен несёт получателей %v, ожидалось [%s]", got, testAudience)
	}
}
