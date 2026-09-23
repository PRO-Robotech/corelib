// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// code_replay_test.go — пробы ПОВТОРНОГО предъявления кода авторизации
// (RFC 6749 §4.1.2, замечание о безопасности; RFC 9700 §4.2.4).
//
// Предмет — СОСТОЯНИЕ гранта, а не текст отказа: код, предъявленный дважды,
// означает, что им владеют двое, и выданное по нему обязано умереть. Полоса
// кода обязана вести себя как полоса токена обновления (refresh_replay_test.go):
// повтор замечается и последовательный, и одновременный, отзыв исполняет
// церемония по завершении операции, а отказ отзыва — отказ операции, а не
// случай «код погашен», за которым живое семейство.
//
// Код гасится РОВНО ОДИН РАЗ за обмен — при предъявлении, до сверки
// доказательства (привязка PKCE — поле той же записи и снимается вместе с
// кодом). Поэтому одновременный повтор у кода один путь, которым его замечает
// проигравший: оба обмена прошли выборку кода, и погашение одного из них
// затрагивает ноль строк. Окно воспроизводится двумя встречами — на выборке
// кода и перед погашением, — с единицей работы и без неё.
package oauthceremony_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

func codeExchange(code string) oauthceremony.TokenRequest {
	return oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	}
}

// requireCodeReplayRefusal — отказ на повтор кода: на проводе `invalid_grant`,
// по значению — «код уже погашен», не «внутренняя ошибка».
func requireCodeReplayRefusal(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("повторное предъявление кода прошло")
	}
	if !errors.Is(err, oauthceremony.ErrAuthorizationCodeConsumed) {
		t.Errorf("повтор кода отвергнут случаем %v, ожидался %v",
			oauthceremony.CodeOf(err), oauthceremony.CodeAuthorizationCodeConsumed)
	}
	if wire := oauthceremony.CodeOf(err).WireCode(); wire != "invalid_grant" {
		t.Errorf("на проводе повтор кода назван %q, ожидался \"invalid_grant\"", wire)
	}
}

// requirePairDead — пара, выданная по коду, негодна, и отозвано семейство —
// с причиной «повтор кода авторизации» у каждого вызова порта отзыва.
func requirePairDead(t *testing.T, ceremony *oauthceremony.Ceremony, store *memoryPorts, grantID string,
	tokens oauthceremony.TokenResult) {
	t.Helper()

	if !store.familyRevoked(grantID) {
		t.Errorf("семейство гранта %s не отозвано", grantID)
	}
	requireRevokedFor(t, store, grantID, oauthceremony.RevocationCodeReplay)
	if access, refresh := store.liveArtifactsOf(grantID); access != 0 || refresh != 0 {
		t.Errorf("у гранта %s после повтора кода живы токенов доступа %d шт, токенов обновления %d шт",
			grantID, access, refresh)
	}
	if introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Active {
		t.Error("токен доступа, выданный по коду, пережил повтор кода")
	}
	if introspect(t, ceremony, tokens.RefreshToken, oauthceremony.TokenKindRefresh).Active {
		t.Error("токен обновления, выданный по коду, пережил повтор кода")
	}
}

// TestSingleCodeExchangeKeepsThePairAlive — законный близнец: один обмен,
// семейство живо. Без него пробы ниже были бы зелены и у церемонии,
// отзывающей семейство на каждом обмене.
func TestSingleCodeExchangeKeepsThePairAlive(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	tokens := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, tokens.AccessToken)

	if store.familyRevoked(grantID) {
		t.Fatal("одиночный обмен отозвал семейство")
	}
	requireNotRevoked(t, store, grantID)
	if !introspect(t, ceremony, tokens.RefreshToken, oauthceremony.TokenKindRefresh).Active {
		t.Error("токен обновления одиночного обмена назван негодным")
	}
}

// TestSequentialCodeReplayRevokesTheFamily — код предъявлен после обмена.
func TestSequentialCodeReplayRevokesTheFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	first, err := ceremony.Exchange(context.Background(), codeExchange(code))
	if err != nil {
		t.Fatalf("первый обмен отказал: %v", err)
	}
	grantID := grantOf(t, ceremony, first.AccessToken)

	_, err = ceremony.Exchange(context.Background(), codeExchange(code))
	requireCodeReplayRefusal(t, err)
	requirePairDead(t, ceremony, store, grantID, first)
}

