// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// lifespan_ceiling_test.go — соотношение, на котором стоит решение о потолке
// срока токена обновления.
package tokenpolicy_test

import (
	"testing"

	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

// TestRefreshTokenCeilingStaysWithinTheSecretCredentialCeiling — токен
// обновления не живёт дольше секрета.
//
// Решение у MaxRefreshTokenTTL опирается на это соотношение: предъявительский
// документ, срок которого владелец не выбирал, не вправе жить дольше того, чей
// срок владелец назвал сам. Подъём любого из двух потолков без пересмотра
// другого роняет пробу и называет оба числа — тогда изменение становится
// решением, а не дрейфом.
func TestRefreshTokenCeilingStaysWithinTheSecretCredentialCeiling(t *testing.T) {
	if tokenpolicy.MaxRefreshTokenTTL > tokenpolicy.SecretCredentialTTLCeiling {
		t.Fatalf("потолок срока токена обновления %s выше потолка срока секрета %s.\n"+
			"Токен обновления выпускается по ходу входа, и срока его владелец не выбирает;\n"+
			"пересмотрите MaxRefreshTokenTTL либо SecretCredentialTTLCeiling.",
			tokenpolicy.MaxRefreshTokenTTL, tokenpolicy.SecretCredentialTTLCeiling)
	}
}
