// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// access_issuer_test.go — токен доступа выпускает ПОРТ службы, а не движок.
//
// Предмет — три утверждения о границе с портом выпуска:
//
//   - токен доступа в ответе обмена — ровно то значение, которое вернул порт,
//     а `expires_in` — срок ЭТОГО выпуска (exp минус момент выпуска), тот же,
//     что записан у гранта в хранилище;
//   - выпуск, нарушающий контракт порта, не уезжает клиенту;
//   - отказ опознания предъявленного токена — отказ операции, а не «токен
//     негоден» и не «отозвано».
package oauthceremony_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

// requireIssuedAs утверждает, что n-й выпуск порта и есть то, что ушло
// клиентом в ответе got: значение токена, срок в ответе и срок у записи.
func requireIssuedAs(t *testing.T, step string, store *memoryPorts, ceremony *oauthceremony.Ceremony,
	got oauthceremony.TokenResult, n int) oauthceremony.IssuedAccessToken {
	t.Helper()

	issuances := store.issuer.issuances()
	if len(issuances) != n {
		t.Fatalf("%s: у порта выпуска %d выпусков, ожидалось %d — токен доступа выпустил не порт", step, len(issuances), n)
	}
	issued := issuances[n-1].issued

	if got.AccessToken != issued.Token {
		t.Errorf("%s: токен доступа в ответе %q, а порт выпустил %q", step, got.AccessToken, issued.Token)
	}
	if want := issued.ExpiresAt.Sub(issued.IssuedAt); got.ExpiresIn != want {
		t.Errorf("%s: expires_in %s, а срок выпуска (exp − момент выпуска) %s", step, got.ExpiresIn, want)
	}

	stored := introspect(t, ceremony, got.AccessToken, oauthceremony.TokenKindAccess)
	if !stored.Active {
		t.Fatalf("%s: только что выпущенный токен доступа назван негодным", step)
	}
	if !stored.ExpiresAt.Equal(issued.ExpiresAt) {
		t.Errorf("%s: у записи гранта срок %s, а в выпуске %s — два источника одного срока",
			step, stored.ExpiresAt.Format(time.RFC3339Nano), issued.ExpiresAt.Format(time.RFC3339Nano))
	}

	store.mu.Lock()
	_, keyed := store.access[issued.ID]
	store.mu.Unlock()
	if !keyed {
		t.Errorf("%s: грант токена доступа положен не под его идентификатором (jti) %q", step, issued.ID)
	}
	return issued
}

// TestAccessTokenInTheResponseIsTheIssuersAndItsLifetime — при остановленных
// часах порта срок в ответе равен сроку выпуска ТОЧНО.
//
// Потолок подставки (7 мин 13 с, затем 5 мин) нарочно отличен от срока
// настроек (testAccessLifespan): ответ, берущий срок из настроек, назвал бы
// настройки, и равенство покраснело бы. Проверяются оба пути выпуска — обмен
// кода и оборот токена обновления: у движка срок в ответе считают два разных
// обработчика.
//
// Часы порта останавливаются на ПЕРВОМ выпуске и в целой секунде — так, как
// момент выпуска лежит в токене (`iat`). Остановленные раньше вызова, они
// назвали бы моментом выпуска миг до начала вызова, а это нарушение контракта
// порта (TestAnIssuanceBreakingThePortContractIsRefused).
func TestAccessTokenInTheResponseIsTheIssuersAndItsLifetime(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	var clock time.Time
	store.issuer.now = func() time.Time {
		if clock.IsZero() {
			clock = time.Now().UTC().Truncate(time.Second)
		}
		return clock
	}
	store.issuer.ceiling = 7*time.Minute + 13*time.Second
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	first, err := ceremony.Exchange(context.Background(), codeExchange(code))
	if err != nil {
		t.Fatalf("обмен кода отказал: %v", err)
	}
	issued := requireIssuedAs(t, "обмен кода", store, ceremony, first, 1)
	if !issued.IssuedAt.Equal(clock) || issued.ExpiresAt.Sub(issued.IssuedAt) != 7*time.Minute+13*time.Second {
		t.Fatalf("ФИКСТУРА: подставка выпустила не то, что ей велено: %s … %s", issued.IssuedAt, issued.ExpiresAt)
	}

	clock = clock.Add(time.Minute)
	store.issuer.ceiling = 5 * time.Minute
	second, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("оборот отказал: %v", err)
	}
	rotated := requireIssuedAs(t, "оборот", store, ceremony, second, 2)
	if second.ExpiresIn != 5*time.Minute {
		t.Errorf("оборот: expires_in %s, срок выпуска — пять минут", second.ExpiresIn)
	}

	store.mu.Lock()
	row, found := store.refresh[opaqueDigest(second.RefreshToken)]
	store.mu.Unlock()
	if !found || row.accessSig != rotated.ID {
		t.Errorf("токен обновления оборота связан не с jti выпущенного с ним токена доступа %q: %+v", rotated.ID, row)
	}
}

