// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// proof_key_test.go — пробы доказательства владения ключом (PKCE, RFC 7636)
// как ПОЛЕЙ ЗАПИСИ КОДА авторизации.
//
// Привязка кода к доказательству — вызов (`code_challenge`) и метод — лежит в
// той же записи, что и код: у службы это одна строка (`authorization_codes`), и
// второго хранилища под подписью кода в пути нет. Привязка обязательна, метод —
// только S256: запрос кода без неё отвергается ДО того, как код выпущен, а
// запись кода без неё — нарушение контракта порта.
package oauthceremony_test

import (
	"context"
	"errors"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// otherVerifier — второе годное доказательство: 43 знака из алфавита RFC 7636
// §4.1, не совпадающее с testVerifier.
const otherVerifier = "otherVerifier-otherVerifier-otherVerifier-0"

// accessTokensStored — сколько токенов доступа положено в хранилище.
func accessTokensStored(store *memoryPorts) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.access)
}

// codesStored — сколько кодов положено в хранилище.
func codesStored(store *memoryPorts) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.codes)
}

// TestProofKeyDecidesTheExchange — верное доказательство выдаёт токены, неверное
// отвергается случаем `invalid_grant` и не выдаёт ничего.
func TestProofKeyDecidesTheExchange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier string
		issued   bool
	}{
		{name: "верное доказательство", verifier: testVerifier, issued: true},
		{name: "неверное доказательство", verifier: otherVerifier, issued: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			code, _ := issueCode(t, ceremony)
			req := codeExchange(code)
			req.CodeVerifier = tc.verifier
			tokens, err := ceremony.Exchange(context.Background(), req)

			if tc.issued {
				if err != nil {
					t.Fatalf("обмен с верным доказательством отказал: %v", err)
				}
				if tokens.AccessToken == "" {
					t.Fatal("обмен с верным доказательством не выдал токена доступа")
				}
				return
			}
			if err == nil {
				t.Fatal("обмен с неверным доказательством выдал токены")
			}
			if got := oauthceremony.CodeOf(err); got != oauthceremony.CodeInvalidGrant {
				t.Errorf("неверное доказательство отвергнуто случаем %v, ожидался %v", got, oauthceremony.CodeInvalidGrant)
			}
			if wire := oauthceremony.CodeOf(err).WireCode(); wire != "invalid_grant" {
				t.Errorf("на проводе неверное доказательство названо %q, ожидался \"invalid_grant\"", wire)
			}
			if n := accessTokensStored(store); n != 0 {
				t.Errorf("обмен с неверным доказательством положил токенов доступа %d шт", n)
			}
		})
	}
}

// TestAuthorizationRequestWithoutAnS256ProofKeyIsRefused — запрос кода без
// привязки S256 отвергается случаем `invalid_request` уже точкой авторизации,
// до согласия, и отказ доставляется клиенту перенаправлением. Намерение,
// которое служба всё же закрывает выдачей, кода не выпускает: в хранилище не
// уезжает ни одной записи.
//
// Близнец — первая строка таблицы: S256 и вызов верной формы.
func TestAuthorizationRequestWithoutAnS256ProofKeyIsRefused(t *testing.T) {
	good := proofKeyChallenge(testVerifier)
	for _, tc := range []struct {
		name       string
		additional map[string][]string
		refused    bool
	}{
		{name: "S256 и вызов верной формы", additional: map[string][]string{
			"code_challenge": {good}, "code_challenge_method": {"S256"}}},
		{name: "без вызова и метода", additional: nil, refused: true},
		{name: "без вызова", additional: map[string][]string{
			"code_challenge_method": {"S256"}}, refused: true},
		{name: "пустой вызов", additional: map[string][]string{
			"code_challenge": {""}, "code_challenge_method": {"S256"}}, refused: true},
		{name: "метод plain", additional: map[string][]string{
			"code_challenge": {good}, "code_challenge_method": {"plain"}}, refused: true},
		{name: "метод не назван", additional: map[string][]string{
			"code_challenge": {good}}, refused: true},
		{name: "неизвестный метод", additional: map[string][]string{
			"code_challenge": {good}, "code_challenge_method": {"S512"}}, refused: true},
		{name: "метод в другом регистре", additional: map[string][]string{
			"code_challenge": {good}, "code_challenge_method": {"s256"}}, refused: true},
		{name: "вызов короче свёртки", additional: map[string][]string{
			"code_challenge": {good[:42]}, "code_challenge_method": {"S256"}}, refused: true},
		{name: "вызов длиннее свёртки", additional: map[string][]string{
			"code_challenge": {good + "A"}, "code_challenge_method": {"S256"}}, refused: true},
		{name: "вызов вне алфавита base64url", additional: map[string][]string{
			"code_challenge": {good[:42] + "."}, "code_challenge_method": {"S256"}}, refused: true},
		{name: "два вызова", additional: map[string][]string{
			"code_challenge": {good, good}, "code_challenge_method": {"S256"}}, refused: true},
		{name: "два метода", additional: map[string][]string{
			"code_challenge": {good}, "code_challenge_method": {"S256", "S256"}}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			req := authorizeRequest()
			req.Additional = tc.additional
			intent, authorizeErr := ceremony.Authorize(context.Background(), req)
			if !intent.Issued() {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: Authorize не выдал намерения (%v) — отказ некуда доставить", authorizeErr)
			}

			_, completeErr := ceremony.CompleteAuthorization(context.Background(), intent, loggedIn(oauthceremony.AuthorizationGrant{
				Subject:       testSubject,
				GrantedScopes: []string{"openid", "offline"},
			}))

			if !tc.refused {
				if authorizeErr != nil {
					t.Fatalf("Authorize отверг запрос с привязкой S256: %v", authorizeErr)
				}
				if completeErr != nil {
					t.Fatalf("CompleteAuthorization отверг запрос с привязкой S256: %v", completeErr)
				}
				if n := codesStored(store); n != 1 {
					t.Errorf("выдача положила кодов %d шт, ожидался 1", n)
				}
				return
			}

			if authorizeErr == nil {
				t.Errorf("Authorize принял запрос (%s): <nil>", tc.name)
			} else {
				if got := oauthceremony.CodeOf(authorizeErr); got != oauthceremony.CodeInvalidRequest {
					t.Errorf("Authorize отверг запрос случаем %v, ожидался %v: %v", got, oauthceremony.CodeInvalidRequest, authorizeErr)
				}
				denied, err := ceremony.DenyAuthorization(context.Background(), intent, authorizeErr)
				if err != nil {
					t.Fatalf("отказ точки авторизации не собран: %v", err)
				}
				if got := denied.Parameters["error"]; len(got) != 1 || got[0] != "invalid_request" {
					t.Errorf("клиенту доставлен отказ %v, ожидался [invalid_request]", got)
				}
			}
			if completeErr == nil {
				t.Errorf("CompleteAuthorization выпустил код по запросу (%s): <nil>", tc.name)
			} else if got := oauthceremony.CodeOf(completeErr); got != oauthceremony.CodeInvalidRequest {
				t.Errorf("CompleteAuthorization отверг запрос случаем %v, ожидался %v: %v", got, oauthceremony.CodeInvalidRequest, completeErr)
			}
			if n := codesStored(store); n != 0 {
				t.Errorf("по запросу (%s) в хранилище положено кодов %d шт, ожидалось 0", tc.name, n)
			}
		})
	}
}

