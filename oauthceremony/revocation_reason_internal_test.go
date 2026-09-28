// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// revocation_reason_internal_test.go — отзыв без названной причины до порта не
// доходит.
//
// Файл внутренний намеренно: причину мост берёт из ведомости операции, а
// снаружи операция без причины и с отзывом не строится — каждая операция
// церемонии, в которой движок отзывает, причину называет. Здесь запрет судится
// у единственного места, где причина выбирается, по каждому её источнику.
package oauthceremony

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingGrants — порт отзыва, записывающий каждую полученную причину.
type recordingGrants struct {
	mu      sync.Mutex
	reasons []RevocationReason
}

func (g *recordingGrants) RevokeGrantRefreshTokens(_ context.Context, _ string, reason RevocationReason) (StoreOutcome, error) {
	return g.record(reason)
}

func (g *recordingGrants) RevokeGrantAccessTokens(_ context.Context, _ string, reason RevocationReason) (StoreOutcome, error) {
	return g.record(reason)
}

func (g *recordingGrants) record(reason RevocationReason) (StoreOutcome, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reasons = append(g.reasons, reason)
	return RowsTouched(0), nil
}

func (g *recordingGrants) received() []RevocationReason {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]RevocationReason(nil), g.reasons...)
}

// revokeBoth зовёт оба отзыва моста так, как их зовут движок и церемония.
func revokeBoth(ctx context.Context, bridge *storageBridge) []error {
	return []error{
		bridge.RevokeRefreshToken(ctx, "grant-1"),
		bridge.RevokeAccessToken(ctx, "grant-1"),
	}
}

// TestRevocationWithoutANamedReasonNeverReachesThePort — мост, у операции
// которого нет причины отзыва, порта не зовёт и отказывает случаем
// CodeCeremonyMisuse. Причину нельзя подставить: у словаря нет значения «не
// названа», а любое из трёх было бы ложью службе в её журнал.
//
// Близнецы — по каждому источнику причины: названная операцией (отзыв
// клиентом), замеченный мостом повтор и оба разом — побеждает названная
// операцией, потому что клиент, отзывающий обёрнутым токеном, просит отзыва,
// а не сообщает о повторе.
func TestRevocationWithoutANamedReasonNeverReachesThePort(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() context.Context
		want RevocationReason
	}{
		{name: "no-operation", ctx: context.Background},
		{name: "operation-names-nothing", ctx: func() context.Context {
			ctx, _ := withNotes(context.Background())
			return ctx
		}},
		{name: "client-revoke", want: RevocationClientRevoke, ctx: func() context.Context {
			ctx, notes := withNotes(context.Background())
			notes.requestRevocation(RevocationClientRevoke)
			return ctx
		}},
		{name: "code-replay", want: RevocationCodeReplay, ctx: func() context.Context {
			ctx, notes := withNotes(context.Background())
			notes.markReplayedFamily("grant-1", "client-1", codeReplayed("probe"), RevocationCodeReplay)
			return ctx
		}},
		{name: "refresh-replay", want: RevocationRefreshReplay, ctx: func() context.Context {
			ctx, notes := withNotes(context.Background())
			notes.markReplayedFamily("grant-1", "", refreshReplayed("probe"), RevocationRefreshReplay)
			return ctx
		}},
		{name: "client-revoke-of-a-replayed-token", want: RevocationClientRevoke, ctx: func() context.Context {
			ctx, notes := withNotes(context.Background())
			notes.requestRevocation(RevocationClientRevoke)
			notes.markReplayedFamily("grant-1", "client-1", refreshReplayed("probe"), RevocationRefreshReplay)
			return ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grants := &recordingGrants{}
			bridge := &storageBridge{ports: Ports{Grants: grants}, timeout: time.Second}

			errs := revokeBoth(tc.ctx(), bridge)
			received := grants.received()

			if tc.want == "" {
				if len(received) != 0 {
					t.Errorf("порт отзыва получил причины %q от операции, не назвавшей причины", received)
				}
				for i, err := range errs {
					if !errors.Is(err, ErrCeremonyMisuse) {
						t.Errorf("отзыв №%d без причины кончился %v, ожидался случай %v", i+1, err, CodeCeremonyMisuse)
					}
				}
				return
			}

			for i, err := range errs {
				if err != nil {
					t.Errorf("отзыв №%d с причиной %q отказал: %v", i+1, tc.want, err)
				}
			}
			if len(received) != len(errs) {
				t.Fatalf("порт отзыва вызван %d раз, ожидалось %d", len(received), len(errs))
			}
			for i, reason := range received {
				if reason != tc.want {
					t.Errorf("вызов порта №%d получил причину %q, ожидалась %q", i+1, reason, tc.want)
				}
			}
		})
	}
}
