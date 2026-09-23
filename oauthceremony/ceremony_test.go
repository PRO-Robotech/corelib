// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// ceremony_test.go — пробы ГРАНИЦЫ, а не движка.
//
// Пробы движка внесены целиком вместе с ним и прогоняются отдельно; повторять
// их здесь значило бы завести второе место об одном предмете. Здесь
// проверяется ровно то, что принадлежит нам: перевод наших типов в типы
// движка и обратно, перевод отказов, контракт портов и отсутствие следов
// поставщика в том, что уезжает наружу.
package oauthceremony_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

const (
	// Адреса точек — те, под которыми служба доступа публикует церемонию
	// (`/iam/v1/...`).
	testAuthorizationEndpoint = "https://iam.example.net/iam/v1/authorize"
	testTokenEndpoint         = "https://iam.example.net/iam/v1/token"
	testClientID              = "svc-console"
	testSecret                = "correct-horse-battery-staple"
	testRedirectURI           = "https://console.example.net/oauth2/callback"
	testSubject               = "usr-7f3c9a1e"
	testState                 = "s6BhdRkqt3s6BhdRkqt3s6BhdRkqt3xx"
	testVerifier              = "dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXkQ"
)

// newTestCeremony собирает церемонию с полным набором настроек. Ни одно поле
// не опущено: New отвергает неназванное, и это здесь проверяется заодно.
func newTestCeremony(t *testing.T, ports oauthceremony.Ports, tweaks ...func(*oauthceremony.Config)) *oauthceremony.Ceremony {
	t.Helper()

	cfg := oauthceremony.Config{
		AuthorizationEndpoint:           testAuthorizationEndpoint,
		TokenEndpoint:                   testTokenEndpoint,
		SigningSecret:                   []byte("0123456789abcdef0123456789abcdef"),
		AccessTokenLifespan:             time.Hour,
		RefreshTokenLifespan:            24 * time.Hour,
		AuthorizationCodeLifespan:       10 * time.Minute,
		ScopeMatching:                   oauthceremony.ScopeMatchingExact,
		RefreshTokenIssuance:            oauthceremony.RefreshTokenIssuanceOnScope,
		RefreshTokenScopes:              []string{"offline"},
		RequireProofKey:                 true,
		RequireProofKeyForPublicClients: true,
		SecretHashCost:                  10,
		MinParameterEntropy:             8,
		PortTimeout:                     2 * time.Second,
		OperationTimeout:                5 * time.Second,
	}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}

	ceremony, err := oauthceremony.New(cfg, ports)
	if err != nil {
		t.Fatalf("New не собрал церемонию: %v", err)
	}
	return ceremony
}

func registerTestClient(t *testing.T, store *memoryPorts) {
	t.Helper()

	hash, err := bcrypt.GenerateFromPassword([]byte(testSecret), 10)
	if err != nil {
		t.Fatalf("хеш секрета клиента не собран: %v", err)
	}
	store.clients[testClientID] = oauthceremony.ClientRegistration{
		ClientID:     testClientID,
		HashedSecret: hash,
		RedirectURIs: []string{testRedirectURI},
		GrantKinds: []oauthceremony.GrantKind{
			oauthceremony.GrantAuthorizationCode,
			oauthceremony.GrantRefreshToken,
		},
		ResponseKinds: []string{"code"},
		Scopes:        []string{"openid", "profile", "offline"},
	}
}

func proofKeyChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeRequest() oauthceremony.AuthorizationRequest {
	return oauthceremony.AuthorizationRequest{
		ClientID:      testClientID,
		RedirectURI:   testRedirectURI,
		ResponseKinds: []oauthceremony.ResponseKind{oauthceremony.ResponseKindCode},
		Scopes:        []string{"openid", "offline"},
		State:         testState,
		Additional: map[string][]string{
			"code_challenge":        {proofKeyChallenge(testVerifier)},
			"code_challenge_method": {"S256"},
		},
	}
}

