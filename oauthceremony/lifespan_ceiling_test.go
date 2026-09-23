// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// lifespan_ceiling_test.go — у сроков всех трёх артефактов церемонии — токена
// доступа, кода авторизации и токена обновления — есть потолок фундамента
// (tokenpolicy), и New судит ВЕЛИЧИНУ, а не только факт, что срок назван.
//
// Предмет — отказ сборки: значение выше потолка не собирает церемонию, и текст
// отказа называет и поле, и потолок. Близнец каждого отказа — то же значение,
// РАВНОЕ потолку: оно собирает церемонию. Отказ и близнец различаются ровно
// одним фактом — одной наносекундой в одном поле; остальной набор настроек у
// них общий, взятый у newTestCeremony, поэтому отказ не может прийти от
// соседнего поля.
package oauthceremony_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

func TestNewRefusesAnArtifactLifespanAboveItsCeiling(t *testing.T) {
	cases := []struct {
		name        string
		field       string
		ceilingName string
		ceiling     time.Duration
		set         func(*oauthceremony.Config, time.Duration)
	}{
		{
			name:        "токен доступа",
			field:       "Config.AccessTokenLifespan",
			ceilingName: "tokenpolicy.MaxTokenTTL",
			ceiling:     tokenpolicy.MaxTokenTTL,
			set:         func(c *oauthceremony.Config, d time.Duration) { c.AccessTokenLifespan = d },
		},
		{
			name:        "код авторизации",
			field:       "Config.AuthorizationCodeLifespan",
			ceilingName: "tokenpolicy.MaxAuthorizationCodeTTL",
			ceiling:     tokenpolicy.MaxAuthorizationCodeTTL,
			set:         func(c *oauthceremony.Config, d time.Duration) { c.AuthorizationCodeLifespan = d },
		},
		{
			name:        "токен обновления",
			field:       "Config.RefreshTokenLifespan",
			ceilingName: "tokenpolicy.MaxRefreshTokenFamilyTTL",
			ceiling:     tokenpolicy.MaxRefreshTokenFamilyTTL,
			set:         func(c *oauthceremony.Config, d time.Duration) { c.RefreshTokenLifespan = d },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()

			// Близнец: срок, РАВНЫЙ потолку, собирает церемонию —
			// newTestCeremony сам падает, если New отказал. Заодно снимается
			// полный набор настроек, от которого отказ отличается одним полем.
			var twin oauthceremony.Config
			newTestCeremony(t, store.ports(), func(c *oauthceremony.Config) {
				tc.set(c, tc.ceiling)
				twin = *c
			})

			above := twin
			tc.set(&above, tc.ceiling+time.Nanosecond)
			_, err := oauthceremony.New(above, store.ports())
			if err == nil {
				t.Fatalf("%s: срок %s выше потолка %s принят", tc.field, tc.ceiling+time.Nanosecond, tc.ceiling)
			}
			if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
				t.Fatalf("%s выше потолка отвергнут не как негодная сборка: случай %v, текст %q",
					tc.field, oauthceremony.CodeOf(err), err)
			}
			for _, named := range []string{tc.field, tc.ceilingName, tc.ceiling.String()} {
				if !strings.Contains(err.Error(), named) {
					t.Errorf("отказ не называет %q: %v", named, err)
				}
			}
		})
	}
}
