// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// refresh_replay_test.go — пробы ПОВТОРА токена обновления (RFC 9700 §4.14.2,
// RFC 6819 §5.2.2.3).
//
// Предмет — СОСТОЯНИЕ гранта после отказа, а не текст отказа. Токен обновления,
// предъявленный после оборота, означает, что им владеют двое, и сервер не знает,
// кто из них законный: семейство гранта обязано умереть целиком — и прежний
// токен доступа, и пара, выданная оборотом. Проба, проверяющая только то, что
// повтор отвергнут, зелена и там, где выданная оборотом пара пережила повтор:
// ровно так и было, пока подставка отдавала обёрнутый токен как живой.
//
// Повтор бывает двух видов, и оба проверены отдельно, потому что движок ведёт
// их разными путями:
//
//   - ПОСЛЕДОВАТЕЛЬНЫЙ — обёрнутый токен предъявлен после того, как оборот
//     состоялся; его видит выборка;
//   - ОДНОВРЕМЕННЫЙ — два предъявления одного токена прошли выборку раньше, чем
//     хоть одно обернуло токен; его видит только число строк оборота.
//
// Законный близнец обоих — оборот без повтора: семейство живо.
package oauthceremony_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// exchangeCode проходит церемонию до первой пары токенов.
func exchangeCode(t *testing.T, ceremony *oauthceremony.Ceremony) oauthceremony.TokenResult {
	t.Helper()

	code, _ := issueCode(t, ceremony)
	tokens, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	})
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("обмен кода не выдал пары токенов: доступа %q, обновления %q", tokens.AccessToken, tokens.RefreshToken)
	}
	return tokens
}

func refreshRequest(refreshToken string) oauthceremony.TokenRequest {
	return oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantRefreshToken,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		RefreshToken: refreshToken,
		Scopes:       []string{"openid", "offline"},
	}
}

// introspect отвечает, назван ли артефакт годным. Отказ интроспекции — не
// «негоден», а поломка пробы: RFC 7662 §2.2 на негодный артефакт велит
// отвечать `active: false`.
func introspect(t *testing.T, ceremony *oauthceremony.Ceremony, token string, kind oauthceremony.TokenKind) oauthceremony.IntrospectionResult {
	t.Helper()

	result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
		Token:        token,
		KindHint:     kind,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	})
	if err != nil {
		t.Fatalf("интроспекция %s отказала вместо ответа active=false: %v", kind, err)
	}
	return result
}

// grantOf — грант, породивший токен доступа. Берётся ДО повтора: после отзыва
// спросить уже не у кого.
func grantOf(t *testing.T, ceremony *oauthceremony.Ceremony, accessToken string) string {
	t.Helper()

	result := introspect(t, ceremony, accessToken, oauthceremony.TokenKindAccess)
	if !result.Active || result.GrantID == "" {
		t.Fatalf("только что выданный токен доступа не назвал своего гранта: %+v", result)
	}
	return result.GrantID
}

// requireReplayRefusal — отказ на повтор: провод говорит `invalid_grant`, а
// значение — отдельным случаем токена обновления, не «код погашен» и не
// «внутренняя ошибка».
func requireReplayRefusal(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("повтор токена обновления прошёл")
	}
	if wire := oauthceremony.CodeOf(err).WireCode(); wire != "invalid_grant" {
		t.Errorf("на проводе повтор назван %q, ожидался \"invalid_grant\" (случай %v)", wire, oauthceremony.CodeOf(err))
	}
	if errors.Is(err, oauthceremony.ErrServerError) {
		t.Errorf("повтор назван внутренней ошибкой: %v", err)
	}
	if !errors.Is(err, oauthceremony.ErrRefreshTokenRotated) {
		t.Errorf("повтор отвергнут случаем %v, ожидался %v",
			oauthceremony.CodeOf(err), oauthceremony.CodeRefreshTokenRotated)
	}
}