// TestIssuedCodeIsOneRecordThatCarriesItsProofKey — выданный код — ОДНА запись
// хранилища, и вызов с методом — её поля, а не второе место в протокольных
// полях запроса.
func TestIssuedCodeIsOneRecordThatCarriesItsProofKey(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	issueCode(t, ceremony)
	stored := store.storedCode(t)

	if n := store.recordsUnder(stored.signature); n != 1 {
		t.Errorf("под подписью выданного кода записей %d шт, ожидалась 1 — запись кода", n)
	}
	if want := proofKeyChallenge(testVerifier); stored.challenge != want {
		t.Errorf("вызов в записи кода %q, ожидался %q", stored.challenge, want)
	}
	if stored.method != "S256" {
		t.Errorf("метод в записи кода %q, ожидался \"S256\"", stored.method)
	}
	for _, key := range []string{"code_challenge", "code_challenge_method"} {
		if _, twice := stored.form[key]; twice {
			t.Errorf("поле %s лежит и в протокольных полях записи кода — у значения два места", key)
		}
	}
}

// TestExchangeChecksTheProofKeyOfTheCodeRecord — доказательство сверяется с
// привязкой, которую несёт ЗАПИСЬ КОДА в миг обмена: переписанная в записи
// привязка решает обмен, прежняя — нет.
func TestExchangeChecksTheProofKeyOfTheCodeRecord(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier string
		issued   bool
	}{
		{name: "доказательство под привязку записи", verifier: otherVerifier, issued: true},
		{name: "доказательство под прежнюю привязку", verifier: testVerifier, issued: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			code, _ := issueCode(t, ceremony)
			store.rebindStoredCode(t, proofKeyChallenge(otherVerifier), "S256")

			req := codeExchange(code)
			req.CodeVerifier = tc.verifier
			tokens, err := ceremony.Exchange(context.Background(), req)

			if tc.issued {
				if err != nil || tokens.AccessToken == "" {
					t.Fatalf("обмен по доказательству привязки из записи кода не выдал токенов: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("обмен прошёл по доказательству, которого запись кода не несёт, — сверка шла не с записью кода")
			}
			if wire := oauthceremony.CodeOf(err).WireCode(); wire != "invalid_grant" {
				t.Errorf("на проводе отказ назван %q, ожидался \"invalid_grant\"", wire)
			}
		})
	}
}

// TestCodeRecordWithoutAnS256ProofKeyIsAContractBreach — запись кода, вернувшаяся
// из хранилища без годной привязки S256, — порча записи службой, а не «PKCE не
// было»: обмен отвергается как нарушение контракта порта и не выдаёт ничего.
//
// Близнец — TestExchangeChecksTheProofKeyOfTheCodeRecord, первая строка: та же
// переписанная привязка, но годная.
func TestCodeRecordWithoutAnS256ProofKeyIsAContractBreach(t *testing.T) {
	good := proofKeyChallenge(testVerifier)
	for _, tc := range []struct {
		name      string
		challenge string
		method    string
	}{
		{name: "без вызова", challenge: "", method: "S256"},
		{name: "без метода", challenge: good, method: ""},
		{name: "метод plain", challenge: testVerifier, method: "plain"},
		{name: "вызов не той формы", challenge: good[:42], method: "S256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			code, _ := issueCode(t, ceremony)
			store.rebindStoredCode(t, tc.challenge, tc.method)

			_, err := ceremony.Exchange(context.Background(), codeExchange(code))
			if !errors.Is(err, oauthceremony.ErrPortContract) {
				t.Errorf("запись кода (%s) принята: случай %v (%v), ожидался %v",
					tc.name, oauthceremony.CodeOf(err), err, oauthceremony.CodePortContract)
			}
			if n := accessTokensStored(store); n != 0 {
				t.Errorf("по записи кода (%s) положено токенов доступа %d шт", tc.name, n)
			}
		})
	}
}