// TestAnIssuanceBreakingThePortContractIsRefused — выпуск, в котором порт
// нарушил контракт, не уезжает клиентом и не ложится в хранилище.
//
// Каждый отрицательный случай меняет в выпуске РОВНО ОДИН факт против своего
// законного близнеца и называет ПРИЧИНУ отказа (why — часть подробностей
// отказа), а не только его случай: иначе отказ по соседней причине выдал бы
// себя за этот. Случай «срок ровно на границе» — законный близнец случая «на
// наносекунду позже»; случай «момент выпуска — начало секунды вызова» —
// законный близнец случаев о моменте выпуска.
func TestAnIssuanceBreakingThePortContractIsRefused(t *testing.T) {
	boundOf := func(grant oauthceremony.GrantRecord) time.Time {
		return grant.Session.ExpiresAt[oauthceremony.TokenKindAccess]
	}
	// callSecond — начало идущей секунды: выпуск исполняется внутри вызова,
	// и это начало секунды, в которой церемония позвала порт, или позже.
	callSecond := func() time.Time { return time.Now().UTC().Truncate(time.Second) }
	for _, tc := range []struct {
		name    string
		reshape func(grant oauthceremony.GrantRecord, issued *oauthceremony.IssuedAccessToken)
		// why — причина отказа в подробностях. Пусто — выпуск законен.
		why string
	}{
		{name: "срок ровно на границе церемонии",
			reshape: func(g oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.ExpiresAt = boundOf(g) }},
		{name: "срок позже границы церемонии", why: "later than the bound",
			reshape: func(g oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) {
				i.ExpiresAt = boundOf(g).Add(time.Nanosecond)
			}},
		{name: "без идентификатора", why: "without its identifier",
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.ID = "" }},
		{name: "без значения токена", why: "without its value",
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.Token = "" }},
		{name: "момент выпуска не назван", why: "without its issuance instant",
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.IssuedAt = time.Time{} }},
		{name: "срок не позже момента выпуска", why: "not after its issuance",
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.ExpiresAt = i.IssuedAt }},
		// Момент выпуска — не раньше начала секунды, в которой церемония
		// позвала порт. Так выпускает подписант, у которого `iat` и `exp` —
		// целые секунды, и срок в ответе у него бывает на секунду длиннее
		// срока настроек: предел по сроку настроек отверг бы этот выпуск.
		{name: "момент выпуска — начало секунды вызова, срок — граница",
			reshape: func(g oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) {
				i.IssuedAt = callSecond()
				i.ExpiresAt = boundOf(g)
			}},
		{name: "момент выпуска на две секунды раньше вызова, срок — граница", why: "before the second",
			reshape: func(g oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) {
				i.IssuedAt = callSecond().Add(-2 * time.Second)
				i.ExpiresAt = boundOf(g)
			}},
		{name: "момент выпуска на двое суток раньше вызова, срок — граница", why: "before the second",
			reshape: func(g oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) {
				i.IssuedAt = callSecond().Add(-48 * time.Hour)
				i.ExpiresAt = boundOf(g)
			}},
		// Срок короче границы. Предел по сроку настроек случая ниже не
		// ловит: срок в ответе — пятнадцать минут, короче двадцати в
		// настройках, а токену жить пять.
		{name: "момент выпуска — начало секунды вызова, срок — пять минут",
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) {
				i.IssuedAt = callSecond()
				i.ExpiresAt = i.IssuedAt.Add(5 * time.Minute)
			}},
		{name: "момент выпуска на десять минут раньше вызова, срок — пять минут", why: "before the second",
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) {
				i.IssuedAt = callSecond()
				i.ExpiresAt = i.IssuedAt.Add(5 * time.Minute)
				i.IssuedAt = i.IssuedAt.Add(-10 * time.Minute)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			store.issuer.reshape = tc.reshape
			ceremony := newTestCeremony(t, store.ports())

			code, _ := issueCode(t, ceremony)
			tokens, err := ceremony.Exchange(context.Background(), codeExchange(code))

			store.mu.Lock()
			storedAccess := len(store.access)
			store.mu.Unlock()
			if tc.why == "" {
				if err != nil {
					t.Fatalf("законный выпуск отвергнут: %v", err)
				}
				requireIssuedAs(t, tc.name, store, ceremony, tokens, 1)
				return
			}
			if err == nil {
				t.Fatalf("выпуск с нарушенным контрактом уехал клиентом: %+v", tokens)
			}
			if !errors.Is(err, oauthceremony.ErrPortContract) {
				t.Errorf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
			}
			var failure *oauthceremony.ProtocolError
			if errors.As(err, &failure) && !strings.Contains(failure.Debug, tc.why) {
				t.Errorf("отказ не называет своей причины %q: %q", tc.why, failure.Debug)
			}
			if storedAccess != 0 {
				t.Errorf("выпуск с нарушенным контрактом положен в хранилище: записей токена доступа %d", storedAccess)
			}
		})
	}
}