// issueCode проходит точку авторизации до выданного кода.
func issueCode(t *testing.T, ceremony *oauthceremony.Ceremony) (string, oauthceremony.AuthorizationResult) {
	t.Helper()

	intent, err := ceremony.Authorize(context.Background(), authorizeRequest())
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}
	if intent.ClientID() != testClientID {
		t.Fatalf("намерение называет клиента %q, ожидался %q", intent.ClientID(), testClientID)
	}

	result, err := ceremony.CompleteAuthorization(context.Background(), intent, oauthceremony.AuthorizationGrant{
		Subject:       testSubject,
		Username:      "console-operator",
		GrantedScopes: []string{"openid", "offline"},
		Claims:        map[string]any{"tenant": "b1g0000000000000a"},
	})
	if err != nil {
		t.Fatalf("CompleteAuthorization отказал: %v", err)
	}

	codes := result.Parameters["code"]
	if len(codes) != 1 || codes[0] == "" {
		t.Fatalf("в ответе точки авторизации нет кода: параметры %v", result.Parameters)
	}
	return codes[0], result
}

// TestAuthorizationCodeCeremonyRoundTrip — полный проход границы: запрос
// авторизации, согласие, обмен кода, интроспекция, отзыв.
func TestAuthorizationCodeCeremonyRoundTrip(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	code, result := issueCode(t, ceremony)

	if got := result.Parameters["state"]; len(got) != 1 || got[0] != testState {
		t.Errorf("state не вернулся клиенту: %v", result.Parameters["state"])
	}
	if !strings.HasPrefix(result.RedirectURI, testRedirectURI) {
		t.Errorf("перенаправление ведёт не на зарегистрированный адрес: %q", result.RedirectURI)
	}
	if result.Delivery != oauthceremony.DeliveryQuery {
		t.Errorf("доставка ответа %q, ожидалась %q", result.Delivery, oauthceremony.DeliveryQuery)
	}

	tokens, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	})
	if err != nil {
		t.Fatalf("Exchange отказал: %v", err)
	}
	if tokens.AccessToken == "" {
		t.Fatal("обмен не выдал токена доступа")
	}
	if tokens.RefreshToken == "" {
		t.Fatal("обмен не выдал токена обновления при выданной области offline")
	}
	if tokens.TokenType != "bearer" {
		t.Errorf("тип токена %q, ожидался \"bearer\"", tokens.TokenType)
	}
	// Ответ называет ОСТАВШЕЕСЯ время до срока, записанного у токена, в
	// целых секундах: движок округляет срок до секунды, а к мигу ответа
	// проходят миллисекунды — отсюда час либо час без секунды. Точное
	// равенство часу держалось, пока срок у токена НЕ записывался вовсе и
	// ответ брал голую длительность из настроек (lifetime_test.go).
	if tokens.ExpiresIn < time.Hour-time.Second || tokens.ExpiresIn > time.Hour {
		t.Errorf("срок токена доступа %v, ожидался час (не меньше %v)", tokens.ExpiresIn, time.Hour-time.Second)
	}

	introspection, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
		Token:        tokens.AccessToken,
		KindHint:     oauthceremony.TokenKindAccess,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	})
	if err != nil {
		t.Fatalf("Introspect отказал: %v", err)
	}
	if !introspection.Active {
		t.Fatal("только что выданный токен доступа назван негодным")
	}
	if introspection.Subject != testSubject {
		t.Errorf("интроспекция назвала субъекта %q, ожидался %q", introspection.Subject, testSubject)
	}
	if introspection.Claims["tenant"] != "b1g0000000000000a" {
		t.Errorf("утверждения сеанса не пережили обмен: %v", introspection.Claims)
	}

	if err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
		Token:        tokens.AccessToken,
		KindHint:     oauthceremony.TokenKindAccess,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	}); err != nil {
		t.Fatalf("Revoke отказал: %v", err)
	}

	afterRevocation, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
		Token:        tokens.AccessToken,
		KindHint:     oauthceremony.TokenKindAccess,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	})
	if err != nil {
		t.Fatalf("Introspect после отзыва отказал: %v", err)
	}
	if afterRevocation.Active {
		t.Error("отозванный токен доступа назван годным")
	}
}

