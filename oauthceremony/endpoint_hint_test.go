// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// endpoint_hint_test.go — подсказка отказа адреса, который разбор и обратная
// запись меняют.
//
// Правила у частей адреса разные, и подсказка обязана называть, что к чему
// относится. Разбор понижает регистр схемы; хост не-ASCII обратная запись
// экранирует, а клиентам хост публикуется A-меткой (`xn--…`); путь обратная
// запись экранирует, но его регистра не трогает, и путь к регистру
// чувствителен. Подсказка «экранируй и пиши в нижнем регистре» верна для схемы
// и для пути, но хост не-ASCII она посылает писать экранированно, а путь —
// понижать регистр.
package oauthceremony_test

import (
	"errors"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// endpointRespellingText — описание отказа после имени поля, побайтно.
const endpointRespellingText = " is not written the way it is served: parsing and re-serialising it changes it; " +
	"write the scheme in lower case, a non-ASCII host as its A-label (the xn-- form), " +
	"and whitespace and non-ASCII characters of the path percent-encoded, keeping the letter case of the path"

// TestEndpointRespellingRefusalNamesTheRuleOfEachPart — адрес, который
// обратная запись меняет в схеме, в хосте или в пути, отвергается одним и тем
// же текстом, и текст называет правило каждой части.
//
// Близнец каждого отвергнутого написания — то же место адреса, записанное по
// правилу подсказки: схема в нижнем регистре, хост A-меткой, путь
// экранированно; и путь в верхнем регистре — подсказка велит его регистра не
// трогать, и проверка его принимает. Против близнеца меняется ровно одна часть
// адреса.
func TestEndpointRespellingRefusalNamesTheRuleOfEachPart(t *testing.T) {
	store := newMemoryPorts()
	valid := oauthceremony.Config{
		AuthorizationEndpoint:     testAuthorizationEndpoint,
		TokenEndpoint:             testTokenEndpoint,
		AccessTokenLifespan:       testAccessLifespan,
		RefreshTokenLifespan:      24 * time.Hour,
		AuthorizationCodeLifespan: testCodeLifespan,
		ScopeMatching:             oauthceremony.ScopeMatchingExact,
		RefreshTokenIssuance:      oauthceremony.RefreshTokenIssuanceAlways,
		MinParameterEntropy:       8,
		PortTimeout:               time.Second,
		OperationTimeout:          time.Second,
		NewGrantID:                mintTestGrantID,
	}
	fields := map[string]func(*oauthceremony.Config, string){
		"Config.AuthorizationEndpoint": func(c *oauthceremony.Config, v string) { c.AuthorizationEndpoint = v },
		"Config.TokenEndpoint":         func(c *oauthceremony.Config, v string) { c.TokenEndpoint = v },
	}
	pairs := []struct {
		part            string
		respelled, twin string
	}{
		{part: "схема в верхнем регистре", respelled: "HTTPS://iam.example.net/iam/v1/endpoint", twin: "https://iam.example.net/iam/v1/endpoint"},
		{part: "хост не-ASCII", respelled: "https://тест.example/iam/v1/endpoint", twin: "https://xn--e1aybc.example/iam/v1/endpoint"},
		{part: "хост не-ASCII в верхнем регистре", respelled: "https://ТЕСТ.example/iam/v1/endpoint", twin: "https://xn--e1aybc.example/iam/v1/endpoint"},
		{part: "путь с буквой не-ASCII", respelled: "https://iam.example.net/iam/v1/точка", twin: "https://iam.example.net/iam/v1/%D1%82%D0%BE%D1%87%D0%BA%D0%B0"},
		{part: "путь с пробелом", respelled: "https://iam.example.net/iam v1/endpoint", twin: "https://iam.example.net/iam%20v1/endpoint"},
		{part: "путь с буквой не-ASCII и в верхнем регистре", respelled: "https://iam.example.net/IAM/V1/ТОЧКА", twin: "https://iam.example.net/IAM/V1/%D0%A2%D0%9E%D0%A7%D0%9A%D0%90"},
	}

	var refused, twins int
	for field, set := range fields {
		for _, pair := range pairs {
			t.Run(field+"/"+pair.part, func(t *testing.T) {
				cfg := valid
				set(&cfg, pair.respelled)
				_, err := oauthceremony.New(cfg, store.ports())
				var refusal *oauthceremony.ProtocolError
				if !errors.As(err, &refusal) || refusal.Code != oauthceremony.CodeCeremonyMisuse {
					t.Fatalf("адрес %q в %s не отвергнут отказом сборки: %v", pair.respelled, field, err)
				}
				if want := field + endpointRespellingText; refusal.Description != want {
					t.Errorf("описание отказа\n  %q\nожидалось\n  %q", refusal.Description, want)
				}
				if refusal.Hint != "" {
					t.Errorf("у отказа сборки подсказка отдельным полем: %q", refusal.Hint)
				}
			})
			refused++
			t.Run(field+"/близнец: "+pair.part+" по правилу подсказки", func(t *testing.T) {
				cfg := valid
				set(&cfg, pair.twin)
				if _, err := oauthceremony.New(cfg, store.ports()); err != nil {
					t.Fatalf("адрес %q, записанный по правилу подсказки, в %s отвергнут: %v", pair.twin, field, err)
				}
			})
			twins++
		}
	}
	t.Logf("перепись: полей %d · пар %d · отвергнутых %d · близнецов %d", len(fields), len(pairs), refused, twins)
	if len(pairs) == 0 || refused != len(fields)*len(pairs) || twins != refused {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: отвергнутых %d, близнецов %d", refused, twins)
	}
}
