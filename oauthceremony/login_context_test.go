// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// login_context_test.go — контекст входа гранта: сессия, уровень
// аутентификации (`acr`) и момент аутентификации (`auth_time`).
//
// Предмет — три утверждения:
//
//   - контекст входа — ПОЛЯ решения службы (AuthorizationGrant) и записи сеанса
//     (SessionRecord), а не ключи карты Claims; снимок, сделанный на выдаче
//     кода, переезжает через обмен кода и каждый оборот без изменения — в
//     каждой записи хранилища, в каждом гранте порта выпуска и в ответе
//     интроспекции;
//   - выдача без годного контекста отвергается до кода, а словарь уровня
//     закрыт и один на платформу (acrlevel);
//   - запись хранилища без годного контекста — нарушение контракта порта на
//     каждом пути выборки: кода, токена доступа, токена обновления.
//
// Каждый отрицательный случай меняет против законного близнеца РОВНО ОДИН
// факт.
package oauthceremony_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

const (
	// testSessionID — сессия входа, из которой служба выдаёт грант.
	testSessionID = "hss-7q2m9k4w1x3c5v8bz"
	// testACR — уровень этой сессии: не наименьший из словаря, чтобы
	// подстановка «самого слабого» не совпала с ним случайно.
	testACR = "2"
	// testTenant — ключ карты Claims, который кладёт служба: положительный
	// контроль того, что карта в записи читается и переносится.
	testTenant = "b1g0000000000000a"
)

// testAuthTime — момент аутентификации в прошлом, в целых секундах. Снимок, а
// не «сейчас»: перенос, берущий момент с часов, с ним не совпадёт.
var testAuthTime = time.Date(2026, 9, 23, 8, 41, 17, 0, time.UTC)

// loginContextClaimKeys — имена, под которыми контекст входа известен как
// утверждения токена.
var loginContextClaimKeys = []string{"sid", "acr", "auth_time"}

// loggedIn дополняет решение службы контекстом входа, как это делает служба,
// опознав человека.
func loggedIn(grant oauthceremony.AuthorizationGrant) oauthceremony.AuthorizationGrant {
	grant.SessionID = testSessionID
	grant.ACR = testACR
	grant.AuthTime = testAuthTime
	return grant
}

// loginContextMismatch называет расхождения контекста входа с выданным.
// Пусто — контекст тот же.
func loginContextMismatch(sessionID, acr string, authTime time.Time) []string {
	var off []string
	if sessionID != testSessionID {
		off = append(off, fmt.Sprintf("сессия %q, выдана %q", sessionID, testSessionID))
	}
	if acr != testACR {
		off = append(off, fmt.Sprintf("acr %q, выдан %q", acr, testACR))
	}
	if !authTime.Equal(testAuthTime) {
		off = append(off, fmt.Sprintf("auth_time %s, выдан %s", authTime.Format(time.RFC3339Nano), testAuthTime.Format(time.RFC3339)))
	}
	return off
}

// requireLoginContextIn — запись сеанса несёт выданный контекст входа полями и
// ни одного его ключа в карте. Положительный контроль той же записи — субъект
// и ключ, положенный службой: без него «контекста нет» было бы неотличимо от
// «прочитана не та запись».
func requireLoginContextIn(t *testing.T, where string, s oauthceremony.SessionRecord) {
	t.Helper()
	if s.Subject != testSubject || s.Claims["tenant"] != testTenant {
		t.Fatalf("ФИКСТУРА: %s: субъект %q и tenant %v — прочитана не та запись", where, s.Subject, s.Claims["tenant"])
	}
	for _, off := range loginContextMismatch(s.SessionID, s.ACR, s.AuthTime) {
		t.Errorf("%s: %s", where, off)
	}
	for _, key := range loginContextClaimKeys {
		if value, carried := s.Claims[key]; carried {
			t.Errorf("%s: контекст входа едет ключом карты Claims %q = %v", where, key, value)
		}
	}
}

