// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// issued_internal_test.go — срок в ответе обмена берётся у выпуска ЭТОГО
// ответа, и ответ без своего выпуска не собирается.
//
// Путь движка с успехом, но без выпуска через порт, наружу недостижим: обмен
// выпускает токен доступа только стратегией церемонии. Поэтому отказы здесь
// судятся на самой функции, а не через обмен: без этой пробы ветвь отказа
// жила бы без держателя.
package oauthceremony

import (
	"context"
	"testing"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

func TestResponseLifetimeComesFromItsOwnIssuance(t *testing.T) {
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	issued := IssuedAccessToken{Token: "at.1", ID: "jti-1", IssuedAt: at, ExpiresAt: at.Add(7*time.Minute + 13*time.Second)}
	engineAnswer := TokenResult{AccessToken: "at.1", ExpiresIn: 20 * time.Minute}

	got, err := withIssuedLifetime(engineAnswer, issued, true)
	if err != nil {
		t.Fatalf("ответ со своим выпуском отвергнут: %v", err)
	}
	if got.ExpiresIn != 7*time.Minute+13*time.Second {
		t.Errorf("expires_in %s, а срок выпуска 7m13s — срок взят не у выпуска", got.ExpiresIn)
	}

	for name, tc := range map[string]struct {
		result TokenResult
		issued IssuedAccessToken
		noted  bool
	}{
		"выпуска в операции нет":    {result: engineAnswer, noted: false},
		"в ответе чужой токен":      {result: TokenResult{AccessToken: "at.2", ExpiresIn: 20 * time.Minute}, issued: issued, noted: true},
		"в ответе токена нет вовсе": {result: TokenResult{ExpiresIn: 20 * time.Minute}, issued: issued, noted: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := withIssuedLifetime(tc.result, tc.issued, tc.noted)
			if CodeOf(err) != CodeServerError {
				t.Fatalf("ответ без своего выпуска собран: %+v, отказ %v", got, err)
			}
		})
	}
}

// silentIssuer — порт выпуска, который считает обращения и не выпускает
// ничего: проба ниже утверждает, что до него не дошли.
type silentIssuer struct{ calls int }

func (s *silentIssuer) IssueAccessToken(context.Context, GrantRecord) (IssuedAccessToken, error) {
	s.calls++
	return IssuedAccessToken{}, nil
}

func (s *silentIssuer) IdentifyAccessToken(context.Context, string) (string, error) {
	s.calls++
	return "", ErrGrantNotFound
}

// TestAccessTokenIsNotIssuedWithoutTheCeremonysBound — движок попросил токен
// доступа, не назначив ему срока в сеансе: границы, которую сверить с
// выпуском, нет, и порт не зовётся вовсе. Оба пути выпуска, которые церемония
// обслуживает, срок назначают до выпуска (flow_authorize_code_token.go,
// flow_refresh.go), поэтому снаружи ветвь недостижима и судится здесь.
// Близнец — любой обмен церемонии: срок назначен, порт позван
// (TestAccessTokenInTheResponseIsTheIssuersAndItsLifetime).
func TestAccessTokenIsNotIssuedWithoutTheCeremonysBound(t *testing.T) {
	issuer := &silentIssuer{}
	bridge, _ := newStorageBridge(Ports{}, time.Second)
	strategy := &artifactStrategy{issuer: issuer, deadline: bridge.deadline, lifespans: &engine.Config{}}
	requester := engine.NewAccessRequest(newSession())

	_, _, err := strategy.GenerateAccessToken(context.Background(), requester)
	if CodeOf(err) != CodeCeremonyMisuse {
		t.Fatalf("выпуск без границы срока: случай %v, ожидался %v", CodeOf(err), CodeCeremonyMisuse)
	}
	if issuer.calls != 0 {
		t.Errorf("порт выпуска позван без границы срока: обращений %d", issuer.calls)
	}
}