// TestRedeemingAuthorizationCodeTwiceIsDistinguishableByValue — ГЛАВНАЯ проба
// атомарности на нашей стороне.
//
// Движок читает код снаружи транзакции, а гасит внутри; значит второе
// предъявление обязано быть отвергнуто ПОРТОМ, и отказ обязан быть различим
// ПО ЗНАЧЕНИЮ, а не по тексту подсказки.
func TestRedeemingAuthorizationCodeTwiceIsDistinguishableByValue(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	exchange := oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	}

	first, err := ceremony.Exchange(context.Background(), exchange)
	if err != nil {
		t.Fatalf("первый обмен отказал: %v", err)
	}

	_, err = ceremony.Exchange(context.Background(), exchange)
	if err == nil {
		t.Fatal("второй обмен тем же кодом прошёл — код погашен не был")
	}
	if !errors.Is(err, oauthceremony.ErrAuthorizationCodeConsumed) {
		t.Fatalf("второй обмен отказал случаем %v, ожидался %v",
			oauthceremony.CodeOf(err), oauthceremony.CodeAuthorizationCodeConsumed)
	}

	// Повторное предъявление кода обязано снять всё, что по нему выдано
	// (RFC 6749 §4.1.2, замечание о безопасности).
	after, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
		Token:        first.AccessToken,
		KindHint:     oauthceremony.TokenKindAccess,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	})
	if err != nil {
		t.Fatalf("интроспекция после повторного предъявления отказала: %v", err)
	}
	if after.Active {
		t.Error("артефакты гранта пережили повторное предъявление кода")
	}
}

// TestIssuedArtifactsCarryNoVendorPrefix — предикат того, что имя поставщика
// движка не уезжает наружу В ТЕКСТЕ АРТЕФАКТА.
//
// Движок предлагает стратегию выпуска, штампующую `ory_at_`, `ory_rt_`,
// `ory_ac_` в начало каждого артефакта. Такая приставка — привязка в самом
// видном месте: она попадает в журналы клиента и в его код разбора, и снять
// её потом нельзя, не сломав всех, кто на неё смотрит.
func TestIssuedArtifactsCarryNoVendorPrefix(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	tokens, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	})
	if err != nil {
		t.Fatalf("Exchange отказал: %v", err)
	}

	// Перечень приставок взят из стратегии движка дословно.
	vendorPrefixes := []string{"ory_", "ory_at_", "ory_rt_", "ory_ac_"}
	artifacts := map[string]string{
		"код авторизации":  code,
		"токен доступа":    tokens.AccessToken,
		"токен обновления": tokens.RefreshToken,
	}
	for name, artifact := range artifacts {
		if artifact == "" {
			t.Fatalf("артефакт %q пуст — проверять нечего", name)
		}
		for _, prefix := range vendorPrefixes {
			if strings.HasPrefix(artifact, prefix) {
				t.Errorf("%s начинается с приставки поставщика %q: %q", name, prefix, artifact)
			}
		}
	}
}

// TestClientCredentialsIsNotServed — вид гранта `client_credentials`
// церемония не обслуживает: у машинной полосы службы доступа своя выдача.
//
// Отказ обязан прийти ДО движка и своим случаем «вид не поддерживается», а не
// «клиенту не разрешено» от движка: второе значило бы, что обработчик вида
// провязан и лишь этому клиенту не выдан.
//
// Близнец — обмен кода тем же клиентом в той же церемонии: он проходит
// (TestAuthorizationCodeCeremonyRoundTrip), отличие — вид гранта.
func TestClientCredentialsIsNotServed(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	reg := store.clients[testClientID]
	reg.GrantKinds = append(reg.GrantKinds, oauthceremony.GrantKind("client_credentials"))
	store.clients[testClientID] = reg
	ceremony := newTestCeremony(t, store.ports())

	for _, kind := range oauthceremony.GrantKinds() {
		if kind == "client_credentials" {
			t.Errorf("словарь видов гранта называет %q", kind)
		}
	}

	_, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantKind("client_credentials"),
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	})
	if err == nil {
		t.Fatal("обмен client_credentials прошёл")
	}
	if !errors.Is(err, oauthceremony.ErrUnsupportedGrantType) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodeUnsupportedGrantType)
	}
}