// requireFamilyStepCarriesTheLoginContext — после выпуска пары: грант, который
// получил порт выпуска, запись токена доступа под его jti, запись токена
// обновления под sha256 его значения и интроспекция обоих называют тот же
// контекст входа.
func requireFamilyStepCarriesTheLoginContext(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, step string, tokens oauthceremony.TokenResult) {
	t.Helper()

	issuances := store.issuer.issuances()
	if len(issuances) == 0 || tokens.RefreshToken == "" {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s: выпусков %d, токен обновления %q — пары нет", step, len(issuances), tokens.RefreshToken)
	}
	last := issuances[len(issuances)-1]
	if last.issued.Token != tokens.AccessToken {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s: последний выпуск порта — не токен этого шага", step)
	}
	requireLoginContextIn(t, step+": грант порта выпуска", last.grant.Session)

	store.mu.Lock()
	access, accessFound := store.access[last.issued.ID]
	refresh, refreshFound := store.refresh[opaqueDigest(tokens.RefreshToken)]
	store.mu.Unlock()
	if !accessFound || !refreshFound {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s: запись токена доступа %v, токена обновления %v", step, accessFound, refreshFound)
	}
	requireLoginContextIn(t, step+": запись токена доступа", access.Session)
	requireLoginContextIn(t, step+": запись токена обновления", refresh.grant.Session)

	for kind, token := range map[oauthceremony.TokenKind]string{
		oauthceremony.TokenKindAccess:  tokens.AccessToken,
		oauthceremony.TokenKindRefresh: tokens.RefreshToken,
	} {
		got := introspect(t, ceremony, token, kind)
		if !got.Active || got.Subject != testSubject {
			t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s: интроспекция %s — годен %v, субъект %q", step, kind, got.Active, got.Subject)
		}
		for _, off := range loginContextMismatch(got.SessionID, got.ACR, got.AuthTime) {
			t.Errorf("%s: интроспекция %s: %s", step, kind, off)
		}
		for _, key := range loginContextClaimKeys {
			if value, carried := got.Claims[key]; carried {
				t.Errorf("%s: интроспекция %s называет контекст входа ключом карты %q = %v", step, kind, key, value)
			}
		}
	}
}

// TestLoginContextIsOneSnapshotAcrossTheFamily — грант с сессией, `acr` и
// `auth_time` → выдача кода → обмен кода → два оборота. После каждого шага
// каждая запись хранилища, грант порта выпуска и ответ интроспекции несут те
// же три значения: это снимок выдачи, а не пересчёт на шаге.
func TestLoginContextIsOneSnapshotAcrossTheFamily(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	grant := loggedIn(grantOfScopes("openid", "offline"))
	grant.Claims = map[string]any{"tenant": testTenant}
	result, err := completeWith(t, ceremony, authorizeRequest(), grant)
	if err != nil {
		t.Fatalf("выдача с годным контекстом входа отвергнута: %v", err)
	}

	store.mu.Lock()
	var code oauthceremony.SessionRecord
	codes := len(store.codes)
	for _, row := range store.codes {
		code = row.grant.Session
	}
	store.mu.Unlock()
	if codes != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: записей кода %d шт, ожидалась 1", codes)
	}
	requireLoginContextIn(t, "выдача кода: запись кода", code)

	tokens, err := ceremony.Exchange(context.Background(), codeExchange(result.Parameters["code"][0]))
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	requireFamilyStepCarriesTheLoginContext(t, store, ceremony, "обмен кода", tokens)

	for step := 1; step <= 2; step++ {
		next, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
		if err != nil {
			t.Fatalf("оборот №%d отказал: %v", step, err)
		}
		requireFamilyStepCarriesTheLoginContext(t, store, ceremony, fmt.Sprintf("оборот №%d", step), next)
		tokens = next
	}
}

