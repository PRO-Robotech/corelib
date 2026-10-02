// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_probe_window_test.go — окно отзыва пробного процесса notify объявлено
// политикой (Д79).
//
// Процесс `services/notify/cmd/notify-probe` (D4, kacho#2915) отдаёт окно кеша
// вердиктов дескриптору носителя под ручкой KACHO_NOTIFYPROBE_AUTHZ_CACHE_TTL
// с умолчанием 5s. Держатель окна без записи политики — находка гейтов дерева
// kacho (TestEveryVerdictCacheServiceIsDeclared,
// TestEveryVerdictCacheProcessDeclaresItsOwnKnob); здесь — что запись есть, её
// ключ в форме «<процесс> <ручка>» и величина не выходит за потолок.
package authz_test

import (
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/authz"
)

func TestNotifyProbeWindowIsDeclared(t *testing.T) {
	const key = "notify KACHO_NOTIFYPROBE_AUTHZ_CACHE_TTL"
	got, ok := authz.RevocationPolicy.Windows[key]
	if !ok {
		t.Fatalf("окно отзыва пробного процесса notify политикой не объявлено: записи %q нет; "+
			"объявлено %d окон", key, len(authz.RevocationPolicy.Windows))
	}
	if got != 5*time.Second {
		t.Fatalf("окно %q = %s, ожидалось 5s — умолчание ручки в исходнике процесса", key, got)
	}
	if got > authz.RevocationPolicy.Ceiling {
		t.Fatalf("окно %q = %s превышает потолок %s", key, got, authz.RevocationPolicy.Ceiling)
	}
}
