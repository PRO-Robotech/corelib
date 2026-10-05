// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// bootposture_notifications_test.go — флаг ленты извещений в самоотчёте
// посадки (kacho#2918, полоса S1-A4): ключ `notifications_enabled` с закрытым
// набором `true` | `false` | `n/a`. Нулевое значение поля — `n/a`: у служб без
// ленты (geo, notify) это их настоящая посадка, а не «не объявлено».
package observability_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/PRO-Robotech/corelib/observability"
)

func notificationsWire(t *testing.T, p observability.BootPosture) (any, bool) {
	t.Helper()
	var buf bytes.Buffer
	observability.LogBootPosture(observability.NewSlogger(&buf), p)
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("строка самоотчёта не JSON-объект: %v (raw=%q)", err, buf.String())
	}
	v, ok := line["notifications_enabled"]
	return v, ok
}

// TestLogBootPosture_NotificationsFlagHasThreeDeclaredStates — три написания
// ключа, каждое строкой: «выключено» отличимо и от «ленты нет», и от
// отсутствия ключа.
func TestLogBootPosture_NotificationsFlagHasThreeDeclaredStates(t *testing.T) {
	cases := []struct {
		name string
		flag observability.NotificationsFlag
		want string
	}{
		{"нулевое значение — ленты нет", observability.NotificationsFlag{}, observability.NotificationsNotApplicable},
		{"флаг включён", observability.NotificationsFlagOf(true), observability.NotificationsOn},
		{"флаг выключен", observability.NotificationsFlagOf(false), observability.NotificationsOff},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := notificationsWire(t, observability.BootPosture{Service: "vpc", Notifications: c.flag})
			if !ok {
				t.Fatalf("ключ notifications_enabled отсутствует в строке самоотчёта")
			}
			if got != c.want {
				t.Fatalf("notifications_enabled = %v (%T), want %q", got, got, c.want)
			}
			if c.flag.String() != c.want {
				t.Fatalf("String() = %q, want %q", c.flag.String(), c.want)
			}
		})
		seen[c.want] = true
	}
	if len(seen) != 3 {
		t.Fatalf("написаний %d, ожидается три различных: %v", len(seen), seen)
	}
	if observability.NotificationsOn != "true" || observability.NotificationsOff != "false" ||
		observability.NotificationsNotApplicable != "n/a" {
		t.Fatalf("написания — хард-контракт гейта посадки: %q %q %q",
			observability.NotificationsOn, observability.NotificationsOff, observability.NotificationsNotApplicable)
	}
}

// TestLogBootPosture_NotificationsFlagZeroPostureIsNotApplicable — запись, не
// знающая о поле (geo, notify), печатает `n/a`, а не пропускает ключ.
func TestLogBootPosture_NotificationsFlagZeroPostureIsNotApplicable(t *testing.T) {
	got, ok := notificationsWire(t, observability.BootPosture{Service: "geo"})
	if !ok || got != "n/a" {
		t.Fatalf("notifications_enabled = %v (присутствует %v), want \"n/a\"", got, ok)
	}
}