// requireGrantRefusedNaming — выдача отвергнута как ошибка службы, отказ
// называет поле, и кода нет ни в ответе, ни в хранилище.
func requireGrantRefusedNaming(t *testing.T, store *memoryPorts, field string, result oauthceremony.AuthorizationResult, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("выдача принята: параметры ответа %v", result.Parameters)
	}
	if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Errorf("отвергнута случаем %v, ожидался %v: %v", oauthceremony.CodeOf(err), oauthceremony.CodeCeremonyMisuse, err)
	}
	if !strings.Contains(err.Error(), field) {
		t.Errorf("отказ не называет %s: %q", field, err.Error())
	}
	if codes := result.Parameters["code"]; len(codes) != 0 {
		t.Errorf("при отказе в ответе есть код: %v", codes)
	}
	store.mu.Lock()
	stored := len(store.codes)
	store.mu.Unlock()
	if stored != 0 {
		t.Errorf("при отказе в хранилище положено кодов %d шт", stored)
	}
}

// TestGrantWithoutAGoodLoginContextIsRefused — выдача без сессии, с уровнем
// вне словаря или без момента аутентификации отвергается до кода. Законный
// близнец каждого случая — тот же грант с годным контекстом.
func TestGrantWithoutAGoodLoginContextIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		spoil func(*oauthceremony.AuthorizationGrant)
	}{
		{name: "близнец: контекст годен"},
		{name: "сессия пуста", field: "AuthorizationGrant.SessionID",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.SessionID = "" }},
		{name: "сессия из пробелов", field: "AuthorizationGrant.SessionID",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.SessionID = " \t " }},
		{name: "уровень пуст", field: "AuthorizationGrant.ACR",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.ACR = "" }},
		{name: "уровень аноним", field: "AuthorizationGrant.ACR",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.ACR = "0" }},
		{name: "уровень выше словаря", field: "AuthorizationGrant.ACR",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.ACR = "4" }},
		{name: "уровень в чужом написании", field: "AuthorizationGrant.ACR",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.ACR = "aal2" }},
		{name: "уровень с пробелом", field: "AuthorizationGrant.ACR",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.ACR = " 2" }},
		{name: "момент нулевой", field: "AuthorizationGrant.AuthTime",
			spoil: func(g *oauthceremony.AuthorizationGrant) { g.AuthTime = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			grant := loggedIn(grantOfScopes("openid", "offline"))
			if tc.spoil == nil {
				if _, err := completeWith(t, ceremony, authorizeRequest(), grant); err != nil {
					t.Fatalf("выдача с годным контекстом входа отвергнута: %v", err)
				}
				return
			}
			tc.spoil(&grant)
			result, err := completeWith(t, ceremony, authorizeRequest(), grant)
			requireGrantRefusedNaming(t, store, tc.field, result, err)
		})
	}
}

// TestEveryLevelOfTheRankingIsAccepted — словарь уровня закрыт, но полон: каждый
// уровень, который ранжирование acrlevel ставит выше анонима, принимается и
// доезжает до записи кода тем же словом.
func TestEveryLevelOfTheRankingIsAccepted(t *testing.T) {
	for _, level := range []string{"1", "2", "3"} {
		t.Run(level, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			grant := loggedIn(grantOfScopes("openid", "offline"))
			grant.ACR = level
			if _, err := completeWith(t, ceremony, authorizeRequest(), grant); err != nil {
				t.Fatalf("уровень %q отвергнут: %v", level, err)
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			for _, row := range store.codes {
				if row.grant.Session.ACR != level {
					t.Errorf("запись кода несёт уровень %q, выдан %q", row.grant.Session.ACR, level)
				}
			}
		})
	}
}

