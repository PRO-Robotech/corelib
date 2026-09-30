// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// introspection_auth_internal_test.go — способ доказательства, которого точка
// интроспекции не принимает, отвергается ДО обращения к движку.
//
// Снаружи этого не видно ни по вызовам порта сверки, ни по вызовам
// справочника: движок отказывает такому запросу на разборе заголовка, раньше,
// чем спросит справочник. Единственный наблюдатель — записывающий поставщик
// (requestAddressRecorder), подставленный вместо движка: он считает запросы,
// которые церемония движку подала.
package oauthceremony

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// TestIntrospectionRefusalComesBeforeTheEngine — запрос интроспекции способом
// вне IntrospectionAuthMethods движку не подаётся: перехвачено ноль запросов,
// отказ — ErrCeremonyMisuse. Близнец — способ из перечня: запрос подан движку
// ровно один раз.
func TestIntrospectionRefusalComesBeforeTheEngine(t *testing.T) {
	type attempt struct {
		name     string
		method   ClientAuthMethod
		secret   string
		resolved ClientAuthMethod
	}
	var attempts []attempt
	for _, method := range ClientAuthMethods() {
		secret := "probe-secret"
		if method == ClientAuthNone {
			secret = ""
		}
		attempts = append(attempts, attempt{name: string(method), method: method, secret: secret, resolved: method})
	}
	attempts = append(attempts,
		attempt{name: "пусто при секрете", secret: "probe-secret", resolved: ClientAuthBasic},
		attempt{name: "пусто без секрета", resolved: ClientAuthNone},
	)

	var reached, stopped int
	for _, a := range attempts {
		accepted := slices.Contains(IntrospectionAuthMethods(), a.resolved)
		if accepted {
			reached++
		} else {
			stopped++
		}
		t.Run(a.name, func(t *testing.T) {
			c, err := New(endpointProbeConfig("https://iam.example.net/iam/v1/authorize", "https://iam.example.net/iam/v1/token"), uncalledPorts())
			if err != nil {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: New не собрал церемонию: %v", err)
			}
			recorder := &requestAddressRecorder{OAuth2Provider: c.provider}
			c.provider = recorder

			_, err = c.Introspect(context.Background(), IntrospectionRequest{
				Token:        "probe-token",
				ClientID:     "probe-client",
				ClientSecret: a.secret,
				AuthMethod:   a.method,
			})
			if err == nil {
				t.Fatal("НЕ ВЫПОЛНИЛОСЬ: Introspect прошёл мимо записывающего поставщика")
			}

			want := 0
			if accepted {
				want = 1
			}
			if len(recorder.seen) != want {
				t.Fatalf("способом %q движку подано запросов %d, ожидалось %d: %+v", a.resolved, len(recorder.seen), want, recorder.seen)
			}
			if !accepted && !errors.Is(err, ErrCeremonyMisuse) {
				t.Errorf("способ %q отвергнут случаем %v, ожидался %v: %v", a.resolved, CodeOf(err), CodeCeremonyMisuse, err)
			}
		})
	}
	t.Logf("перепись: попыток %d · подано движку %d · остановлено до движка %d", len(attempts), reached, stopped)
	if reached == 0 || stopped == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: подано %d, остановлено %d", reached, stopped)
	}
}