// TestClientAssertionIsRefused — утверждение клиента (RFC 7523 §2.2)
// церемония не обслуживает, и движок отвергает его ДО хранилища.
//
// Утверждение подписано по-настоящему и несёт всё, что движок проверяет
// (iss, sub, aud, jti, exp): отказ обязан прийти из-за того, что способ не
// обслуживается, а не из-за негодной подписи или разбора. Близнец — обмен тем
// же клиентом с секретом (TestAuthorizationCodeCeremonyRoundTrip); отличие —
// способ доказательства.
func TestClientAssertionIsRefused(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())
	code, _ := issueCode(t, ceremony)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: ключ утверждения не создан: %v", err)
	}
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": testClientID,
		"sub": testClientID,
		"aud": testTokenEndpoint,
		"jti": "assertion-probe-1",
		"exp": time.Now().Add(time.Minute).Unix(),
	}).SignedString(key)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: утверждение не подписано: %v", err)
	}

	_, err = ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		AuthMethod:   oauthceremony.ClientAuthNone,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
		Additional: map[string][]string{
			"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
			"client_assertion":      {assertion},
		},
	})
	if err == nil {
		t.Fatal("обмен с утверждением клиента прошёл")
	}
	if errors.Is(err, oauthceremony.ErrServerError) || errors.Is(err, oauthceremony.ErrUnknown) {
		t.Fatalf("утверждение клиента отвергнуто поломкой, а не отказом протокола: %v", err)
	}
	if !errors.Is(err, oauthceremony.ErrInvalidRequest) {
		t.Fatalf("случай %v, ожидался %v (способ не обслуживается)", oauthceremony.CodeOf(err), oauthceremony.CodeInvalidRequest)
	}
	t.Logf("отказ: %v", err)
}

// TestRequestObjectIsRefused — объект запроса OpenID Connect (OIDC Core §6)
// церемония не обслуживает: у записи клиента нет ни набора ключей, ни адресов
// объектов запроса, и движок отвечает «не поддерживается», а не разбирает
// объект чужим ключом и не ходит за ним по адресу.
//
// Близнец — тот же запрос без объекта (issueCode): он проходит; отличие —
// одно поле.
func TestRequestObjectIsRefused(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	for name, field := range map[string]string{"по значению": "request", "по адресу": "request_uri"} {
		t.Run(name, func(t *testing.T) {
			request := authorizeRequest()
			request.Additional[field] = []string{"https://console.example.net/request-object.jwt"}
			if field == "request" {
				request.Additional[field] = []string{"eyJhbGciOiJSUzI1NiJ9.eyJzY29wZSI6Im9wZW5pZCJ9.c2ln"}
			}
			_, err := ceremony.Authorize(context.Background(), request)
			if err == nil {
				t.Fatalf("запрос с объектом запроса (%s) прошёл", field)
			}
			want := oauthceremony.ErrRequestNotSupported
			if field == "request_uri" {
				want = oauthceremony.ErrRequestURINotSupported
			}
			if !errors.Is(err, want) {
				t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodeOf(want))
			}
		})
	}
}

// TestDenialTravelsBackAsRedirect — отказ в согласии уезжает клиенту
// перенаправлением с полем `error`, а не телом ответа (RFC 6749 §4.1.2.1).
func TestDenialTravelsBackAsRedirect(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	intent, err := ceremony.Authorize(context.Background(), authorizeRequest())
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}

	result, err := ceremony.DenyAuthorization(context.Background(), intent, oauthceremony.ErrAccessDenied)
	if err != nil {
		t.Fatalf("DenyAuthorization отказал: %v", err)
	}
	if got := result.Parameters["error"]; len(got) != 1 || got[0] != "access_denied" {
		t.Errorf("в отказе нет поля error=access_denied: %v", result.Parameters)
	}
	if got := result.Parameters["state"]; len(got) != 1 || got[0] != testState {
		t.Errorf("state не вернулся вместе с отказом: %v", result.Parameters)
	}
	if len(result.Parameters["code"]) != 0 {
		t.Error("отказ в согласии вернул код авторизации")
	}
}

