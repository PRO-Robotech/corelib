// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// issuer_grant_test.go — что порт выпуска токена доступа получает в гранте.
//
// Предмет — три утверждения о гранте, пересекающем границу порта выпуска:
//
//   - в нём нет ни одного предъявленного секрета: ни кода, ни `code_verifier`,
//     ни токена обновления, ни секрета клиента, — он очищен так же, как запись
//     гранта в хранилище;
//   - ключ семейства в нём — тот самый идентификатор, по которому семейство
//     потом отзывается: по нему место предъявления токена узнаёт отзыв;
//   - то, что клиент ПРОСИЛ, и то, что дало согласие, лежат в разных полях, и
//     порт отличает одно от другого.
package oauthceremony_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// presentedSecrets — поля запроса токена, которые несут предъявленный секрет.
var presentedSecrets = []string{"code", "code_verifier", "refresh_token", "client_secret"}

// secretsIn называет, какие из предъявленных значений и полей-носителей
// секрета нашлись в форме гранта. Пусто — ни одного.
func secretsIn(form map[string][]string, presented []string) []string {
	var found []string
	for _, name := range presentedSecrets {
		if _, carried := form[name]; carried {
			found = append(found, "поле "+name)
		}
	}
	for name, values := range form {
		for _, value := range values {
			for _, secret := range presented {
				if secret != "" && strings.Contains(value, secret) {
					found = append(found, "значение предъявленного секрета в поле "+name)
				}
			}
		}
	}
	return found
}

// TestIssuerReceivesTheGrantAsItIsStored — порт выпуска получает грант,
// очищенный так же, как запись гранта в хранилище: предъявленные секреты
// границу порта не пересекают.
//
// Близнец каждого случая — запись того же гранта в хранилище, положенная под
// jti этого выпуска: её очистил движок, и детектор на ней молчит. Грант
// порта обязан совпасть с ней формой.
func TestIssuerReceivesTheGrantAsItIsStored(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method oauthceremony.ClientAuthMethod
		rotate bool
	}{
		{name: "обмен кода, секрет клиента в заголовке", method: oauthceremony.ClientAuthBasic},
		{name: "обмен кода, секрет клиента в теле", method: oauthceremony.ClientAuthPost},
		{name: "оборот токена обновления, секрет клиента в заголовке", method: oauthceremony.ClientAuthBasic, rotate: true},
		{name: "оборот токена обновления, секрет клиента в теле", method: oauthceremony.ClientAuthPost, rotate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			code, _ := issueCode(t, ceremony)
			exchange := codeExchange(code)
			exchange.AuthMethod = tc.method
			tokens, err := ceremony.Exchange(context.Background(), exchange)
			if err != nil {
				t.Fatalf("обмен кода отказал: %v", err)
			}
			presented := []string{code, testVerifier, testSecret}

			if tc.rotate {
				rotation := refreshRequest(tokens.RefreshToken)
				rotation.AuthMethod = tc.method
				if _, err := ceremony.Exchange(context.Background(), rotation); err != nil {
					t.Fatalf("оборот отказал: %v", err)
				}
				presented = []string{tokens.RefreshToken, testSecret}
			}

			issuances := store.issuer.issuances()
			if len(issuances) == 0 {
				t.Fatal("НЕ ВЫПОЛНИЛОСЬ: порт выпуска не позван ни разу — грант судить не на чем")
			}
			last := issuances[len(issuances)-1]

			store.mu.Lock()
			stored, found := store.access[last.issued.ID]
			store.mu.Unlock()
			if !found {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: записи гранта под jti %q нет — близнеца нет", last.issued.ID)
			}
			if leaked := secretsIn(stored.Form, presented); len(leaked) != 0 {
				t.Fatalf("ФИКСТУРА: близнец не чист — в записи хранилища %v: %v", leaked, stored.Form)
			}

			if leaked := secretsIn(last.grant.Form, presented); len(leaked) != 0 {
				t.Errorf("порт выпуска получил предъявленные секреты: %v", leaked)
			}
			if !reflect.DeepEqual(last.grant.Form, stored.Form) {
				t.Errorf("форма гранта у порта выпуска %v, а у записи хранилища %v", last.grant.Form, stored.Form)
			}
			if last.grant.GrantID != stored.GrantID || last.grant.ClientID != stored.ClientID {
				t.Errorf("грант порта выпуска (%q, %q) — не тот, что лёг в хранилище (%q, %q)",
					last.grant.GrantID, last.grant.ClientID, stored.GrantID, stored.ClientID)
			}
		})
	}
}

