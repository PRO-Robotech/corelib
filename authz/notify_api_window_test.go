// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_api_window_test.go — окна отзыва процесса notify-api объявлены
// политикой (NTF-5 S1, kacho#2924).
//
// Процесс `services/notify/cmd/notify-api` держит ДВА окна вердиктов: звено
// прав слушателя (KACHO_NOTIFY_AUTHZ_CACHE_TTL) и сужатель затронутых ресурсов
// (KACHO_NOTIFY_LIST_FILTER_CACHE_TTL). Единица переписи гейта дерева kacho
// TestEveryAuthzWindowKnobIsDeclared — ОКНО, поэтому записи две, а не одна на
// процесс. Здесь — что обе есть, ключ в форме «<процесс> <ручка>», величина
// равна общему умолчанию площадок без особой поверхности и не выходит за потолок.
package authz_test

import (
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/authz"
)

func TestNotifyAPIWindowsAreDeclared(t *testing.T) {
	for _, key := range []string{
		"notify KACHO_NOTIFY_AUTHZ_CACHE_TTL",
		"notify KACHO_NOTIFY_LIST_FILTER_CACHE_TTL",
	} {
		got, ok := authz.RevocationPolicy.Windows[key]
		if !ok {
			t.Errorf("окно отзыва notify-api политикой не объявлено: записи %q нет; объявлено %d окон",
				key, len(authz.RevocationPolicy.Windows))
			continue
		}
		if got != 5*time.Second {
			t.Errorf("окно %q = %s, ожидалось 5s — общее умолчание площадок без особой поверхности", key, got)
		}
		if got > authz.RevocationPolicy.Ceiling {
			t.Errorf("окно %q = %s превышает потолок %s", key, got, authz.RevocationPolicy.Ceiling)
		}
	}
}

// TestEveryDeclaredWindowFitsUnderTheCeiling — законный близнец по всей
// переписи: потолок держит каждое объявленное окно, а не только новые.
func TestEveryDeclaredWindowFitsUnderTheCeiling(t *testing.T) {
	if len(authz.RevocationPolicy.Windows) == 0 {
		t.Fatal("перепись окон пуста — сверять нечего, вердикт беспредметен")
	}
	for key, got := range authz.RevocationPolicy.Windows {
		if got <= 0 || got > authz.RevocationPolicy.Ceiling {
			t.Errorf("окно %q = %s вне (0, %s]", key, got, authz.RevocationPolicy.Ceiling)
		}
	}
	t.Logf("осмотрено окон: %d, потолок %s", len(authz.RevocationPolicy.Windows), authz.RevocationPolicy.Ceiling)
}
