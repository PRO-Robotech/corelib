// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// replay_build_test.go — повтор, по записи которого запрос движку не собрать.
//
// Повтор кода авторизации и токена обновления мост замечает на выборке и уже
// ПОТОМ собирает по записи повторённого артефакта запрос движку. Собрать его
// удаётся не всегда: клиента могли снять, а запись сеанса может оказаться
// такой, какую не принимает hydrateSession. Повтор от этого повтором быть не
// перестаёт — это сигнал, что артефактом владеют двое. Поэтому предмет проб
// один: ответ операции — случай повтора, а семейство отзывается по гранту,
// замеченному до сборки, с причиной повтора.
//
// Причин отказа сборки две, и проверены обе:
//
//   - запись сеанса — каждый вид, который не принимает hydrateSession, на обоих
//     путях повтора (обмен кода, оборот токена обновления) и на обеих точках,
//     куда предъявляют обёрнутый токен (отзыв, интроспекция);
//   - снятый клиент — на пути кода; путь токена обновления закрепляет
//     TestReplayOfATokenWhoseClientIsGoneStillRevokesTheFamily.
//
// Законный близнец каждой клетки — та же запись без правки.
package oauthceremony_test

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// corruptCodeRecords портит запись сеанса у каждого кода в хранилище. Пусто —
// не портит.
func corruptCodeRecords(store *memoryPorts, corrupt func(*oauthceremony.SessionRecord)) {
	if corrupt == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, row := range store.codes {
		corrupt(&row.grant.Session)
	}
}

// requireFamilyRevokedFor — семейство гранта отозвано с причиной want у каждого
// вызова порта отзыва, и живых артефактов у гранта не осталось.
func requireFamilyRevokedFor(t *testing.T, store *memoryPorts, grantID string, want oauthceremony.RevocationReason) {
	t.Helper()

	if !store.familyRevoked(grantID) {
		t.Errorf("семейство гранта %s не отозвано", grantID)
	}
	requireRevokedFor(t, store, grantID, want)
	if access, refresh := store.liveArtifactsOf(grantID); access != 0 || refresh != 0 {
		t.Errorf("у гранта %s живы токенов доступа %d шт, токенов обновления %d шт", grantID, access, refresh)
	}
}

// rotateOnce проходит церемонию до пары, выданной оборотом, и отдаёт первую
// пару (её токен обновления теперь обёрнут) и грант семейства.
func rotateOnce(t *testing.T, ceremony *oauthceremony.Ceremony) (oauthceremony.TokenResult, string) {
	t.Helper()

	first := exchangeCode(t, ceremony)
	grantID := grantOf(t, ceremony, first.AccessToken)
	if _, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken)); err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: оборот отказал, обёрнутого токена нет: %v", err)
	}
	return first, grantID
}