// TestIntentFromAnotherCeremonyIsRejected — намерение годно ровно одной
// церемонии. Иначе настройки одной («срок кода 10 минут») молча применялись бы
// в другой.
func TestIntentFromAnotherCeremonyIsRejected(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	first := newTestCeremony(t, store.ports())
	second := newTestCeremony(t, store.ports())

	intent, err := first.Authorize(context.Background(), authorizeRequest())
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}

	_, err = second.CompleteAuthorization(context.Background(), intent, oauthceremony.AuthorizationGrant{
		Subject:       testSubject,
		GrantedScopes: []string{"openid"},
	})
	if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Fatalf("чужое намерение принято: %v", err)
	}

	var zero oauthceremony.AuthorizationIntent
	if _, err := first.CompleteAuthorization(context.Background(), zero, oauthceremony.AuthorizationGrant{
		Subject: testSubject,
	}); !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Fatalf("нулевое намерение принято: %v", err)
	}
}

// TestAdditionalCannotRestateANamedParameter — у значения ровно одно место.
func TestAdditionalCannotRestateANamedParameter(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	request := authorizeRequest()
	request.Additional["state"] = []string{"another-state-value-32-characters"}

	if _, err := ceremony.Authorize(context.Background(), request); !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Fatalf("дубль именованного поля в Additional принят: %v", err)
	}

	exchange := oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         "irrelevant",
		RedirectURI:  testRedirectURI,
		Additional:   map[string][]string{"client_secret": {"smuggled"}},
	}
	if _, err := ceremony.Exchange(context.Background(), exchange); !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Fatalf("секрет клиента окольным путём принят: %v", err)
	}
}

// TestUnknownClientIsAnInvalidClientNotAServerError — отсутствие клиента
// переводится в отказ протокола, а не в «внутреннюю ошибку».
func TestUnknownClientIsAnInvalidClientNotAServerError(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	request := authorizeRequest()
	request.ClientID = "svc-that-was-never-registered"

	_, err := ceremony.Authorize(context.Background(), request)
	if err == nil {
		t.Fatal("запрос от незарегистрированного клиента прошёл")
	}
	if errors.Is(err, oauthceremony.ErrServerError) {
		t.Fatalf("отсутствие клиента названо внутренней ошибкой: %v", err)
	}
	if !errors.Is(err, oauthceremony.ErrInvalidClient) {
		t.Fatalf("отказ по случаю %v, ожидался %v",
			oauthceremony.CodeOf(err), oauthceremony.CodeInvalidClient)
	}
}

