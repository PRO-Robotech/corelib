// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// introspection_auth_test.go — интроспекция принимает ровно те способы
// доказательства спрашивающего, которые объявляет (IntrospectionAuthMethods),
// а прочие отвергает ПО ИМЕНИ.
//
// Перечень попыток выводится из словаря способов (ClientAuthMethods), а не
// выписан: способ, добавленный в словарь, судится без правки пробы. Каждая
// попытка кончается одним из двух доказуемых исходов — способ исполнен
// (`active: true` о живом токене, порт сверки позван раз) либо отвергнут
// ErrCeremonyMisuse с именем поля и способа, не позвав порта. Третьего —
// способ уехал в движок и получил его отказ, не называющий способа, — нет.
package oauthceremony_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// introspectionAttempt — одна попытка доказательства спрашивающего: способ,
// как его называет запрос, секрет и способ, в который пустое значение
// разрешается (для непустого — он сам).
type introspectionAttempt struct {
	name     string
	method   oauthceremony.ClientAuthMethod
	secret   string
	resolved oauthceremony.ClientAuthMethod
}

// introspectionAttempts — каждый способ словаря с тем секретом, какой ему
// свойствен (у ClientAuthNone секрета нет), и пустое значение обоими
// разрешениями.
func introspectionAttempts() []introspectionAttempt {
	var attempts []introspectionAttempt
	for _, method := range oauthceremony.ClientAuthMethods() {
		secret := testSecret
		if method == oauthceremony.ClientAuthNone {
			secret = ""
		}
		attempts = append(attempts, introspectionAttempt{name: string(method), method: method, secret: secret, resolved: method})
	}
	return append(attempts,
		introspectionAttempt{name: "пусто при секрете", secret: testSecret, resolved: oauthceremony.ClientAuthBasic},
		introspectionAttempt{name: "пусто без секрета", resolved: oauthceremony.ClientAuthNone},
	)
}

// TestIntrospectionRefusesByNameAProofMethodItDoesNotAccept — способ, которого
// точка интроспекции не принимает, отвергается ErrCeremonyMisuse, и отказ
// называет поле IntrospectionRequest.AuthMethod и способ; порт сверки при этом
// не зовётся. Способ, который точка принимает, исполняется: живой токен —
// `active: true`, порт сверки позван раз.
//
// Близнец каждого отказа — тот же запрос способом ClientAuthBasic: против него
// меняется ровно один факт — способ (и секрет, которого у ClientAuthNone нет).
func TestIntrospectionRefusesByNameAProofMethodItDoesNotAccept(t *testing.T) {
	attempts := introspectionAttempts()

	var accepted, refused int
	for _, a := range attempts {
		isAccepted := slices.Contains(oauthceremony.IntrospectionAuthMethods(), a.resolved)
		if isAccepted {
			accepted++
		} else {
			refused++
		}
		t.Run(a.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			token := exchangeCode(t, ceremony).AccessToken
			before := len(store.verificationLog())

			result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
				Token:        token,
				KindHint:     oauthceremony.TokenKindAccess,
				ClientID:     testClientID,
				ClientSecret: a.secret,
				AuthMethod:   a.method,
			})
			calls := len(store.verificationLog()) - before

			if isAccepted {
				if err != nil || !result.Active {
					t.Fatalf("принятый способ %q не исполнен: active=%v, отказ %v", a.resolved, result.Active, err)
				}
				if calls != 1 {
					t.Errorf("принятым способом %q порт сверки позван %d раз, ожидался один", a.resolved, calls)
				}
				return
			}
			if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
				t.Fatalf("способ %q отвергнут случаем %v, ожидался %v: %v",
					a.resolved, oauthceremony.CodeOf(err), oauthceremony.CodeCeremonyMisuse, err)
			}
			for _, want := range []string{"IntrospectionRequest.AuthMethod", strconv.Quote(string(a.resolved))} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("отказ не называет %s: %q", want, err)
				}
			}
			if result.Active {
				t.Error("при отказе ответ называет токен годным")
			}
			if calls != 0 {
				t.Errorf("отвергнутым способом %q порт сверки позван %d раз", a.resolved, calls)
			}
		})
	}
	t.Logf("перепись: способов словаря %d · попыток %d · принято %d · отвергнуто по имени %d",
		len(oauthceremony.ClientAuthMethods()), len(attempts), accepted, refused)
	if len(oauthceremony.ClientAuthMethods()) == 0 || accepted == 0 || refused == 0 || accepted+refused != len(attempts) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: попыток %d, принято %d, отвергнуто %d", len(attempts), accepted, refused)
	}
}

// TestIntrospectionMethodsAreDeclaredMethods — каждый способ, который
// объявляет интроспекция, есть в словаре способов: перечень точки — сужение
// словаря, а не второй словарь.
func TestIntrospectionMethodsAreDeclaredMethods(t *testing.T) {
	declared := oauthceremony.ClientAuthMethods()
	for _, method := range oauthceremony.IntrospectionAuthMethods() {
		if !slices.Contains(declared, method) {
			t.Errorf("интроспекция объявляет способ %q, которого нет в словаре %v", method, declared)
		}
	}
	t.Logf("перепись: способов словаря %d · способов интроспекции %d",
		len(declared), len(oauthceremony.IntrospectionAuthMethods()))
	if len(oauthceremony.IntrospectionAuthMethods()) == 0 {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: интроспекция не объявляет ни одного способа")
	}
}
