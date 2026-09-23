// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// endpoints_internal_test.go — адреса точек авторизации и выдачи, которые
// церемония отдаёт движку, — ровно те, что названы в Config.
//
// Судятся оба места, где адрес доезжает до движка:
//
//   - настройки движка: TokenURL (и его геттер) — адрес, с которым движок
//     сверяет `aud` утверждения клиента (RFC 7523 §3), если до сверки
//     доходит; на путях церемонии не доходит (TestClientAssertionIsRefused),
//     и проба судит значение настройки, а не сверку;
//   - запросы, которые церемония синтезирует и подаёт движку: GET точки
//     авторизации (Authorize) и POST точки выдачи (Exchange, Introspect,
//     Revoke).
//
// Запросы перехватываются на границе движка: записывающий поставщик
// подменяет четыре метода разбора запроса, запоминает адрес и отказывает, не
// доходя до хранилища, — проба судит адрес поданного запроса, а не вердикт
// движка о нём. Перепись перехваченного сверяется с числом точек входа:
// перехват, не увидевший запроса, — не вердикт.
//
// Файл внутренний намеренно: ни настройки движка, ни синтезированный запрос
// снаружи пакета не видны.
package oauthceremony

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// recordedRequest — запрос, поданный движку: точка входа движка, метод, адрес
// без строки запроса и сама строка запроса — отдельно от адреса.
type recordedRequest struct {
	entry   string
	method  string
	address string
	query   string
}

// requestAddressRecorder перехватывает разбор запроса движком. Прочие методы
// поставщика — настоящие: на пути, который проба проходит, их не зовут.
type requestAddressRecorder struct {
	engine.OAuth2Provider
	seen []recordedRequest
}

func (r *requestAddressRecorder) record(entry string, req *http.Request) error {
	address := *req.URL
	address.RawQuery = ""
	r.seen = append(r.seen, recordedRequest{
		entry:   entry,
		method:  req.Method,
		address: address.String(),
		query:   req.URL.RawQuery,
	})
	// Отказ протокола, а не поломка: церемония переводит его как обычный
	// отказ движка и не идёт к портам — порты пробы не отвечают.
	return engine.ErrInvalidRequest
}

func (r *requestAddressRecorder) NewAuthorizeRequest(_ context.Context, req *http.Request) (engine.AuthorizeRequester, error) {
	return nil, r.record("NewAuthorizeRequest", req)
}

func (r *requestAddressRecorder) NewAccessRequest(_ context.Context, req *http.Request, _ engine.Session) (engine.AccessRequester, error) {
	return nil, r.record("NewAccessRequest", req)
}

func (r *requestAddressRecorder) NewIntrospectionRequest(_ context.Context, req *http.Request, _ engine.Session) (engine.IntrospectionResponder, error) {
	return nil, r.record("NewIntrospectionRequest", req)
}

func (r *requestAddressRecorder) NewRevocationRequest(_ context.Context, req *http.Request) error {
	return r.record("NewRevocationRequest", req)
}