// TestNewRejectsEveryUnnamedSetting — негодная сборка отвергается ДО первого
// запроса, и отвергается поимённо.
//
// Годная сборка — ровно те поля, которые церемония читает на своих путях, и
// ничего сверх них: поле, которого New требует, но не читает ни один путь
// церемонии, служба была бы обязана назвать впустую. Годный набор ниже
// поэтому — и положительный близнец каждого отказа, и предикат того, что New
// не требует лишнего.
func TestNewRejectsEveryUnnamedSetting(t *testing.T) {
	store := newMemoryPorts()
	valid := oauthceremony.Config{
		AuthorizationEndpoint:     testAuthorizationEndpoint,
		TokenEndpoint:             testTokenEndpoint,
		SigningSecret:             []byte("0123456789abcdef0123456789abcdef"),
		AccessTokenLifespan:       time.Hour,
		RefreshTokenLifespan:      24 * time.Hour,
		AuthorizationCodeLifespan: 10 * time.Minute,
		ScopeMatching:             oauthceremony.ScopeMatchingExact,
		RefreshTokenIssuance:      oauthceremony.RefreshTokenIssuanceAlways,
		SecretHashCost:            10,
		MinParameterEntropy:       8,
		PortTimeout:               time.Second,
		OperationTimeout:          time.Second,
	}
	if _, err := oauthceremony.New(valid, store.ports()); err != nil {
		t.Fatalf("годная сборка отвергнута: %v", err)
	}

	cases := map[string]func(*oauthceremony.Config){
		"SigningSecret короток":         func(c *oauthceremony.Config) { c.SigningSecret = []byte("short") },
		"AccessTokenLifespan не назван": func(c *oauthceremony.Config) { c.AccessTokenLifespan = 0 },
		"RefreshTokenLifespan не назван": func(c *oauthceremony.Config) {
			c.RefreshTokenLifespan = 0
		},
		"AuthorizationCodeLifespan не назван": func(c *oauthceremony.Config) {
			c.AuthorizationCodeLifespan = 0
		},
		"ScopeMatching не назван": func(c *oauthceremony.Config) {
			c.ScopeMatching = oauthceremony.ScopeMatchingUnspecified
		},
		"RefreshTokenIssuance не назван": func(c *oauthceremony.Config) {
			c.RefreshTokenIssuance = oauthceremony.RefreshTokenIssuanceUnspecified
		},
		"RefreshTokenScopes при Always": func(c *oauthceremony.Config) {
			c.RefreshTokenScopes = []string{"offline"}
		},
		"RefreshTokenScopes пуст при OnScope": func(c *oauthceremony.Config) {
			c.RefreshTokenIssuance = oauthceremony.RefreshTokenIssuanceOnScope
		},
		"SecretHashCost ниже предела": func(c *oauthceremony.Config) { c.SecretHashCost = 4 },
		"SecretHashCost выше предела": func(c *oauthceremony.Config) { c.SecretHashCost = 31 },
		"MinParameterEntropy занижен": func(c *oauthceremony.Config) { c.MinParameterEntropy = 4 },
		"PortTimeout не назван":       func(c *oauthceremony.Config) { c.PortTimeout = 0 },
		"OperationTimeout не назван":  func(c *oauthceremony.Config) { c.OperationTimeout = 0 },
		"OperationTimeout короче порта": func(c *oauthceremony.Config) {
			c.PortTimeout = 2 * time.Second
			c.OperationTimeout = time.Second
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			spoil(&cfg)
			if _, err := oauthceremony.New(cfg, store.ports()); !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
				t.Fatalf("негодная сборка принята: %v", err)
			}
		})
	}
}

