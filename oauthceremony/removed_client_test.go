// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// removed_client_test.go — артефакт клиента, которого сняли из справочника
// между выдачей и предъявлением.
//
// Запрос движка по записи кода или токена мост собирает заново и спрашивает
// справочник о клиенте, которому выдан грант. Клиента могли снять, и «клиента
// нет» — законный ответ справочника, а не его отказ: артефакт такого клиента
// негоден. Предмет проб — что этот ответ доходит до движка как «записи нет» и
// операция отвечает так же, как на любой негодный артефакт: обмен —
// `invalid_grant`, интроспекция — `active: false`, отзыв — успехом без
// действия (RFC 7009 §2.2). «Внутренняя ошибка» и «временно недоступно» —
// ответы о поломке сервера, а сервер исправен.
//
// Класс, а не экземпляр: запрос по записи собирается на каждом пути, где
// предъявляют артефакт, — обмен кода, оборот токена обновления, интроспекция и
// отзыв токена доступа и токена обновления. Судятся все шесть.
//
// Предъявитель — другой клиент, прошедший сверку секрета
// (requireAuthenticatedBy): снятый клиент доказать себя не может, и отказ
// сверки был бы ответом на другой вопрос. Близнец каждого пути — тот же
// артефакт и тот же предъявитель при клиенте гранта в справочнике: против него
// меняется ровно один факт — снят ли клиент.
//
// Повтор артефакта снятого клиента — другой предмет: его держат
// TestReplayOfATokenWhoseClientIsGoneStillRevokesTheFamily и
// TestCodeReplayOfAClientThatIsGoneStillRevokesTheFamily.
package oauthceremony_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// strangerClientID — клиент, предъявляющий чужой артефакт.
const strangerClientID = "svc-stranger"

// removedClientPath — путь, на котором предъявляют артефакт тестового
// клиента.
//
// issue выдаёт артефакт тестовому клиенту и называет его грант. present
// предъявляет его от имени клиента clientID и отвечает исходом операции: у
// интроспекции — годен ли артефакт. twin судит исход при клиенте гранта в
// справочнике, removed — при снятом.
type removedClientPath struct {
	name    string
	issue   func(t *testing.T, ceremony *oauthceremony.Ceremony, store *memoryPorts) (artifact, grantID string)
	present func(ceremony *oauthceremony.Ceremony, artifact, clientID string) (active bool, err error)
	twin    func(t *testing.T, active bool, err error)
	removed func(t *testing.T, active bool, err error)
}

// requireInvalidGrant — отказ обмена: на проводе `invalid_grant` с состоянием
// 400, а не отказ сервера.
func requireInvalidGrant(t *testing.T, _ bool, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("обмен чужого артефакта исполнился")
	}
	if wire := oauthceremony.CodeOf(err).WireCode(); wire != "invalid_grant" {
		t.Errorf("на проводе отказ назван %q, ожидался \"invalid_grant\" (случай %v): %v",
			wire, oauthceremony.CodeOf(err), err)
	}
	if status := oauthceremony.CodeOf(err).HTTPStatus(); status != http.StatusBadRequest {
		t.Errorf("состояние отказа %d, ожидалось %d: %v", status, http.StatusBadRequest, err)
	}
	if errors.Is(err, oauthceremony.ErrServerError) {
		t.Errorf("негодный артефакт назван отказом сервера: %v", err)
	}
}

// requireInactive — интроспекция ответила «негоден», не отказав.
func requireInactive(t *testing.T, active bool, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("интроспекция отказала вместо ответа active=false: %v", err)
	}
	if active {
		t.Error("артефакт снятого клиента назван годным")
	}
}

// requireActive — интроспекция назвала живой артефакт годным.
func requireActive(t *testing.T, active bool, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("интроспекция живого артефакта отказала: %v", err)
	}
	if !active {
		t.Error("живой артефакт назван негодным")
	}
}

// requireRevocationSucceeded — отзыв ответил успехом.
func requireRevocationSucceeded(t *testing.T, _ bool, err error) {
	t.Helper()

	if err != nil {
		t.Errorf("отзыв негодного артефакта отказал (случай %v, на проводе %q, состояние %d): %v",
			oauthceremony.CodeOf(err), oauthceremony.CodeOf(err).WireCode(), oauthceremony.CodeOf(err).HTTPStatus(), err)
	}
}

// requireRevocationRefusedToStranger — отзыв чужого живого артефакта отвергнут
// как отзыв не своего (RFC 7009 §2.1).
func requireRevocationRefusedToStranger(t *testing.T, _ bool, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("чужой клиент отозвал живой артефакт")
	}
	if wire := oauthceremony.CodeOf(err).WireCode(); wire != "unauthorized_client" {
		t.Errorf("на проводе отказ назван %q, ожидался \"unauthorized_client\" (случай %v): %v",
			wire, oauthceremony.CodeOf(err), err)
	}
}

