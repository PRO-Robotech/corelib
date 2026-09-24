// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// revocation_reason_test.go — порт отзыва получает ПРИЧИНУ отзыва.
//
// Предмет — значение, которое получила подставка порта, а не факт вызова.
// Служба пишет причину в свой закрытый словарь, и по одному факту вызова она
// не различила бы повтор кода авторизации, повтор токена обновления и отзыв
// клиентом: все три снимают одно и то же семейство одними и теми же методами.
//
// Путей отзыва больше, чем причин, и каждая причина проверена на каждом своём
// пути, потому что отзывают разные исполнители:
//
//   - движок — на последовательном повторе кода и токена обновления и на
//     отзыве живого токена клиентом;
//   - церемония — по завершении операции, на одновременном повторе и на отзыве
//     клиентом обёрнутым токеном обновления.
//
// Пути повтора судятся помощниками requirePairDead (код) и requireFamilyDead
// (токен обновления) в code_replay_test.go и refresh_replay_test.go: оба
// вызывают requireRevokedFor, поэтому причину утверждает каждая проба повтора —
// последовательного и одновременного, с единицей работы и без неё. Здесь —
// словарь, отзыв клиентом и законный близнец «повтора нет — отзыва нет».
package oauthceremony_test

import (
	"context"
	"slices"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// revokerMethods — методы порта отзыва. Семейство отозвано, когда каждый из
// них вызван по гранту; вызов лишь одного оставил бы жить артефакты другого
// вида.
var revokerMethods = []string{"RevokeGrantRefreshTokens", "RevokeGrantAccessTokens"}

// requireRevokedFor — каждый метод порта отзыва вызван по гранту, и КАЖДЫЙ
// вызов назвал причину want. Пустой перечень вызовов — красное, а не «чужих
// причин нет»: проба, не увидевшая ни одного вызова, ничего не утвердила.
func requireRevokedFor(t *testing.T, store *memoryPorts, grantID string, want oauthceremony.RevocationReason) {
	t.Helper()

	calls := store.revocationsOf(grantID)
	perMethod := map[string]int{}
	for _, call := range calls {
		perMethod[call.method]++
		if call.reason != want {
			t.Errorf("порт отзыва получил в %s по гранту %s причину %q, ожидалась %q",
				call.method, grantID, call.reason, want)
		}
	}
	for _, method := range revokerMethods {
		if perMethod[method] == 0 {
			t.Errorf("метод %s порта отзыва по гранту %s не вызван (вызовов порта всего %d)",
				method, grantID, len(calls))
		}
	}
}

// requireNotRevoked — по гранту не было ни одного вызова порта отзыва.
func requireNotRevoked(t *testing.T, store *memoryPorts, grantID string) {
	t.Helper()

	if calls := store.revocationsOf(grantID); len(calls) != 0 {
		t.Errorf("по гранту %s без повтора и без просьбы клиента порт отзыва вызван %d раз: %+v",
			grantID, len(calls), calls)
	}
}

// TestRevocationReasonDictionaryIsClosed — словарь причин ровно из трёх
// значений, и их написание — контракт со службой: она сопрягает его со своим
// закрытым словарём по значению. Значения «причина не названа» у словаря нет:
// нулевое значение типа и слово вне перечня словарю не принадлежат.
func TestRevocationReasonDictionaryIsClosed(t *testing.T) {
	want := []oauthceremony.RevocationReason{"code-replay", "refresh-replay", "client-revoke"}
	got := oauthceremony.RevocationReasons()
	if !slices.Equal(got, want) {
		t.Fatalf("словарь причин отзыва %q, ожидался %q", got, want)
	}

	named := map[oauthceremony.RevocationReason]oauthceremony.RevocationReason{
		oauthceremony.RevocationCodeReplay:    "code-replay",
		oauthceremony.RevocationRefreshReplay: "refresh-replay",
		oauthceremony.RevocationClientRevoke:  "client-revoke",
	}
	for constant, spelling := range named {
		if constant != spelling {
			t.Errorf("постоянная причины пишется %q, ожидалось %q", constant, spelling)
		}
		if !constant.Declared() {
			t.Errorf("причина %q словаря названа необъявленной", constant)
		}
	}

	for _, outside := range []oauthceremony.RevocationReason{"", "logout", "Code-Replay", "code_replay"} {
		if outside.Declared() {
			t.Errorf("значение %q вне словаря названо объявленной причиной", outside)
		}
	}
}

// TestClientRevocationNamesClientRevokeToThePort — клиент отзывает через точку
// отзыва живой токен доступа и живой токен обновления: семейство снимает
// движок, и порт получает «client-revoke».
//
// Близнец — TestExchangeWithoutReplayRevokesNothing (отличие — просьба
// клиента): без неё порт отзыва не зовут вовсе.
func TestClientRevocationNamesClientRevokeToThePort(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  oauthceremony.TokenKind
		token func(oauthceremony.TokenResult) string
	}{
		{name: "access-token", kind: oauthceremony.TokenKindAccess,
			token: func(r oauthceremony.TokenResult) string { return r.AccessToken }},
		{name: "refresh-token", kind: oauthceremony.TokenKindRefresh,
			token: func(r oauthceremony.TokenResult) string { return r.RefreshToken }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			tokens := exchangeCode(t, ceremony)
			grantID := grantOf(t, ceremony, tokens.AccessToken)

			if err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
				Token:        tc.token(tokens),
				KindHint:     tc.kind,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			}); err != nil {
				t.Fatalf("отзыв клиентом отказал: %v", err)
			}

			requireRevokedFor(t, store, grantID, oauthceremony.RevocationClientRevoke)
		})
	}
}

// TestExchangeWithoutReplayRevokesNothing — законный близнец всех проб
// причины: обмен кода и оборот токена обновления без повтора порт отзыва не
// зовут. Без него requireRevokedFor был бы зелен и у церемонии, отзывающей
// семейство с верной причиной на каждом обмене.
func TestExchangeWithoutReplayRevokesNothing(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, first.AccessToken)
	if _, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken)); err != nil {
		t.Fatalf("оборот токена обновления отказал: %v", err)
	}

	requireNotRevoked(t, store, grantID)
}