// TestNewRejectsAnEndpointThatIsNotAnAbsoluteAddress — адреса точек
// авторизации и выдачи названы явно, и New отвергает каждое из них, если оно
// не абсолютный адрес с хостом и без запроса, фрагмента и сведений
// пользователя либо если движок получил бы его в другом написании, — поимённо,
// называя ровно то поле, которое испорчено.
//
// Положительные близнецы — те же настройки с годным адресом, в том числе с
// портом, на другом хосте и с экранированными знаками: проба не имеет права
// отвергать законное. Каждый отрицательный случай меняет против годного набора
// ровно один факт — значение одного поля, а против своего близнеца — одну
// черту написания.
func TestNewRejectsAnEndpointThatIsNotAnAbsoluteAddress(t *testing.T) {
	store := newMemoryPorts()
	valid := oauthceremony.Config{
		AuthorizationEndpoint:     testAuthorizationEndpoint,
		TokenEndpoint:             testTokenEndpoint,
		SigningSecret:             []byte("0123456789abcdef0123456789abcdef"),
		AccessTokenLifespan:       time.Hour,
		RefreshTokenLifespan:      24 * time.Hour,
		AuthorizationCodeLifespan: 10 * time.Minute,
		ScopeMatching:             oauthceremony.ScopeMatchingExact,
		RefreshTokenIssuance:      oauthceremony.RefreshTokenIssuanceAlways,
		SecretHashCost:            10,
		MinParameterEntropy:       8,
		PortTimeout:               time.Second,
		OperationTimeout:          time.Second,
	}

	fields := map[string]func(*oauthceremony.Config, string){
		"Config.AuthorizationEndpoint": func(c *oauthceremony.Config, v string) { c.AuthorizationEndpoint = v },
		"Config.TokenEndpoint":         func(c *oauthceremony.Config, v string) { c.TokenEndpoint = v },
	}
	lawful := map[string]string{
		"без порта":                        "https://iam.example.net/iam/v1/endpoint",
		"с портом":                         "https://iam.example.net:8443/iam/v1/endpoint",
		"с пустым портом":                  "https://iam.example.net:/iam/v1/endpoint",
		"на другом хосте":                  "https://login.example.org/iam/v1/endpoint",
		"с экранированным пробелом":        "https://iam.example.net/iam%20v1/endpoint",
		"с экранированной буквой не-ASCII": "https://iam.example.net/iam/v1/%D1%82%D0%BE%D1%87%D0%BA%D0%B0",
	}
	// «Порт без хоста» и «пустой порт без хоста» — близнецы «с портом» и «с
	// пустым портом»: против них снято ровно имя хоста. Разбор кладёт порт в
	// Host (`:8443`, `:`), и непустой Host у таких адресов хоста не означает.
	spoiled := map[string]string{
		"пустое":                     "",
		"из пробелов":                "   ",
		"относительное":              "/iam/v1/endpoint",
		"без схемы":                  "iam.example.net/iam/v1/endpoint",
		"без хоста":                  "https:///iam/v1/endpoint",
		"порт без хоста":             "https://:8443/iam/v1/endpoint",
		"пустой порт без хоста":      "https://:/iam/v1/endpoint",
		"непрозрачное без хоста":     "https:iam.example.net/iam/v1/endpoint",
		"со сведениями пользователя": "https://operator:secret@iam.example.net/iam/v1/endpoint",
		"с запросом":                 "https://iam.example.net/iam/v1/endpoint?tenant=a",
		"с пустым запросом":          "https://iam.example.net/iam/v1/endpoint?",
		"с фрагментом":               "https://iam.example.net/iam/v1/endpoint#part",
		"с пустым фрагментом":        "https://iam.example.net/iam/v1/endpoint#",
		"неразбираемое":              "https://iam.example.net:port/iam/v1/endpoint",

		// «Другое написание» — близнецы «без порта» и экранированных: адрес
		// разбирается, но запрос, который церемония подаёт движку, нёс бы его
		// не так, как он назван, — пробел и буква не-ASCII уехали бы
		// экранированными, схема — в нижнем регистре. Адрес, который служба
		// публикует, и адрес запроса, поданного движку, разошлись бы.
		"с пробелом в конце":             "https://iam.example.net/iam/v1/endpoint ",
		"с неразрывным пробелом в конце": "https://iam.example.net/iam/v1/endpoint\u00a0",
		"с пробелом внутри":              "https://iam.example.net/iam v1/endpoint",
		"с буквой не-ASCII":              "https://iam.example.net/iam/v1/точка",
		"со схемой в верхнем регистре":   "HTTPS://iam.example.net/iam/v1/endpoint",
	}

	var judged int
	for field, set := range fields {
		for name, value := range lawful {
			t.Run(field+"/годное "+name, func(t *testing.T) {
				cfg := valid
				set(&cfg, value)
				if _, err := oauthceremony.New(cfg, store.ports()); err != nil {
					t.Fatalf("годный адрес %q в %s отвергнут: %v", value, field, err)
				}
			})
			judged++
		}
		for name, value := range spoiled {
			t.Run(field+"/"+name, func(t *testing.T) {
				cfg := valid
				set(&cfg, value)
				_, err := oauthceremony.New(cfg, store.ports())
				if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
					t.Fatalf("негодный адрес %q в %s принят: %v", value, field, err)
				}
				if !strings.Contains(err.Error(), field+" ") {
					t.Fatalf("отказ не называет испорченное поле %s: %v", field, err)
				}
				for other := range fields {
					if other != field && strings.Contains(err.Error(), other) {
						t.Fatalf("отказ называет не то поле: испорчено %s, названо %s: %v", field, other, err)
					}
				}
				t.Logf("отказ: %v", err)
			})
			judged++
		}
	}
	t.Logf("перепись: полей %d · годных форм %d · негодных форм %d · случаев %d",
		len(fields), len(lawful), len(spoiled), judged)
	if judged != len(fields)*(len(lawful)+len(spoiled)) || judged == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: осмотрено случаев %d", judged)
	}
}