// failingRevoker — отказ порта отзыва; текст — чтобы находка его называла.
var failingRevoker = errors.New("revoker: storage is down")

// TestSequentialCodeReplayWithAFailingRevokerFailsTheOperation — отзыв не
// состоялся, и это отказ ОПЕРАЦИИ: ответить «код погашен», оставив живым
// выданное по нему, значило бы выдать обнаруженную атаку за отражённую.
//
// Близнец — TestSequentialCodeReplayRevokesTheFamily (отличие — исход порта
// отзыва).
func TestSequentialCodeReplayWithAFailingRevokerFailsTheOperation(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	if _, err := ceremony.Exchange(context.Background(), codeExchange(code)); err != nil {
		t.Fatalf("первый обмен отказал: %v", err)
	}

	store.mu.Lock()
	store.revokeFailure = failingRevoker
	store.mu.Unlock()

	_, err := ceremony.Exchange(context.Background(), codeExchange(code))
	if err == nil {
		t.Fatal("повтор кода при отказавшем порте отзыва прошёл")
	}
	if errors.Is(err, oauthceremony.ErrAuthorizationCodeConsumed) {
		t.Fatalf("отказ отзыва скрыт за случаем «код погашен»: %v", err)
	}
	if !errors.Is(err, oauthceremony.ErrServerError) {
		t.Errorf("отказ отзыва назван случаем %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodeServerError)
	}
}

// TestSequentialRefreshReplayWithAFailingRevokerFailsTheOperation — та же
// половина на полосе токена обновления.
//
// Близнец — TestSequentialRefreshReplayRevokesTheFamily.
func TestSequentialRefreshReplayWithAFailingRevokerFailsTheOperation(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	if _, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken)); err != nil {
		t.Fatalf("первый оборот отказал: %v", err)
	}

	store.mu.Lock()
	store.revokeFailure = failingRevoker
	store.mu.Unlock()

	_, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err == nil {
		t.Fatal("повтор токена обновления при отказавшем порте отзыва прошёл")
	}
	if errors.Is(err, oauthceremony.ErrRefreshTokenRotated) {
		t.Fatalf("отказ отзыва скрыт за случаем «токен обёрнут»: %v", err)
	}
	if !errors.Is(err, oauthceremony.ErrServerError) {
		t.Errorf("отказ отзыва назван случаем %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodeServerError)
	}
}

// TestCodePresentedAgainAfterAFailedProofIsAReplay — первое предъявление с
// неверным доказательством погасило код и не выдало ничего, второе — с верным
// доказательством либо без него. Предъявление и есть использование кода: код
// уже предъявлялся, и это повтор, а не второй шанс сверить доказательство.
func TestCodePresentedAgainAfterAFailedProofIsAReplay(t *testing.T) {
	for _, tc := range []struct {
		name           string
		secondVerifier string
	}{
		{name: "second-with-the-right-verifier", secondVerifier: testVerifier},
		{name: "second-without-a-verifier", secondVerifier: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			code, _ := issueCode(t, ceremony)
			wrong := codeExchange(code)
			wrong.CodeVerifier = "wrong-verifier-wrong-verifier-wrong-verifier-00"
			if _, err := ceremony.Exchange(context.Background(), wrong); err == nil {
				t.Fatal("обмен с неверным доказательством прошёл")
			}

			second := codeExchange(code)
			second.CodeVerifier = tc.secondVerifier
			tokens, err := ceremony.Exchange(context.Background(), second)
			requireCodeReplayRefusal(t, err)
			if tokens.AccessToken != "" {
				t.Error("повтор кода выдал токен доступа")
			}
			store.mu.Lock()
			issued := len(store.access)
			store.mu.Unlock()
			if issued != 0 {
				t.Errorf("по коду, предъявленному второй раз, положено токенов доступа %d шт", issued)
			}
			requireRevokedFor(t, store, grantOfStored(t, store), oauthceremony.RevocationCodeReplay)
		})
	}
}

// codeRace — одновременное предъявление одного кода двумя обменами.
type codeRace struct {
	name  string
	setup func(store *memoryPorts) []*rendezvous
}

