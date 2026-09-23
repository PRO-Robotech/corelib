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
func TestAccessTokenInTheResponseIsTheIssuersAndItsLifetime(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	clock := time.Now().UTC().Truncate(time.Second)
	store.issuer.now = func() time.Time { return clock }
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
// Каждый отрицательный случай меняет в выпуске РОВНО ОДИН факт против
// законного (TestAccessTokenInTheResponseIsTheIssuersAndItsLifetime); случай
// «срок ровно на границе» — законный близнец случая «на наносекунду позже».
func TestAnIssuanceBreakingThePortContractIsRefused(t *testing.T) {
	boundOf := func(grant oauthceremony.GrantRecord) time.Time {
		return grant.Session.ExpiresAt[oauthceremony.TokenKindAccess]
	}
	for _, tc := range []struct {
		name    string
		reshape func(grant oauthceremony.GrantRecord, issued *oauthceremony.IssuedAccessToken)
		refused bool
	}{
		{name: "срок ровно на границе церемонии", refused: false,
			reshape: func(g oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.ExpiresAt = boundOf(g) }},
		{name: "срок позже границы церемонии", refused: true,
			reshape: func(g oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) {
				i.ExpiresAt = boundOf(g).Add(time.Nanosecond)
			}},
		{name: "без идентификатора", refused: true,
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.ID = "" }},
		{name: "без значения токена", refused: true,
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.Token = "" }},
		{name: "момент выпуска не назван", refused: true,
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.IssuedAt = time.Time{} }},
		{name: "срок не позже момента выпуска", refused: true,
			reshape: func(_ oauthceremony.GrantRecord, i *oauthceremony.IssuedAccessToken) { i.ExpiresAt = i.IssuedAt }},
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
			if !tc.refused {
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
			if storedAccess != 0 {
				t.Errorf("выпуск с нарушенным контрактом положен в хранилище: записей токена доступа %d", storedAccess)
			}
			t.Logf("отказ: %v", err)
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
	if tokens, err := ceremony.Exchange(context.Background(), codeExchange(code)); !errors.Is(err, oauthceremony.ErrPortDeadline) {
		t.Fatalf("обмен при отказе выпуска: случай %v, ожидался %v; ответ %+v",
			oauthceremony.CodeOf(err), oauthceremony.CodePortDeadline, tokens)
	}
	store.mu.Lock()
	storedAccess, storedRefresh := len(store.access), len(store.refresh)
	store.mu.Unlock()
	if storedAccess != 0 || storedRefresh != 0 {
		t.Errorf("при отказе выпуска в хранилище положено: токенов доступа %d, обновления %d", storedAccess, storedRefresh)
	}

	store.issuer.setIssueFailure(nil)
	next, _ := issueCode(t, ceremony)
	tokens, err := ceremony.Exchange(context.Background(), codeExchange(next))
	if err != nil {
		t.Fatalf("близнец: обмен следующего кода при здоровом порте отказал: %v", err)
	}
	requireIssuedAs(t, "близнец", store, ceremony, tokens, 1)
}

// TestFailedIdentificationFailsTheOperation — предъявленный токен порт не
// сумел опознать: это отказ операции, а не ответ.
//
// Проглоти церемония отказ, интроспекция ответила бы «негоден» на годный
// токен, а отзыв — «отозвано» на токен, который жив: RFC 7009 §2.2 велит
// отвечать успехом на отзыв несуществующего, и отказ проверяющего стал бы
// неотличим от этого. Законный близнец каждого случая — тот же токен при
// здоровом порте: интроспекция называет его годным, отзыв снимает.
func TestFailedIdentificationFailsTheOperation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		want    error
	}{
		{name: "порт отказал", failure: errors.New("the key set of the access service is unreachable"), want: nil},
		{name: "срок вызова истёк", failure: context.DeadlineExceeded, want: oauthceremony.ErrPortDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			tokens := exchangeCode(t, ceremony)

			if !introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Active {
				t.Fatal("близнец: при здоровом порте выданный токен назван негодным")
			}

			store.issuer.setIdentifyFailure(tc.failure)
			result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
				Token:        tokens.AccessToken,
				KindHint:     oauthceremony.TokenKindAccess,
				ClientID:     testClientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			if err == nil {
				t.Errorf("интроспекция при отказе опознания ответила, а не отказала: %+v", result)
			} else if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("интроспекция: случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodeOf(tc.want))
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
			} else if tc.want != nil && !errors.Is(revokeErr, tc.want) {
				t.Errorf("отзыв: случай %v, ожидался %v", oauthceremony.CodeOf(revokeErr), oauthceremony.CodeOf(tc.want))
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
// подписывает служба, и её подписант выше потолка не выпустит — церемония,
// собранная со сроком длиннее, отказывала бы на каждом обмене.
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
