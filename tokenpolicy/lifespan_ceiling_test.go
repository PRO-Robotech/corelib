// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// lifespan_ceiling_test.go — потолки сроков артефактов церемонии: не выше
// решения продукта, и потолок семейства токенов обновления не выше потолка
// срока секрета.
package tokenpolicy_test

import (
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

// TestCeremonyLifespanCeilingsStayWithinTheProductDecision — потолки сроков
// артефактов церемонии не выше решённых продуктом.
//
// Величины — решение о политике окна (замысел LINE-A-1, редакция 14): код —
// не выше 60 секунд, семейство токенов обновления — не выше 168 часов от
// первой выдачи, токен доступа — существующий потолок 30 минут. Проба судит
// «не выше», а не равенство: сузить потолок вправе и без неё, а поднять —
// только пересмотром решения, и тогда проба называет обе величины.
func TestCeremonyLifespanCeilingsStayWithinTheProductDecision(t *testing.T) {
	cases := []struct {
		name    string
		ceiling time.Duration
		decided time.Duration
	}{
		{"tokenpolicy.MaxAuthorizationCodeTTL", tokenpolicy.MaxAuthorizationCodeTTL, 60 * time.Second},
		{"tokenpolicy.MaxRefreshTokenFamilyTTL", tokenpolicy.MaxRefreshTokenFamilyTTL, 168 * time.Hour},
		{"tokenpolicy.MaxTokenTTL", tokenpolicy.MaxTokenTTL, 30 * time.Minute},
	}
	for _, tc := range cases {
		if tc.ceiling > tc.decided {
			t.Errorf("%s = %s выше решённого %s: подъём потолка — пересмотр решения, а не правка числа",
				tc.name, tc.ceiling, tc.decided)
		}
	}
}

// TestRefreshTokenFamilyCeilingStaysWithinTheSecretCredentialCeiling —
// семейство токенов обновления не живёт дольше секрета.
//
// Сравниваются две АБСОЛЮТНЫЕ величины: предел семейства от первой выдачи и
// предел срока секрета от его выпуска. Решение у MaxRefreshTokenFamilyTTL
// опирается на это соотношение: предъявительский документ, срок которого
// владелец не выбирал, не вправе жить дольше того, чей срок владелец назвал
// сам. Подъём любого из двух потолков без пересмотра другого роняет пробу и
// называет оба числа — тогда изменение становится решением, а не дрейфом.
func TestRefreshTokenFamilyCeilingStaysWithinTheSecretCredentialCeiling(t *testing.T) {
	if tokenpolicy.MaxRefreshTokenFamilyTTL > tokenpolicy.SecretCredentialTTLCeiling {
		t.Fatalf("потолок срока семейства токенов обновления %s выше потолка срока секрета %s.\n"+
			"Семейство выпускается по ходу входа, и срока его владелец не выбирает;\n"+
			"пересмотрите MaxRefreshTokenFamilyTTL либо SecretCredentialTTLCeiling.",
			tokenpolicy.MaxRefreshTokenFamilyTTL, tokenpolicy.SecretCredentialTTLCeiling)
	}
}
