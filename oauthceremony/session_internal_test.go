// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// session_internal_test.go — сеанс церемонии не пишет нулевого срока.
//
// Файл внутренний намеренно: сеанс не экспортируется, а нулевой срок в нём
// снаружи наблюдаем только следствием — бессрочным токеном обновления, — и то
// лишь на тех путях, которые проба сумела бы построить. Здесь запрет судится у
// единственного места, где срок назначается, по каждому входу.
package oauthceremony

import (
	"testing"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// TestSessionNeverWritesAZeroExpiry — нулевой срок не записывается ни одним
// входом SetExpiresAt: при названной границе нулевой срок («без срока»)
// сжимается к ней, без границы — не пишется и прежний срок остаётся, а нулевая
// граница, оказавшаяся в сеансе в обход проверки, не сжимает срок к нулю.
func TestSessionNeverWritesAZeroExpiry(t *testing.T) {
	bound := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	later := bound.Add(time.Hour)
	earlier := bound.Add(-time.Hour)

	t.Run("нулевой срок при названной границе — граница", func(t *testing.T) {
		s := newSession().(*ceremonySession)
		s.notAfter[engine.RefreshToken] = bound
		s.SetExpiresAt(engine.RefreshToken, time.Time{})
		if got := s.GetExpiresAt(engine.RefreshToken); !got.Equal(bound) {
			t.Fatalf("срок %v, ожидалась граница %v", got, bound)
		}
	})

	t.Run("нулевой срок без границы не пишется", func(t *testing.T) {
		s := newSession().(*ceremonySession)
		s.SetExpiresAt(engine.RefreshToken, time.Time{})
		if at, written := s.ExpiresAt[engine.RefreshToken]; written {
			t.Fatalf("записан срок %v", at)
		}
		s.SetExpiresAt(engine.RefreshToken, earlier)
		s.SetExpiresAt(engine.RefreshToken, time.Time{})
		if got := s.GetExpiresAt(engine.RefreshToken); !got.Equal(earlier) {
			t.Fatalf("нулевой срок затёр прежний: %v, ожидался %v", got, earlier)
		}
	})

	t.Run("нулевая граница не сжимает срок к нулю", func(t *testing.T) {
		s := newSession().(*ceremonySession)
		s.notAfter[engine.RefreshToken] = time.Time{}
		s.SetExpiresAt(engine.RefreshToken, later)
		if got := s.GetExpiresAt(engine.RefreshToken); !got.Equal(later) {
			t.Fatalf("срок %v, ожидался %v", got, later)
		}
	})

	t.Run("близнец: срок позже границы сжимается, раньше — пишется", func(t *testing.T) {
		s := newSession().(*ceremonySession)
		s.notAfter[engine.RefreshToken] = bound
		s.SetExpiresAt(engine.RefreshToken, later)
		if got := s.GetExpiresAt(engine.RefreshToken); !got.Equal(bound) {
			t.Fatalf("срок %v, ожидалась граница %v", got, bound)
		}
		s.SetExpiresAt(engine.RefreshToken, earlier)
		if got := s.GetExpiresAt(engine.RefreshToken); !got.Equal(earlier) {
			t.Fatalf("срок %v, ожидался %v", got, earlier)
		}
	})
}
