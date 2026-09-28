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
	"net/http"
	"sync"
	"testing"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
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

// Порты, с которыми движок на пути отзыва доходит до отзыва гранта:
// справочник знает клиента, сверка принимает его секрет, порт выпуска опознаёт
// предъявленный токен, хранилище токенов доступа отдаёт его грант. Прочие
// порты путь отзыва по подсказке «токен доступа» не зовёт.
type (
	revocationClients      struct{ ClientDirectory }
	revocationSecrets      struct{ ClientSecretVerifier }
	revocationIssuer       struct{ AccessTokenIssuer }
	revocationAccessTokens struct{ AccessTokenVault }
)

const (
	revocationProbeClient = "probe-client"
	revocationProbeToken  = "probe-token"
	revocationProbeJTI    = "probe-jti"
)

func (revocationClients) LookupClient(_ context.Context, clientID string) (ClientRegistration, error) {
	return ClientRegistration{
		ClientID:      clientID,
		RedirectURIs:  []string{"https://app.example.net/callback"},
		GrantKinds:    []GrantKind{GrantAuthorizationCode, GrantRefreshToken},
		ResponseKinds: []string{"code"},
		Scopes:        []string{"offline"},
	}, nil
}

func (revocationSecrets) VerifyClientSecret(context.Context, string, PresentedSecret) (SecretVerdict, error) {
	return SecretMatched, nil
}

func (revocationIssuer) IdentifyAccessToken(_ context.Context, token string) (string, error) {
	if token != revocationProbeToken {
		return "", ErrGrantNotFound
	}
	return revocationProbeJTI, nil
}

func (revocationAccessTokens) FetchAccessToken(_ context.Context, signature string) (GrantRecord, error) {
	if signature != revocationProbeJTI {
		return GrantRecord{}, ErrGrantNotFound
	}
	now := time.Now().UTC()
	return GrantRecord{
		GrantID:       "grant-1",
		ClientID:      revocationProbeClient,
		IssuedAt:      now,
		GrantedScopes: []string{"offline"},
		Session: SessionRecord{
			Subject:   "user-1",
			SessionID: "session-1",
			ACR:       "1",
			AuthTime:  now,
			ExpiresAt: map[TokenKind]time.Time{TokenKindAccess: now.Add(10 * time.Minute)},
		},
	}, nil
}

// reasonlessRevocation — движок, до которого операция отзыва дошла БЕЗ
// причины: перед движком он стирает из ведомости причину, названную Revoke.
// Снаружи такая операция не строится — каждая операция церемонии, в которой
// движок отзывает, причину называет, — и это единственный способ судить её
// ответ.
type reasonlessRevocation struct{ engine.OAuth2Provider }

func (p reasonlessRevocation) NewRevocationRequest(ctx context.Context, req *http.Request) error {
	notes := notesFrom(ctx)
	notes.mu.Lock()
	notes.requested = ""
	notes.mu.Unlock()
	return p.OAuth2Provider.NewRevocationRequest(ctx, req)
}

// TestEngineRevocationWithoutANamedReasonAnswersCeremonyMisuse — движок
// отзывает грант в операции, ведомость которой причины не называет: мост порта
// не зовёт, и ОТВЕТ ОПЕРАЦИИ — CodeCeremonyMisuse, а не «временно недоступно»,
// которым движок отвечает на всякий отказ отзыва (storeErrorsToRevocationError).
// Дефект провязки, названный временной недоступностью, клиент повторял бы без
// конца.
//
// Положительный близнец — та же операция с причиной, названной Revoke: грант
// отозван, и порт получил её в обоих вызовах.
func TestEngineRevocationWithoutANamedReasonAnswersCeremonyMisuse(t *testing.T) {
	for _, tc := range []struct {
		name       string
		reasonless bool
	}{
		{name: "причина названа"},
		{name: "причины нет", reasonless: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grants := &recordingGrants{}
			ports := uncalledPorts()
			ports.Clients = revocationClients{}
			ports.ClientSecrets = revocationSecrets{}
			ports.AccessTokenIssuer = revocationIssuer{}
			ports.AccessTokens = revocationAccessTokens{}
			ports.Grants = grants
			c, err := New(endpointProbeConfig("https://iam.example.net/iam/v1/authorize", "https://iam.example.net/iam/v1/token"), ports)
			if err != nil {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: New не собрал церемонию: %v", err)
			}
			if tc.reasonless {
				c.provider = reasonlessRevocation{OAuth2Provider: c.provider}
			}

			err = c.Revoke(context.Background(), RevocationRequest{
				Token:        revocationProbeToken,
				KindHint:     TokenKindAccess,
				ClientID:     revocationProbeClient,
				ClientSecret: "probe-secret",
				AuthMethod:   ClientAuthBasic,
			})
			received := grants.received()

			if !tc.reasonless {
				if err != nil {
					t.Fatalf("отзыв с причиной, названной операцией, отказал: %v", err)
				}
				if len(received) != 2 {
					t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: порт отзыва вызван %d раз, ожидалось 2 — движок не дошёл до отзыва гранта", len(received))
				}
				for i, reason := range received {
					if reason != RevocationClientRevoke {
						t.Errorf("вызов порта №%d получил причину %q, ожидалась %q", i+1, reason, RevocationClientRevoke)
					}
				}
				return
			}

			if len(received) != 0 {
				t.Errorf("порт отзыва получил причины %q от операции, не назвавшей причины", received)
			}
			if code := CodeOf(err); code != CodeCeremonyMisuse {
				t.Errorf("операция, в которой движок отзывает без причины, кончилась случаем %v, ожидался %v: %v",
					code, CodeCeremonyMisuse, err)
			}
		})
	}
}