func removedClientPaths() []removedClientPath {
	issueTokens := func(pick func(oauthceremony.TokenResult) string) func(*testing.T, *oauthceremony.Ceremony, *memoryPorts) (string, string) {
		return func(t *testing.T, ceremony *oauthceremony.Ceremony, _ *memoryPorts) (string, string) {
			t.Helper()
			tokens := exchangeCode(t, ceremony)
			return pick(tokens), grantOf(t, ceremony, tokens.AccessToken)
		}
	}
	accessOf := func(tokens oauthceremony.TokenResult) string { return tokens.AccessToken }
	refreshOf := func(tokens oauthceremony.TokenResult) string { return tokens.RefreshToken }

	introspectAs := func(kind oauthceremony.TokenKind) func(*oauthceremony.Ceremony, string, string) (bool, error) {
		return func(ceremony *oauthceremony.Ceremony, token, clientID string) (bool, error) {
			result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
				Token:        token,
				KindHint:     kind,
				ClientID:     clientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
			return result.Active, err
		}
	}
	revokeAs := func(kind oauthceremony.TokenKind) func(*oauthceremony.Ceremony, string, string) (bool, error) {
		return func(ceremony *oauthceremony.Ceremony, token, clientID string) (bool, error) {
			return false, ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
				Token:        token,
				KindHint:     kind,
				ClientID:     clientID,
				ClientSecret: testSecret,
				AuthMethod:   oauthceremony.ClientAuthBasic,
			})
		}
	}

	return []removedClientPath{
		{
			name: "обмен кода",
			issue: func(t *testing.T, ceremony *oauthceremony.Ceremony, store *memoryPorts) (string, string) {
				t.Helper()
				code, _ := issueCode(t, ceremony)
				return code, grantOfStored(t, store)
			},
			present: func(ceremony *oauthceremony.Ceremony, code, clientID string) (bool, error) {
				request := codeExchange(code)
				request.ClientID = clientID
				_, err := ceremony.Exchange(context.Background(), request)
				return false, err
			},
			twin:    requireInvalidGrant,
			removed: requireInvalidGrant,
		},
		{
			name:  "оборот токена обновления",
			issue: issueTokens(refreshOf),
			present: func(ceremony *oauthceremony.Ceremony, token, clientID string) (bool, error) {
				request := refreshRequest(token)
				request.ClientID = clientID
				_, err := ceremony.Exchange(context.Background(), request)
				return false, err
			},
			twin:    requireInvalidGrant,
			removed: requireInvalidGrant,
		},
		{
			name:    "интроспекция токена доступа",
			issue:   issueTokens(accessOf),
			present: introspectAs(oauthceremony.TokenKindAccess),
			twin:    requireActive,
			removed: requireInactive,
		},
		{
			name:    "интроспекция токена обновления",
			issue:   issueTokens(refreshOf),
			present: introspectAs(oauthceremony.TokenKindRefresh),
			twin:    requireActive,
			removed: requireInactive,
		},
		{
			name:    "отзыв токена доступа",
			issue:   issueTokens(accessOf),
			present: revokeAs(oauthceremony.TokenKindAccess),
			twin:    requireRevocationRefusedToStranger,
			removed: requireRevocationSucceeded,
		},
		{
			name:    "отзыв токена обновления",
			issue:   issueTokens(refreshOf),
			present: revokeAs(oauthceremony.TokenKindRefresh),
			twin:    requireRevocationRefusedToStranger,
			removed: requireRevocationSucceeded,
		},
	}
}

// TestArtifactOfARemovedClientIsAnInvalidArtifact — артефакт клиента,
// снятого из справочника, предъявлен другим клиентом: ответ операции — ответ о
// негодном артефакте, а не об отказе сервера, и семейства гранта операция не
// отзывает — повтора нет, и клиент гранта об отзыве не просил.
func TestArtifactOfARemovedClientIsAnInvalidArtifact(t *testing.T) {
	paths := removedClientPaths()

	var twins, removed int
	for _, path := range paths {
		for _, clientRemoved := range []bool{false, true} {
			name := path.name + "/близнец: клиент гранта в справочнике"
			if clientRemoved {
				name = path.name + "/клиент гранта снят"
				removed++
			} else {
				twins++
			}
			t.Run(name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				registerClientLike(t, store, strangerClientID)
				ceremony := newTestCeremony(t, store.ports())

				artifact, grantID := path.issue(t, ceremony, store)
				if clientRemoved {
					store.mu.Lock()
					delete(store.clients, testClientID)
					store.mu.Unlock()
				}

				verified := len(store.verificationLog())
				active, err := path.present(ceremony, artifact, strangerClientID)
				requireAuthenticatedBy(t, store.verificationLog()[verified:], strangerClientID)
				if clientRemoved {
					path.removed(t, active, err)
				} else {
					path.twin(t, active, err)
				}
				requireNotRevoked(t, store, grantID)
			})
		}
	}
	t.Logf("перепись: путей %d · близнецов %d · со снятым клиентом %d", len(paths), twins, removed)
	if len(paths) == 0 || twins != len(paths) || removed != len(paths) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: путей %d, близнецов %d, со снятым клиентом %d", len(paths), twins, removed)
	}
}