// TestFailedIssuanceFailsTheExchange — порт выпуска отказал: обмен отказывает
// ЭТИМ отказом, клиенту не уезжает ничего и в хранилище не ложится ничего.
// Законный близнец — следующий код того же клиента при здоровом порте: обмен
// проходит. Тот же код второй раз не предъявляется: запись PKCE снята первым
// предъявлением, и второе — повтор (code_replay_test.go), а не предмет этой
// пробы.
func TestFailedIssuanceFailsTheExchange(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())
	code, _ := issueCode(t, ceremony)

	store.issuer.setIssueFailure(context.DeadlineExceeded)
	tokens, err := ceremony.Exchange(context.Background(), codeExchange(code))
	if !errors.Is(err, oauthceremony.ErrPortDeadline) {
		t.Fatalf("обмен при отказе выпуска: случай %v, ожидался %v; ответ %+v",
			oauthceremony.CodeOf(err), oauthceremony.CodePortDeadline, tokens)
	}
	requireDescription(t, "обмен при отказе выпуска", err, textPortDeadline)
	store.mu.Lock()
	storedAccess, storedRefresh := len(store.access), len(store.refresh)
	store.mu.Unlock()
	if storedAccess != 0 || storedRefresh != 0 {
		t.Errorf("при отказе выпуска в хранилище положено: токенов доступа %d, обновления %d", storedAccess, storedRefresh)
	}

	store.issuer.setIssueFailure(nil)
	next, _ := issueCode(t, ceremony)
	tokens, err = ceremony.Exchange(context.Background(), codeExchange(next))
	if err != nil {
		t.Fatalf("близнец: обмен следующего кода при здоровом порте отказал: %v", err)
	}
	requireIssuedAs(t, "близнец", store, ceremony, tokens, 1)
}