// requireFamilyDead — у гранта не осталось ни одного живого артефакта, и
// отозвано именно СЕМЕЙСТВО, а не снято по одному то, что попалось, — с
// причиной «повтор токена обновления» у каждого вызова порта отзыва.
func requireFamilyDead(t *testing.T, ceremony *oauthceremony.Ceremony, store *memoryPorts, grantID string,
	accessTokens, refreshTokens []string) {
	t.Helper()

	if !store.familyRevoked(grantID) {
		t.Errorf("семейство гранта %s не отозвано", grantID)
	}
	requireRevokedFor(t, store, grantID, oauthceremony.RevocationRefreshReplay)
	if access, refresh := store.liveArtifactsOf(grantID); access != 0 || refresh != 0 {
		t.Errorf("у гранта %s после повтора живы токенов доступа %d шт, токенов обновления %d шт",
			grantID, access, refresh)
	}
	for i, token := range accessTokens {
		if introspect(t, ceremony, token, oauthceremony.TokenKindAccess).Active {
			t.Errorf("токен доступа №%d гранта пережил повтор", i+1)
		}
	}
	for i, token := range refreshTokens {
		if introspect(t, ceremony, token, oauthceremony.TokenKindRefresh).Active {
			t.Errorf("токен обновления №%d гранта пережил повтор", i+1)
		}
		if _, err := ceremony.Exchange(context.Background(), refreshRequest(token)); err == nil {
			t.Errorf("токен обновления №%d гранта после повтора ещё выдаёт токены", i+1)
		}
	}
}

// TestRefreshWithoutReplayKeepsTheFamilyAlive — законный близнец: оборот без
// повтора семейства не трогает. Без него пробы ниже были бы зелены и у
// церемонии, отзывающей семейство на КАЖДОМ обороте.
func TestRefreshWithoutReplayKeepsTheFamilyAlive(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, first.AccessToken)

	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("оборот токена обновления отказал: %v", err)
	}

	if store.familyRevoked(grantID) {
		t.Fatal("оборот без повтора отозвал семейство")
	}
	requireNotRevoked(t, store, grantID)
	if !introspect(t, ceremony, second.AccessToken, oauthceremony.TokenKindAccess).Active {
		t.Error("токен доступа, выданный оборотом, назван негодным")
	}
	if !introspect(t, ceremony, second.RefreshToken, oauthceremony.TokenKindRefresh).Active {
		t.Error("токен обновления, выданный оборотом, назван негодным")
	}
	if _, err := ceremony.Exchange(context.Background(), refreshRequest(second.RefreshToken)); err != nil {
		t.Errorf("токен обновления, выданный оборотом, не обернулся: %v", err)
	}
}

// TestSequentialRefreshReplayRevokesTheFamily — обёрнутый токен предъявлен
// после оборота.
func TestSequentialRefreshReplayRevokesTheFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, first.AccessToken)

	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("первый оборот отказал: %v", err)
	}

	_, err = ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	requireReplayRefusal(t, err)
	requireFamilyDead(t, ceremony, store, grantID,
		[]string{first.AccessToken, second.AccessToken},
		[]string{first.RefreshToken, second.RefreshToken})
}

// TestConcurrentRefreshReplayRevokesTheFamily — два предъявления одного токена
// прошли выборку раньше, чем хоть одно его обернуло.
//
// Победитель гонки получает пару токенов, проигравший — ноль строк оборота.
// Ноль строк и есть повтор: отзывается семейство, и пара победителя умирает
// вместе с ним — сервер не знает, который из двоих законный.
func TestConcurrentRefreshReplayRevokesTheFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, first.AccessToken)

	const racers = 2
	gate := newRendezvous(racers)
	store.refreshFetchGate = gate

	type outcome struct {
		tokens oauthceremony.TokenResult
		err    error
	}
	outcomes := make([]outcome, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
			outcomes[i] = outcome{tokens: tokens, err: err}
		}()
	}
	wg.Wait()

	if !gate.met() {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: оба предъявления не встретились на выборке — одновременность не создана, "+
			"вердикта о повторе нет (исходы: %v; %v)", outcomes[0].err, outcomes[1].err)
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
		t.Fatalf("из %d одновременных предъявлений прошло %d шт и отказано %d шт; ожидалось 1 и 1 (отказы: %v)",
			racers, len(winners), len(refusals), refusals)
	}

	requireReplayRefusal(t, refusals[0])
	requireFamilyDead(t, ceremony, store, grantID,
		[]string{first.AccessToken, winners[0].AccessToken},
		[]string{first.RefreshToken, winners[0].RefreshToken})
}