// TestFamilyKeyTheIssuerSeesIsTheOneRevoked — ключ семейства в гранте порта
// выпуска (GrantRecord.GrantID) один у первого выпуска и у выпуска оборота, и
// по нему же порт отзыва снимает семейство — и по просьбе клиента, и на
// повторе кода. Место предъявления токена узнаёт отзыв только по этому ключу:
// разойдись он с ключом отзыва, отозванный токен был бы годен там, где его
// сверяют по ключам издателя.
func TestFamilyKeyTheIssuerSeesIsTheOneRevoked(t *testing.T) {
	t.Run("отзыв клиентом после оборота", func(t *testing.T) {
		store := newMemoryPorts()
		registerTestClient(t, store)
		ceremony := newTestCeremony(t, store.ports())

		tokens := exchangeCode(t, ceremony)
		rotated, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
		if err != nil {
			t.Fatalf("оборот отказал: %v", err)
		}
		family := requireOneFamilyKey(t, store, 2)

		if err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
			Token:        rotated.RefreshToken,
			KindHint:     oauthceremony.TokenKindRefresh,
			ClientID:     testClientID,
			ClientSecret: testSecret,
			AuthMethod:   oauthceremony.ClientAuthBasic,
		}); err != nil {
			t.Fatalf("отзыв отказал: %v", err)
		}
		requireRevokedFor(t, store, family, oauthceremony.RevocationClientRevoke)
	})

	t.Run("повтор кода", func(t *testing.T) {
		store := newMemoryPorts()
		registerTestClient(t, store)
		ceremony := newTestCeremony(t, store.ports())

		code, _ := issueCode(t, ceremony)
		if _, err := ceremony.Exchange(context.Background(), codeExchange(code)); err != nil {
			t.Fatalf("обмен кода отказал: %v", err)
		}
		family := requireOneFamilyKey(t, store, 1)

		_, err := ceremony.Exchange(context.Background(), codeExchange(code))
		requireCodeReplayRefusal(t, err)
		requireRevokedFor(t, store, family, oauthceremony.RevocationCodeReplay)
	})
}

// requireOneFamilyKey утверждает, что у всех n выпусков порта один и тот же
// непустой ключ семейства, и отдаёт его.
func requireOneFamilyKey(t *testing.T, store *memoryPorts, n int) string {
	t.Helper()

	issuances := store.issuer.issuances()
	if len(issuances) != n {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: у порта выпуска %d выпусков, ожидалось %d", len(issuances), n)
	}
	family := issuances[0].grant.GrantID
	if family == "" {
		t.Fatal("порт выпуска получил грант без ключа семейства")
	}
	for i, issuance := range issuances {
		if issuance.grant.GrantID != family {
			t.Errorf("выпуск %d: ключ семейства %q, у первого выпуска %q", i+1, issuance.grant.GrantID, family)
		}
	}
	return family
}

// TestIssuerTellsConsentedScopesFromRequested — в гранте порта выпуска
// области, которые клиент ПРОСИЛ, и области, которые дало согласие, лежат
// порознь: в токен идут только выданные (GrantedScopes). Клиент просит
// `profile`, согласие его не даёт — у порта `profile` есть только среди
// запрошенных. Близнец — `openid` и `offline`: и просили, и дали.
func TestIssuerTellsConsentedScopesFromRequested(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	request := authorizeRequest()
	request.Scopes = []string{"openid", "offline", "profile"}
	intent, err := ceremony.Authorize(context.Background(), request)
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}
	result, err := ceremony.CompleteAuthorization(context.Background(), intent, loggedIn(oauthceremony.AuthorizationGrant{
		Subject:       testSubject,
		GrantedScopes: []string{"openid", "offline"},
	}))
	if err != nil {
		t.Fatalf("CompleteAuthorization отказал: %v", err)
	}
	codes := result.Parameters["code"]
	if len(codes) != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: кода в ответе нет: %v", result.Parameters)
	}
	if _, err := ceremony.Exchange(context.Background(), codeExchange(codes[0])); err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}

	issuances := store.issuer.issuances()
	if len(issuances) != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: выпусков %d, ожидался один", len(issuances))
	}
	grant := issuances[0].grant
	for _, both := range []string{"openid", "offline"} {
		if !slices.Contains(grant.GrantedScopes, both) || !slices.Contains(grant.RequestedScopes, both) {
			t.Errorf("близнец: %q и просили, и дали, а у порта выдано %v, запрошено %v",
				both, grant.GrantedScopes, grant.RequestedScopes)
		}
	}
	if slices.Contains(grant.GrantedScopes, "profile") {
		t.Errorf("не выданная согласием область названа выданной: %v", grant.GrantedScopes)
	}
	if !slices.Contains(grant.RequestedScopes, "profile") {
		t.Errorf("запрошенная клиентом область пропала из запрошенных: %v — порт не отличил бы просьбу от согласия",
			grant.RequestedScopes)
	}
}