// Тексты отказа портов — те, что уезжают полем Description. Порт выпуска —
// не хранилище, и текст отказа называет порт, а не хранилище.
const (
	textPortFailed   = "A port of the authorization server failed."
	textPortDeadline = "A port call did not finish in time."
	textPortContract = "A port of the authorization server broke its contract."
)

// requireDescription утверждает текст отказа, уезжающий полем Description.
func requireDescription(t *testing.T, step string, err error, want string) {
	t.Helper()

	var failure *oauthceremony.ProtocolError
	if !errors.As(err, &failure) {
		t.Fatalf("%s: отказ не нашего вида: %v", step, err)
	}
	if failure.Description != want {
		t.Errorf("%s: текст отказа %q, ожидался %q", step, failure.Description, want)
	}
}

// requireNoPresentedValue утверждает, что предъявленное значение не попало ни
// в один текст отказа: ни в тот, что уезжает (Error, Description, Hint), ни в
// тот, что ложится в журнал (Debug).
func requireNoPresentedValue(t *testing.T, step string, err error, presented string) {
	t.Helper()

	var failure *oauthceremony.ProtocolError
	if !errors.As(err, &failure) {
		t.Fatalf("%s: отказ не нашего вида: %v", step, err)
	}
	for field, text := range map[string]string{
		"Error":       err.Error(),
		"Description": failure.Description,
		"Hint":        failure.Hint,
		"Debug":       failure.Debug,
	} {
		if strings.Contains(text, presented) {
			t.Errorf("%s: предъявленный токен попал в %s отказа", step, field)
		}
	}
	if !strings.Contains(failure.Debug, "AccessTokenIssuer.IdentifyAccessToken") {
		t.Errorf("%s: подробности отказа не называют вызова порта: %q", step, failure.Debug)
	}
}

// TestFailedIdentificationFailsTheOperation — предъявленный токен порт не
// сумел опознать: это отказ операции, а не ответ.
//
// Проглоти церемония отказ, интроспекция ответила бы «негоден» на годный
// токен, а отзыв — «отозвано» на токен, который жив: RFC 7009 §2.2 велит
// отвечать успехом на отзыв несуществующего, и отказ проверяющего стал бы
// неотличим от этого. Законный близнец каждого случая — тот же токен при
// здоровом порте: интроспекция называет его годным, отзыв снимает.
//
// Порт получает предъявительский токен, и его текст отказа может нести это
// значение, хотя контракт это запрещает: каждый случай кладёт значение в
// текст отказа порта, и ни один текст отказа церемонии его не несёт.
func TestFailedIdentificationFailsTheOperation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failure  func(presented string) error
		want     error
		wantText string
	}{
		{name: "порт отказал",
			failure: func(presented string) error {
				return errors.New("identify " + presented + ": the key set of the access service is unreachable")
			},
			want: oauthceremony.ErrServerError, wantText: textPortFailed},
		{name: "срок вызова истёк",
			failure: func(presented string) error {
				return fmt.Errorf("identify %s: %w", presented, context.DeadlineExceeded)
			},
			want: oauthceremony.ErrPortDeadline, wantText: textPortDeadline},
		{name: "порт назвал отказ сам",
			failure: func(presented string) error {
				return &oauthceremony.ProtocolError{
					Code:        oauthceremony.CodeTemporarilyUnavailable,
					Description: "The signer could not check " + presented + ".",
					Hint:        "Retry " + presented + " later.",
					Debug:       "key set fetch for " + presented + " timed out",
				}
			},
			want: oauthceremony.ErrTemporarilyUnavailable, wantText: "The signer could not check [presented token]."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			tokens := exchangeCode(t, ceremony)

			if !introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Active {
				t.Fatal("близнец: при здоровом порте выданный токен назван негодным")
			}

			store.issuer.setIdentifyFailure(tc.failure(tokens.AccessToken))
			result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
				Token:        tokens.AccessToken,
				KindHint:     oauthceremony.TokenKindAccess,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			if err == nil {
				t.Errorf("интроспекция при отказе опознания ответила, а не отказала: %+v", result)
			} else {
				if !errors.Is(err, tc.want) {
					t.Errorf("интроспекция: случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodeOf(tc.want))
				}
				requireDescription(t, "интроспекция", err, tc.wantText)
				requireNoPresentedValue(t, "интроспекция", err, tokens.AccessToken)
			}

			revokeErr := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
				Token:        tokens.AccessToken,
				KindHint:     oauthceremony.TokenKindAccess,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			if revokeErr == nil {
				t.Error("отзыв при отказе опознания ответил успехом — а токен не снят")
			} else {
				if !errors.Is(revokeErr, tc.want) {
					t.Errorf("отзыв: случай %v, ожидался %v", oauthceremony.CodeOf(revokeErr), oauthceremony.CodeOf(tc.want))
				}
				requireDescription(t, "отзыв", revokeErr, tc.wantText)
				requireNoPresentedValue(t, "отзыв", revokeErr, tokens.AccessToken)
			}

			store.issuer.setIdentifyFailure(nil)
			if !introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Active {
				t.Error("токен, отзыв которого отказал, снят — отказ отзыва был бы ложью")
			}
			if err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
				Token:        tokens.AccessToken,
				KindHint:     oauthceremony.TokenKindAccess,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			}); err != nil {
				t.Fatalf("близнец: отзыв при здоровом порте отказал: %v", err)
			}
			if introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Active {
				t.Error("близнец: отозванный при здоровом порте токен назван годным")
			}
		})
	}
}