// TestEndpointsReachTheEngineAsNamedInConfig — TokenURL движка и адреса
// синтезированных запросов равны значениям из Config, какими бы они ни были.
//
// Случаи различаются тем, чего церемония не вправе подставить сама: путём
// (`/oauth2/...` и `/iam/v1/...` — последний служба доступа публикует на
// деле), хостом и портом (точки на разных хостах) и написанием (экранированные
// знаки уезжают движку как названы, а не раскрытыми). Константа, вывод из
// одного поля или из хоста совпали бы самое большее с одним из них.
func TestEndpointsReachTheEngineAsNamedInConfig(t *testing.T) {
	cases := map[string]struct {
		authorize, token string
	}{
		"на корне хоста": {
			authorize: "https://iam.example.net/oauth2/authorize",
			token:     "https://iam.example.net/oauth2/token",
		},
		"под путём службы доступа": {
			authorize: "https://iam.example.net/iam/v1/authorize",
			token:     "https://iam.example.net/iam/v1/token",
		},
		"на разных хостах, с портом": {
			authorize: "https://login.example.org/iam/v1/authorize",
			token:     "https://iam.example.net:8443/iam/v1/token",
		},
		"с экранированными знаками": {
			authorize: "https://iam.example.net/iam%20v1/%D1%82%D0%BE%D1%87%D0%BA%D0%B0",
			token:     "https://iam.example.net/iam%2Fv1/token",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := New(endpointProbeConfig(tc.authorize, tc.token), uncalledPorts())
			if err != nil {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: New не собрал церемонию: %v", err)
			}

			provider, ok := c.provider.(*engine.Fosite)
			if !ok {
				t.Fatalf("ПРЕДПОСЫЛКА: движок церемонии — %T, а не *engine.Fosite", c.provider)
			}
			settings, ok := provider.Config.(*engine.Config)
			if !ok {
				t.Fatalf("ПРЕДПОСЫЛКА: настройки движка — %T, а не *engine.Config", provider.Config)
			}
			if settings.TokenURL != tc.token {
				t.Errorf("TokenURL движка %q, назначено Config.TokenEndpoint %q", settings.TokenURL, tc.token)
			}
			if got := settings.GetTokenURLs(context.Background()); !slices.Equal(got, []string{tc.token}) {
				t.Errorf("GetTokenURLs движка %q, назначено Config.TokenEndpoint %q", got, tc.token)
			}

			recorder := &requestAddressRecorder{OAuth2Provider: c.provider}
			c.provider = recorder
			driveEverySynthesizedRequest(t, c)

			want := []recordedRequest{
				{entry: "NewAuthorizeRequest", method: http.MethodGet, address: tc.authorize},
				{entry: "NewAccessRequest", method: http.MethodPost, address: tc.token},
				{entry: "NewIntrospectionRequest", method: http.MethodPost, address: tc.token},
				{entry: "NewRevocationRequest", method: http.MethodPost, address: tc.token},
			}
			t.Logf("перепись: точек входа движка ожидалось %d · перехвачено запросов %d", len(want), len(recorder.seen))
			if len(recorder.seen) != len(want) {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: перехвачено %d запросов из %d: %+v", len(recorder.seen), len(want), recorder.seen)
			}
			for i, w := range want {
				got := recorder.seen[i]
				if got.entry != w.entry || got.method != w.method || got.address != w.address {
					t.Errorf("запрос %d подан движку как %s %s в %s, назначено %s %s в %s",
						i, got.method, got.address, got.entry, w.method, w.address, w.entry)
				}
			}
			// Параметры запроса авторизации уезжают строкой запроса поверх
			// адреса точки; адрес, который сам нёс бы строку запроса или
			// фрагмент, их бы испортил — поэтому New такой адрес отвергает.
			if got := recorder.seen[0].query; got != "client_id=probe-client" {
				t.Errorf("строка запроса авторизации %q, отправлено client_id=probe-client", got)
			}
		})
	}
}

// driveEverySynthesizedRequest проходит каждую точку входа церемонии, которая
// синтезирует запрос к движку. Исход каждой — отказ записывающего поставщика;
// судится не он, а поданный запрос.
func driveEverySynthesizedRequest(t *testing.T, c *Ceremony) {
	t.Helper()
	ctx := context.Background()

	if _, err := c.Authorize(ctx, AuthorizationRequest{ClientID: "probe-client"}); err == nil {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: Authorize прошёл мимо записывающего поставщика")
	}
	if _, err := c.Exchange(ctx, TokenRequest{
		Grant:      GrantAuthorizationCode,
		ClientID:   "probe-client",
		AuthMethod: ClientAuthNone,
		Code:       "probe-code",
	}); err == nil {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: Exchange прошёл мимо записывающего поставщика")
	}
	if _, err := c.Introspect(ctx, IntrospectionRequest{
		Token:      "probe-token",
		ClientID:   "probe-client",
		AuthMethod: ClientAuthNone,
	}); err == nil {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: Introspect прошёл мимо записывающего поставщика")
	}
	if err := c.Revoke(ctx, RevocationRequest{
		Token:      "probe-token",
		ClientID:   "probe-client",
		AuthMethod: ClientAuthNone,
	}); err == nil {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: Revoke прошёл мимо записывающего поставщика")
	}
}

func endpointProbeConfig(authorize, token string) Config {
	return Config{
		AuthorizationEndpoint:     authorize,
		TokenEndpoint:             token,
		SigningSecret:             []byte("0123456789abcdef0123456789abcdef"),
		AccessTokenLifespan:       time.Hour,
		RefreshTokenLifespan:      24 * time.Hour,
		AuthorizationCodeLifespan: 10 * time.Minute,
		ScopeMatching:             ScopeMatchingExact,
		RefreshTokenIssuance:      RefreshTokenIssuanceOnScope,
		RefreshTokenScopes:        []string{"offline"},
		SecretHashCost:            10,
		MinParameterEntropy:       8,
		PortTimeout:               2 * time.Second,
		OperationTimeout:          5 * time.Second,
	}
}

func uncalledPorts() Ports {
	return Ports{
		Clients:            uncalledClients{},
		AuthorizationCodes: uncalledAuthorizationCodes{},
		AccessTokens:       uncalledAccessTokens{},
		RefreshTokens:      uncalledRefreshTokens{},
		Grants:             uncalledGrants{},
	}
}
