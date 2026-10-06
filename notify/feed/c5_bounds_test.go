// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// c5_bounds_test.go — границы C5 до первого оператора SQL: описание класса
// obligation, пустой ключ нити у Put, границы Local.Claim и Local.Ack. Каждый
// отрицательный вариант отличается от близнеца одним фактом; хранилище,
// которого касаться нельзя, считает обращения.
package feed_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed"
)

// Описание класса obligation: срока нет и лимитов нет (NTF-5 Р12, §3 З19).
func TestC5_ObligationDescHasNoTTLAndNoLimits(t *testing.T) {
	require.NoError(t, obligationDesc().Validate(), "близнец: obligation без ttl и лимитов")
	withTTL := obligationDesc()
	withTTL.TTL = time.Hour
	require.ErrorIs(t, withTTL.Validate(), feed.ErrAttrsInvalid)
	withLimit := obligationDesc()
	withLimit.Limits = []feed.Limit{{Scope: feed.ScopeRecipient, WindowSeconds: 86400, Max: 3}}
	require.ErrorIs(t, withLimit.Validate(), feed.ErrAttrsInvalid)
	notice := helloDesc()
	notice.TTL = 0
	require.ErrorIs(t, notice.Validate(), feed.ErrAttrsInvalid, "у notice ttl обязателен")
}

// Пустой ключ нити — сторож Put до SQL: отсутствие ключа пишется nil (CX5-52
// (а)). Транзакции нет — сторож решает раньше неё.
func TestC5_EmptyThreadKeyIsRefusedBeforeSQL(t *testing.T) {
	en := enabled(t, true)
	sig, err := feed.JournalSignal(probeJournal(), "probe", "UPDATED")
	require.NoError(t, err)
	src, err := feed.NewSource(feed.Config{Module: "probe", Service: "probe", Enabled: en, Signal: sig,
		Sealer: randomSealer{}, Metrics: prometheus.NewRegistry()})
	require.NoError(t, err)
	_, err = feed.PutID(src.Bind(context.Background()), nil, helloDesc(), "a@example.invalid", withThread(hello(), ""))
	require.ErrorIs(t, err, feed.ErrAttrsInvalid)
}

// Границы Local до первого обращения к хранилищу. Близнец — Local, собранный
// той же конфигурацией: конструктор проходит.
func TestC5_LocalBoundsAreJudgedBeforeSQL(t *testing.T) {
	db := &untouchedDB{}
	l, err := feed.NewLocal(feed.LocalConfig{Module: "probe", Service: "probe", DB: db,
		Keyring: ring(t, key(1, 0xA1)), Observer: feed.NopObserver, Metrics: prometheus.NewRegistry()})
	require.NoError(t, err)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"max 0":       func() error { _, err := l.Claim(ctx, []feed.Class{feed.ClassNotice}, 0); return err },
		"max сверх":   func() error { _, err := l.Claim(ctx, []feed.Class{feed.ClassNotice}, feed.MaxClaim+1); return err },
		"классов нет": func() error { _, err := l.Claim(ctx, nil, 1); return err },
		"класс вне":   func() error { _, err := l.Claim(ctx, []feed.Class{"bulk"}, 1); return err },
		"id не той формы": func() error {
			return l.Ack(ctx, feed.Ack{ID: "x", LeaseToken: probeToken, Outcome: feed.Outcome{Kind: feed.KindSent}})
		},
		"токен не UUID": func() error {
			return l.Ack(ctx, feed.Ack{ID: probeID, LeaseToken: "t", Outcome: feed.Outcome{Kind: feed.KindSent}})
		},
		"superseded — не исход Ack": func() error {
			return l.Ack(ctx, feed.Ack{ID: probeID, LeaseToken: probeToken, Outcome: feed.Outcome{Kind: feed.KindSuperseded, Reason: feed.ReasonNotCurrent}})
		},
		"DEFER без отсрочки": func() error {
			return l.Ack(ctx, feed.Ack{ID: probeID, LeaseToken: probeToken, Outcome: feed.Outcome{Kind: feed.KindDefer, Reason: feed.ReasonPlatformUnavailable}})
		},
		"отсрочка у SENT": func() error {
			return l.Ack(ctx, feed.Ack{ID: probeID, LeaseToken: probeToken, Outcome: feed.Outcome{Kind: feed.KindSent}, DeferFor: time.Minute})
		},
	} {
		err := call()
		require.Error(t, err, name)
		if errorsIsAny(err, feed.ErrClaimInvalid, feed.ErrAckInvalid) {
			continue
		}
		t.Fatalf("%s: отказ не границы: %v", name, err)
	}
	require.Zero(t, db.calls.Load(), "границы решены до хранилища")
}

const (
	probeID    = "ntf-0123456789abcdefg"
	probeToken = "6f1c2a52-8a51-4f4e-9d39-0e7e7e3c2a10"
)

func errorsIsAny(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