// expireAccessRecord переводит срок токена доступа в записи гранта в прошлое:
// так токен истекает, пока проба не ждёт его срока.
func expireAccessRecord(t *testing.T, store *memoryPorts, jti string) {
	t.Helper()

	store.mu.Lock()
	defer store.mu.Unlock()
	grant, found := store.access[jti]
	if !found {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: записи гранта под jti %q нет — истекать нечему", jti)
	}
	grant.Session.ExpiresAt[oauthceremony.TokenKindAccess] = time.Now().UTC().Add(-time.Minute)
}

// TestExpiredAccessTokenHasOneNamedOutcome — подлинный, но истёкший токен
// доступа порт опознаёт как любой свой (jti, nil), а срок судит церемония по
// записи гранта: интроспекция отвечает `active: false` (RFC 7662 §2.2), отзыв
// снимает семейство (RFC 7009 §2.1) — так же, как по живому токену.
//
// Порт, который всё же судит срок или отвечает иным вердиктом о токене,
// нарушает контракт, и церемония отвечает ErrPortContract, а не «негоден»:
// прочти она такой отказ как «не наш», отзыв по истёкшему токену доступа
// ответил бы успехом, оставив живым токен обновления его семейства. Близнец
// отказа — «не наш» (ErrGrantNotFound): `active: false` и успех без действия.
func TestExpiredAccessTokenHasOneNamedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identify error
		// breach — порт нарушил контракт: обе операции отказывают, семейство
		// живо. Иначе — ответ: `active: false` и успех отзыва.
		breach bool
		// familyRevoked — отзыв снял семейство.
		familyRevoked bool
	}{
		{name: "порт срока не судит (исход 1)", familyRevoked: true},
		{name: "порт ответил «не наш» (исход 2)", identify: oauthceremony.ErrGrantNotFound},
		{name: "порт сам судит срок", identify: oauthceremony.ErrTokenExpired, breach: true},
		{name: "порт ответил «неактивен»", identify: oauthceremony.ErrInactiveToken, breach: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			tokens := exchangeCode(t, ceremony)
			family := grantOf(t, ceremony, tokens.AccessToken)
			issuances := store.issuer.issuances()
			expireAccessRecord(t, store, issuances[len(issuances)-1].issued.ID)
			store.issuer.setIdentifyFailure(tc.identify)

			result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
				Token:        tokens.AccessToken,
				KindHint:     oauthceremony.TokenKindAccess,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			switch {
			case tc.breach && !errors.Is(err, oauthceremony.ErrPortContract):
				t.Errorf("интроспекция: случай %v, ожидался %v; ответ %+v",
					oauthceremony.CodeOf(err), oauthceremony.CodePortContract, result)
			case tc.breach:
				requireDescription(t, "интроспекция", err, textPortContract)
			case err != nil:
				t.Errorf("интроспекция истёкшего токена отказала вместо active=false: %v", err)
			case result.Active:
				t.Errorf("истёкший токен назван годным: %+v", result)
			}

			revokeErr := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
				Token:        tokens.AccessToken,
				KindHint:     oauthceremony.TokenKindAccess,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			switch {
			case tc.breach && !errors.Is(revokeErr, oauthceremony.ErrPortContract):
				t.Errorf("отзыв: случай %v, ожидался %v", oauthceremony.CodeOf(revokeErr), oauthceremony.CodePortContract)
			case !tc.breach && revokeErr != nil:
				t.Errorf("отзыв истёкшего токена отказал вместо успеха: %v", revokeErr)
			}

			if tc.familyRevoked {
				requireRevokedFor(t, store, family, oauthceremony.RevocationClientRevoke)
			} else {
				requireNotRevoked(t, store, family)
			}
		})
	}
}