// TestNewRejectsEveryMissingPort — порт, которого нет, называется поимённо.
func TestNewRejectsEveryMissingPort(t *testing.T) {
	store := newMemoryPorts()
	cfg := oauthceremony.Config{
		AuthorizationEndpoint:     testAuthorizationEndpoint,
		TokenEndpoint:             testTokenEndpoint,
		SigningSecret:             []byte("0123456789abcdef0123456789abcdef"),
		AccessTokenLifespan:       time.Hour,
		RefreshTokenLifespan:      24 * time.Hour,
		AuthorizationCodeLifespan: 10 * time.Minute,
		ScopeMatching:             oauthceremony.ScopeMatchingExact,
		RefreshTokenIssuance:      oauthceremony.RefreshTokenIssuanceAlways,
		SecretHashCost:            10,
		MinParameterEntropy:       8,
		PortTimeout:               time.Second,
		OperationTimeout:          time.Second,
	}

	cases := map[string]func(*oauthceremony.Ports){
		"Clients":            func(p *oauthceremony.Ports) { p.Clients = nil },
		"AuthorizationCodes": func(p *oauthceremony.Ports) { p.AuthorizationCodes = nil },
		"AccessTokens":       func(p *oauthceremony.Ports) { p.AccessTokens = nil },
		"RefreshTokens":      func(p *oauthceremony.Ports) { p.RefreshTokens = nil },
		"Grants":             func(p *oauthceremony.Ports) { p.Grants = nil },
		"ProofKeys":          func(p *oauthceremony.Ports) { p.ProofKeys = nil },
	}
	for name, drop := range cases {
		t.Run(name, func(t *testing.T) {
			ports := store.ports()
			drop(&ports)
			if _, err := oauthceremony.New(cfg, ports); !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
				t.Fatalf("набор без порта %s принят: %v", name, err)
			}
		})
	}
}

// TestIntrospectingAnInventedTokenIsNotAnError — RFC 7662 §2.2: на выдуманный
// артефакт отвечают `active: false`, а не отказом.
//
// Отвечай церемония отказом, поверхность отвечала бы разными кодами HTTP на
// годный и негодный артефакт — то есть стала бы прибором для перебора.
func TestIntrospectingAnInventedTokenIsNotAnError(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	for name, token := range map[string]string{
		"выдуманный":           "this-token-was-never-issued",
		"похожий на настоящий": "MTIzNDU2Nzg5MA.MTIzNDU2Nzg5MDEyMzQ1Njc4OTA",
		"с приставкой чужака":  "ory_at_MTIzNDU2Nzg5MA.MTIzNDU2Nzg5MDEyMzQ1Njc4OTA",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
				Token:        token,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			if err != nil {
				t.Fatalf("интроспекция негодного артефакта отказала: %v", err)
			}
			if result.Active {
				t.Error("выдуманный артефакт назван годным")
			}
			if result.Subject != "" || result.ClientID != "" || len(result.Scopes) != 0 {
				t.Errorf("ответ о негодном артефакте несёт подробности: %+v", result)
			}
		})
	}
}

// TestRevokingAnInventedTokenSucceeds — RFC 7009 §2.2: отзыв
// несуществующего артефакта успешен.
func TestRevokingAnInventedTokenSucceeds(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	if err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
		Token:        "this-token-was-never-issued",
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	}); err != nil {
		t.Fatalf("отзыв несуществующего артефакта отказал: %v", err)
	}
}

// TestClientSecretNeverLeavesTheCeremonyInAFailure — секрет клиента не
// оседает в отказе.
//
// Отказ уезжает в журнал службы целиком; попади туда секрет, оборот секретов
// перестал бы быть оборотом.
func TestClientSecretNeverLeavesTheCeremonyInAFailure(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	_, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         "a-code-that-was-never-issued",
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	})
	if err == nil {
		t.Fatal("обмен выдуманного кода прошёл")
	}

	rendered := err.Error()
	var protocolErr *oauthceremony.ProtocolError
	if errors.As(err, &protocolErr) {
		rendered += "\x00" + protocolErr.Description + "\x00" + protocolErr.Hint + "\x00" + protocolErr.Debug
	}
	if strings.Contains(rendered, testSecret) {
		t.Errorf("секрет клиента оказался в отказе: %s", rendered)
	}
}