// TestReplayWithABrokenSessionRecordIsStillAReplay — повторённый артефакт,
// запись сеанса которого не принимает hydrateSession. Мост замечает повтор
// раньше, чем собирает запрос. Поэтому ответ — случай повтора, а не нарушение
// контракта порта, и семейство отзывается с причиной повтора.
//
// Живой артефакт с той же порчей — нарушение контракта; это держит
// TestStoredRecordWithoutAGoodLoginContextIsAContractBreach. Клетки там и
// здесь различаются одним фактом: погашен ли артефакт.
func TestReplayWithABrokenSessionRecordIsStillAReplay(t *testing.T) {
	corruptions := []struct {
		name    string
		corrupt func(*oauthceremony.SessionRecord)
	}{
		{name: "близнец без правки"},
		{name: "SessionRecord.SessionID", corrupt: func(s *oauthceremony.SessionRecord) { s.SessionID = "" }},
		{name: "SessionRecord.ACR", corrupt: func(s *oauthceremony.SessionRecord) { s.ACR = "0" }},
		{name: "SessionRecord.AuthTime", corrupt: func(s *oauthceremony.SessionRecord) { s.AuthTime = time.Time{} }},
		{name: "SessionRecord.Claims", corrupt: func(s *oauthceremony.SessionRecord) {
			s.Claims = maps.Clone(s.Claims)
			s.Claims["acr"] = "3"
		}},
		{name: "SessionRecord.NotAfter", corrupt: func(s *oauthceremony.SessionRecord) {
			s.NotAfter = map[oauthceremony.TokenKind]time.Time{oauthceremony.TokenKindRefresh: {}}
		}},
		{name: "SessionRecord.ExpiresAt", corrupt: func(s *oauthceremony.SessionRecord) {
			s.ExpiresAt = maps.Clone(s.ExpiresAt)
			s.ExpiresAt[oauthceremony.TokenKindRefresh] = time.Time{}
		}},
	}

	surfaces := []struct {
		name string
		// run проходит церемонию до повторённого артефакта, портит его
		// запись, предъявляет его и судит исход.
		run func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord))
	}{
		{name: "повтор кода при обмене", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) {
			code, _ := issueCode(t, ceremony)
			first, err := ceremony.Exchange(context.Background(), codeExchange(code))
			if err != nil {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: первый обмен отказал, погашенного кода нет: %v", err)
			}
			grantID := grantOf(t, ceremony, first.AccessToken)
			corruptCodeRecords(store, corrupt)

			_, err = ceremony.Exchange(context.Background(), codeExchange(code))
			requireCodeReplayRefusal(t, err)
			requireFamilyRevokedFor(t, store, grantID, oauthceremony.RevocationCodeReplay)
		}},
		{name: "повтор токена обновления при обороте", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) {
			first, grantID := rotateOnce(t, ceremony)
			corruptRefreshRecords(store, corrupt)

			_, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
			requireReplayRefusal(t, err)
			requireFamilyRevokedFor(t, store, grantID, oauthceremony.RevocationRefreshReplay)
		}},
		// Отзыв обёрнутым токеном: семейство снимается по просьбе клиента
		// (RFC 7009 §2.1), и ответ — успех, а не отказ.
		{name: "отзыв обёрнутым токеном", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) {
			first, grantID := rotateOnce(t, ceremony)
			corruptRefreshRecords(store, corrupt)

			if err := ceremony.Revoke(context.Background(), revocationRequest(first.RefreshToken, oauthceremony.TokenKindRefresh)); err != nil {
				t.Errorf("отзыв обёрнутым токеном отказал случаем %v: %v", oauthceremony.CodeOf(err), err)
			}
			requireFamilyRevokedFor(t, store, grantID, oauthceremony.RevocationClientRevoke)
		}},
		// Интроспекция обёрнутого токена: он негоден, и это ответ
		// `active: false` (RFC 7662 §2.2). Семейства интроспекция не отзывает.
		{name: "интроспекция обёрнутого токена", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) {
			first, grantID := rotateOnce(t, ceremony)
			corruptRefreshRecords(store, corrupt)

			result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
				Token: first.RefreshToken, KindHint: oauthceremony.TokenKindRefresh,
				ClientID: testClientID, ClientSecret: testSecret, AuthMethod: oauthceremony.ClientAuthBasic,
			})
			switch {
			case err != nil:
				t.Errorf("интроспекция обёрнутого токена отказала случаем %v вместо active=false: %v", oauthceremony.CodeOf(err), err)
			case result.Active:
				t.Error("обёрнутый токен обновления назван годным")
			}
			requireNotRevoked(t, store, grantID)
		}},
	}

	for _, surface := range surfaces {
		for _, tc := range corruptions {
			t.Run(surface.name+"/"+tc.name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ceremony := newTestCeremony(t, store.ports())

				surface.run(t, store, ceremony, tc.corrupt)
			})
		}
	}
}

// TestCodeReplayOfAClientThatIsGoneStillRevokesTheFamily — погашенный код
// клиента, которого уже сняли, предъявлен другим клиентом. Запрос движка по
// такому гранту не собрать (клиента нет в справочнике), но повтор остаётся
// повтором: ответ — «код погашен», семейство отзывается по гранту,
// замеченному до сборки. Тот же случай на пути токена обновления держит
// TestReplayOfATokenWhoseClientIsGoneStillRevokesTheFamily.
func TestCodeReplayOfAClientThatIsGoneStillRevokesTheFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	const otherClientID = "svc-other"
	other := store.clients[testClientID]
	other.ClientID = otherClientID
	store.clients[otherClientID] = other
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	first, err := ceremony.Exchange(context.Background(), codeExchange(code))
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: первый обмен отказал, погашенного кода нет: %v", err)
	}
	grantID := grantOf(t, ceremony, first.AccessToken)

	store.mu.Lock()
	delete(store.clients, testClientID)
	store.mu.Unlock()

	replay := codeExchange(code)
	replay.ClientID = otherClientID
	_, err = ceremony.Exchange(context.Background(), replay)
	requireCodeReplayRefusal(t, err)
	requireFamilyRevokedFor(t, store, grantID, oauthceremony.RevocationCodeReplay)
}