// TestIdentificationWithoutAnIdentifierIsAPortContractBreach — порт опознал
// токен, не назвав идентификатора: искать грант не под чем, и это нарушение
// контракта, а не «токен негоден». Близнец — TestFailedIdentificationFailsTheOperation
// при здоровом порте.
func TestIdentificationWithoutAnIdentifierIsAPortContractBreach(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())
	tokens := exchangeCode(t, ceremony)

	store.issuer.mu.Lock()
	store.issuer.identifyEmpty = true
	store.issuer.mu.Unlock()

	_, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
		Token:        tokens.AccessToken,
		KindHint:     oauthceremony.TokenKindAccess,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
	})
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v: %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract, err)
	}
}

// TestTamperedAccessTokenIsInactive — предъявленное значение, которого порт
// не выпускал, — негодный токен, даже если выпущенный рядом жив. Близнец —
// тот же токен без правки: годен.
func TestTamperedAccessTokenIsInactive(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())
	tokens := exchangeCode(t, ceremony)

	if !introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Active {
		t.Fatal("близнец: выданный токен назван негодным")
	}
	if result := introspect(t, ceremony, tokens.AccessToken+"x", oauthceremony.TokenKindAccess); result.Active {
		t.Errorf("исправленный токен назван годным: %+v", result)
	}
}

// TestNewRefusesAnAccessLifespanAboveTheTokenCeiling — срок токена доступа
// не длиннее потолка подписанта платформы (tokenpolicy.MaxTokenTTL): токен
// подписывает служба, и её подписант выше потолка не выпустит. Выпуск короче
// границы законен (контракт AccessTokenIssuer), поэтому церемония со сроком
// длиннее обменивала бы исправно, но срок настроек не исполнялся бы ни на
// одном выпуске — настройка лгала бы молча, и заметить это было бы негде.
func TestNewRefusesAnAccessLifespanAboveTheTokenCeiling(t *testing.T) {
	store := newMemoryPorts()
	withLifespan := func(d time.Duration) func(*oauthceremony.Config) {
		return func(cfg *oauthceremony.Config) { cfg.AccessTokenLifespan = d }
	}

	newTestCeremony(t, store.ports(), withLifespan(tokenpolicy.MaxTokenTTL))

	_, err := oauthceremony.New(newTestCeremonyConfig(withLifespan(tokenpolicy.MaxTokenTTL+time.Nanosecond)), store.ports())
	if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
		t.Fatalf("срок токена доступа выше потолка принят: %v", err)
	}
	for _, named := range []string{"Config.AccessTokenLifespan", tokenpolicy.MaxTokenTTL.String()} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("отказ не называет %q: %v", named, err)
		}
	}
}