// TestIntrospectingARotatedRefreshTokenAnswersInactive — обёрнутый токен
// обновления больше не годен, и интроспекция говорит это ответом
// `active: false`, а не отказом (RFC 7662 §2.2).
//
// Близнец в той же пробе — токен, выданный оборотом: он годен.
func TestIntrospectingARotatedRefreshTokenAnswersInactive(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("оборот отказал: %v", err)
	}

	if !introspect(t, ceremony, second.RefreshToken, oauthceremony.TokenKindRefresh).Active {
		t.Fatal("близнец: токен обновления, выданный оборотом, назван негодным")
	}
	stale := introspect(t, ceremony, first.RefreshToken, oauthceremony.TokenKindRefresh)
	if stale.Active {
		t.Error("обёрнутый токен обновления назван годным")
	}
	if stale.Subject != "" || stale.ClientID != "" || stale.GrantID != "" {
		t.Errorf("ответ о негодном токене несёт подробности: %+v", stale)
	}
}

// TestRevokingAStaleRefreshTokenRevokesTheFamily — клиент отзывает грант
// прежним, уже обёрнутым токеном (выход из сеанса со старой записью).
//
// RFC 7009 §2.1: отзыв токена обновления снимает и токены доступа того же
// гранта. Обёрнутый токен предъявлен — значит, семейство отзывается целиком;
// ответить «успех», оставив живой пару, выданную оборотом, значило бы солгать
// клиенту о выходе.
func TestRevokingAStaleRefreshTokenRevokesTheFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, first.AccessToken)
	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("оборот отказал: %v", err)
	}

	if err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
		Token:        first.RefreshToken,
		KindHint:     oauthceremony.TokenKindRefresh,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	}); err != nil {
		t.Fatalf("отзыв обёрнутым токеном отказал: %v", err)
	}

	if !store.familyRevoked(grantID) {
		t.Error("отзыв обёрнутым токеном не отозвал семейство")
	}
	// Обёрнутый токен, предъявленный точке отзыва, мост замечает как повтор,
	// но семейство снимается потому, что клиент попросил (RFC 7009 §2.1).
	requireRevokedFor(t, store, grantID, oauthceremony.RevocationClientRevoke)
	if introspect(t, ceremony, second.AccessToken, oauthceremony.TokenKindAccess).Active {
		t.Error("токен доступа, выданный оборотом, пережил отзыв")
	}
	if introspect(t, ceremony, second.RefreshToken, oauthceremony.TokenKindRefresh).Active {
		t.Error("токен обновления, выданный оборотом, пережил отзыв")
	}
}

// TestReplayOfATokenWhoseClientIsGoneStillRevokesTheFamily — обёрнутый токен
// клиента, которого уже сняли, предъявлен другим клиентом. Запрос движка по
// такому гранту не собрать (клиента нет в справочнике), но повтор от этого не
// перестаёт быть повтором: семейство отзывается по гранту, записанному до
// сборки. Предъявитель — клиент, прошедший сверку секрета
// (requireAuthenticatedBy), как у пары на пути кода.
func TestReplayOfATokenWhoseClientIsGoneStillRevokesTheFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	const otherClientID = "svc-other"
	registerClientLike(t, store, otherClientID)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, first.AccessToken)
	if _, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken)); err != nil {
		t.Fatalf("оборот отказал: %v", err)
	}

	store.mu.Lock()
	delete(store.clients, testClientID)
	store.mu.Unlock()

	replay := refreshRequest(first.RefreshToken)
	replay.ClientID = otherClientID
	verified := len(store.verificationLog())
	_, err := ceremony.Exchange(context.Background(), replay)
	requireAuthenticatedBy(t, store.verificationLog()[verified:], otherClientID)
	requireReplayRefusal(t, err)

	if !store.familyRevoked(grantID) {
		t.Error("семейство гранта снятого клиента пережило повтор")
	}
	requireRevokedFor(t, store, grantID, oauthceremony.RevocationRefreshReplay)
	if access, refresh := store.liveArtifactsOf(grantID); access != 0 || refresh != 0 {
		t.Errorf("у гранта после повтора живы токенов доступа %d шт, токенов обновления %d шт", access, refresh)
	}
}