func codeRaces() []codeRace {
	return []codeRace{
		{
			// Встреча на выборке кода (оба прочли код непогашенным) и перед
			// погашением при предъявлении (оба дошли до него): повтор виден
			// только по нулю строк погашения.
			name: "both-read-then-consume",
			setup: func(store *memoryPorts) []*rendezvous {
				store.codeFetchGate = newRendezvous(2)
				store.consumeGate = newRendezvous(2)
				return []*rendezvous{store.codeFetchGate, store.consumeGate}
			},
		},
	}
}

// TestConcurrentCodeRedemptionRevokesTheFamily — ровно один обмен проходит,
// второй получает отказ повтора, и пара ПОБЕДИТЕЛЯ после этого негодна:
// сервер не знает, который из двоих законный.
func TestConcurrentCodeRedemptionRevokesTheFamily(t *testing.T) {
	for _, race := range codeRaces() {
		for _, withUnit := range []bool{false, true} {
			name := race.name + "/no-unit-of-work"
			if withUnit {
				name = race.name + "/unit-of-work"
			}
			t.Run(name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ports := store.ports()
				if withUnit {
					ports.Transaction = &recordingUnitOfWork{}
				}
				ceremony := newTestCeremony(t, ports)
				code, _ := issueCode(t, ceremony)
				gates := race.setup(store)

				type outcome struct {
					tokens oauthceremony.TokenResult
					err    error
				}
				outcomes := make([]outcome, 2)
				var wg sync.WaitGroup
				for i := range outcomes {
					wg.Add(1)
					go func() {
						defer wg.Done()
						tokens, err := ceremony.Exchange(context.Background(), codeExchange(code))
						outcomes[i] = outcome{tokens: tokens, err: err}
					}()
				}
				wg.Wait()

				for i, gate := range gates {
					if !gate.met() {
						t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: встреча №%d не состоялась — одновременность не создана "+
							"(исходы: %v; %v)", i+1, outcomes[0].err, outcomes[1].err)
					}
				}

				var winners []oauthceremony.TokenResult
				var refusals []error
				for _, o := range outcomes {
					if o.err == nil {
						winners = append(winners, o.tokens)
						continue
					}
					refusals = append(refusals, o.err)
				}
				if len(winners) != 1 || len(refusals) != 1 {
					t.Fatalf("из 2 одновременных обменов прошло %d и отказано %d; ожидалось 1 и 1 (отказы: %v)",
						len(winners), len(refusals), refusals)
				}
				requireCodeReplayRefusal(t, refusals[0])

				grantID := grantOfStored(t, store)
				requirePairDead(t, ceremony, store, grantID, winners[0])
			})
		}
	}
}

// grantOfStored — грант единственного выданного кода. Берётся из хранилища:
// после отзыва интроспекция гранта не назовёт.
func grantOfStored(t *testing.T, store *memoryPorts) string {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.codes) != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: в хранилище кодов %d шт, ожидался 1", len(store.codes))
	}
	for _, row := range store.codes {
		return row.grant.GrantID
	}
	return ""
}

// TestConsumedCodeWithoutItsGrantIsAContractBreach — выборка, назвавшая код
// погашенным, обязана отдать и его грант: по нему отзывается выданное. Без
// гранта отзывать нечего, и это дефект порта, а не «код погашен» — иначе
// церемония ответила бы «выданное отозвано», не отозвав ничего.
//
// Близнец — TestSequentialCodeReplayRevokesTheFamily (отличие —
// идентификатор гранта в записи).
func TestConsumedCodeWithoutItsGrantIsAContractBreach(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	store.fetchCodeOverride = func(string) (oauthceremony.AuthorizationCodeRecord, error) {
		return oauthceremony.AuthorizationCodeRecord{Grant: oauthceremony.GrantRecord{ClientID: testClientID}},
			oauthceremony.ErrAuthorizationCodeConsumed
	}

	_, err := ceremony.Exchange(context.Background(), codeExchange(code))
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
	}
	if errors.Is(err, oauthceremony.ErrAuthorizationCodeConsumed) {
		t.Error("погашенный код без гранта выдан за повтор, после которого выданное отозвано")
	}
}