// TestClaimsCannotRestateTheLoginContext — у значения одно место. Карта Claims,
// несущая `sid`, `acr` или `auth_time` с другим значением, чем поле, — отказ,
// называющий ключ: иначе у одного значения было бы два места, и порт выпуска
// выбирал бы между ними сам. Законный близнец — та же карта без такого ключа:
// выдача проходит, и в запись идут значения полей.
func TestClaimsCannotRestateTheLoginContext(t *testing.T) {
	restated := map[string]any{
		"sid":       "hss-0000000000000other",
		"acr":       "1",
		"auth_time": testAuthTime.Add(time.Hour).Unix(),
	}
	if len(restated) != len(loginContextClaimKeys) {
		t.Fatalf("ФИКСТУРА: подмен %d, ключей контекста входа %d", len(restated), len(loginContextClaimKeys))
	}

	t.Run("близнец: карта без ключей контекста", func(t *testing.T) {
		store := newMemoryPorts()
		registerTestClient(t, store)
		ceremony := newTestCeremony(t, store.ports())

		grant := loggedIn(grantOfScopes("openid", "offline"))
		grant.Claims = map[string]any{"tenant": testTenant}
		if _, err := completeWith(t, ceremony, authorizeRequest(), grant); err != nil {
			t.Fatalf("выдача без ключей контекста в карте отвергнута: %v", err)
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		for _, row := range store.codes {
			requireLoginContextIn(t, "запись кода", row.grant.Session)
		}
	})

	for _, key := range loginContextClaimKeys {
		t.Run(key, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			grant := loggedIn(grantOfScopes("openid", "offline"))
			grant.Claims = map[string]any{"tenant": testTenant, key: restated[key]}
			result, err := completeWith(t, ceremony, authorizeRequest(), grant)
			requireGrantRefusedNaming(t, store, "AuthorizationGrant.Claims", result, err)
			if err != nil && !strings.Contains(err.Error(), strconv.Quote(key)) {
				t.Errorf("отказ не называет ключ %q: %q", key, err.Error())
			}
		})
	}
}

// corruptAccessRecords портит запись сеанса у каждого токена доступа в
// хранилище. Пусто — не портит.
func corruptAccessRecords(store *memoryPorts, corrupt func(*oauthceremony.SessionRecord)) {
	if corrupt == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for signature, grant := range store.access {
		corrupt(&grant.Session)
		store.access[signature] = grant
	}
}

// corruptRefreshRecords — то же у каждого токена обновления.
func corruptRefreshRecords(store *memoryPorts, corrupt func(*oauthceremony.SessionRecord)) {
	if corrupt == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, row := range store.refresh {
		corrupt(&row.grant.Session)
	}
}

// introspectOrRefusal — отказ интроспекции либо её «негоден», названный
// отказом: на порченую запись законного «негоден» нет, это нарушение контракта.
func introspectOrRefusal(ceremony *oauthceremony.Ceremony, token string, kind oauthceremony.TokenKind) error {
	result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
		Token: token, KindHint: kind,
		ClientID: testClientID, ClientSecret: testSecret, AuthMethod: oauthceremony.ClientAuthBasic,
	})
	if err == nil && !result.Active {
		return errors.New("интроспекция назвала " + string(kind) + " негодным вместо отказа")
	}
	return err
}

func revocationRequest(token string, kind oauthceremony.TokenKind) oauthceremony.RevocationRequest {
	return oauthceremony.RevocationRequest{
		Token: token, KindHint: kind,
		ClientID: testClientID, ClientSecret: testSecret, AuthMethod: oauthceremony.ClientAuthBasic,
	}
}

// TestStoredRecordWithoutAGoodLoginContextIsAContractBreach — запись, отданная
// хранилищем без сессии, с уровнем вне словаря, без момента аутентификации или
// с ключом контекста входа в карте, — нарушение контракта порта на КАЖДОМ пути
// выборки: кода (обмен), токена доступа (интроспекция, отзыв), токена
// обновления (интроспекция, оборот, отзыв). Отказ называет поле. Законный
// близнец — та же запись без правки.
//
// Те же пути судятся и у прежних видов порчи записи сеанса — нулевой границы
// и нулевого срока: отказ сборки записи у всех видов один, и на отзыве и
// интроспекции токена обновления движок огрублял его до «временно недоступно»
// и «негоден».
func TestStoredRecordWithoutAGoodLoginContextIsAContractBreach(t *testing.T) {
	corruptions := []struct {
		field   string
		corrupt func(*oauthceremony.SessionRecord)
	}{
		{field: "близнец без правки"},
		{field: "SessionRecord.SessionID", corrupt: func(s *oauthceremony.SessionRecord) { s.SessionID = "" }},
		{field: "SessionRecord.ACR", corrupt: func(s *oauthceremony.SessionRecord) { s.ACR = "0" }},
		{field: "SessionRecord.AuthTime", corrupt: func(s *oauthceremony.SessionRecord) { s.AuthTime = time.Time{} }},
		{field: "SessionRecord.Claims", corrupt: func(s *oauthceremony.SessionRecord) {
			s.Claims = maps.Clone(s.Claims)
			s.Claims["acr"] = "3"
		}},
		// Прежние виды порчи той же записи — нулевые границы и сроки: путь
		// отказа у них тот же, и огрубляться движком он не вправе ни у кого.
		{field: "SessionRecord.NotAfter", corrupt: func(s *oauthceremony.SessionRecord) {
			s.NotAfter = map[oauthceremony.TokenKind]time.Time{oauthceremony.TokenKindRefresh: {}}
		}},
		{field: "SessionRecord.ExpiresAt", corrupt: func(s *oauthceremony.SessionRecord) {
			s.ExpiresAt = maps.Clone(s.ExpiresAt)
			s.ExpiresAt[oauthceremony.TokenKindRefresh] = time.Time{}
		}},
	}

	surfaces := []struct {
		name string
		// run проходит церемонию до записи, портит её и исполняет
		// операцию, выбирающую именно её.
		run func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) error
	}{
		{name: "выборка кода при обмене", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) error {
			code, _ := issueCode(t, ceremony)
			if corrupt != nil {
				store.mu.Lock()
				for _, row := range store.codes {
					corrupt(&row.grant.Session)
				}
				store.mu.Unlock()
			}
			_, err := ceremony.Exchange(context.Background(), codeExchange(code))
			return err
		}},
		{name: "выборка токена доступа при интроспекции", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) error {
			tokens := exchangeCode(t, ceremony)
			corruptAccessRecords(store, corrupt)
			return introspectOrRefusal(ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess)
		}},
		{name: "выборка токена обновления при интроспекции", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) error {
			tokens := exchangeCode(t, ceremony)
			corruptRefreshRecords(store, corrupt)
			return introspectOrRefusal(ceremony, tokens.RefreshToken, oauthceremony.TokenKindRefresh)
		}},
		{name: "выборка токена обновления при обороте", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) error {
			tokens := exchangeCode(t, ceremony)
			corruptRefreshRecords(store, corrupt)
			_, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
			return err
		}},
		// Отзыв: движок спрашивает запись, чтобы узнать идентификатор гранта,
		// и отказ её сборки огрубляет до «временно недоступно». Точный случай
		// обязан доехать до вызывающего и здесь.
		{name: "выборка токена доступа при отзыве", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) error {
			tokens := exchangeCode(t, ceremony)
			corruptAccessRecords(store, corrupt)
			return ceremony.Revoke(context.Background(), revocationRequest(tokens.AccessToken, oauthceremony.TokenKindAccess))
		}},
		{name: "выборка токена обновления при отзыве", run: func(t *testing.T, store *memoryPorts, ceremony *oauthceremony.Ceremony, corrupt func(*oauthceremony.SessionRecord)) error {
			tokens := exchangeCode(t, ceremony)
			corruptRefreshRecords(store, corrupt)
			return ceremony.Revoke(context.Background(), revocationRequest(tokens.RefreshToken, oauthceremony.TokenKindRefresh))
		}},
	}

	for _, surface := range surfaces {
		for _, tc := range corruptions {
			t.Run(surface.name+"/"+tc.field, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ceremony := newTestCeremony(t, store.ports())

				err := surface.run(t, store, ceremony, tc.corrupt)
				if tc.corrupt == nil {
					if err != nil {
						t.Fatalf("близнец: операция отказала: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatalf("запись с порченым %s принята: операция прошла", tc.field)
				}
				var pe *oauthceremony.ProtocolError
				if !errors.Is(err, oauthceremony.ErrPortContract) || !errors.As(err, &pe) {
					t.Fatalf("запись с порченым %s отвергнута случаем %v, ожидался %v: %v",
						tc.field, oauthceremony.CodeOf(err), oauthceremony.CodePortContract, err)
				}
				if !strings.Contains(pe.Debug, tc.field) {
					t.Errorf("отказ не называет %s: %q", tc.field, pe.Debug)
				}
			})
		}
	}
}
