// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// scope_token_test.go — каждая область, которую церемония выдаёт, —
// `scope-token` по RFC 6749 §3.3:
//
//	scope-token = 1*( %x21 / %x23-5B / %x5D-7E )
//
// Область уезжает клиенту строкой через пробел (`scope` ответа), и знак вне
// грамматики меняет прочитанное: пробел внутри одной области клиент читает
// двумя областями, табуляцию и прочие знаки — чем придётся. Движок выданное не
// судит, а правило с образцом (`tenant.*`) покрывает любой непустой хвост —
// значит, судит церемония.
//
// Область входит в выданное тремя путями, и каждый судится своей пробой:
// запрос авторизации (отказ клиенту `invalid_scope`), решение службы о выдаче
// (ErrCeremonyMisuse) и запись гранта, отданная хранилищем, — по ней обмен и
// оборот выдают области заново (нарушение контракта порта).
//
// Грамматика выписана здесь заново, из RFC, а не взята у церемонии: проба,
// спрашивающая судью о нём самом, проверяла бы только то, что он знает.
package oauthceremony_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// inScopeTokenGrammar — байт входит в `scope-token` (RFC 6749 §3.3).
func inScopeTokenGrammar(c byte) bool {
	return c == 0x21 || (0x23 <= c && c <= 0x5B) || (0x5D <= c && c <= 0x7E)
}

// scopeTokenEdges — крайние знаки грамматики: начало и конец каждого
// диапазона. Область из них — положительный близнец каждого отказа.
const scopeTokenEdges = "!#[]~"

// tenantScopeCeremony — церемония с правилом образца и клиентом, которому
// дозволено `tenant.*`: образец покрывает всякий непустой хвост, и судить форму
// выданной области, кроме церемонии, некому.
func tenantScopeCeremony(t *testing.T) (*memoryPorts, *oauthceremony.Ceremony) {
	t.Helper()

	store := newMemoryPorts()
	registerTestClient(t, store)
	reg := store.clients[testClientID]
	reg.Scopes = append(reg.Scopes, "tenant.*")
	store.clients[testClientID] = reg
	return store, newTestCeremony(t, store.ports(), func(cfg *oauthceremony.Config) {
		cfg.ScopeMatching = oauthceremony.ScopeMatchingWildcard
	})
}

// tenantRequest — запрос авторизации областей openid, offline и образца
// `tenant.*`.
func tenantRequest() oauthceremony.AuthorizationRequest {
	req := authorizeRequest()
	req.Scopes = []string{"openid", "offline", "tenant.*"}
	return req
}

// storedCodes — сколько кодов лежит в подставке хранилища.
func storedCodes(store *memoryPorts) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.codes)
}

// TestEveryByteOfAGrantedScopeIsJudgedByTheGrammar — область, которую служба
// решила выдать, выдаётся тогда и только тогда, когда каждый её байт входит в
// грамматику: судятся все 256 значений байта в середине области, покрытой
// образцом запроса. Вне грамматики — отказ ErrCeremonyMisuse и ни одного кода в
// хранилище; в грамматике — код выпущен, и область лежит в записи без
// изменений. Против близнеца меняется ровно один байт.
func TestEveryByteOfAGrantedScopeIsJudgedByTheGrammar(t *testing.T) {
	var admitted, refused int
	var off []string
	for b := range 256 {
		c := byte(b)
		scope := "tenant.a" + string([]byte{c}) + "b"
		store, ceremony := tenantScopeCeremony(t)

		_, err := completeWith(t, ceremony, tenantRequest(), grantOfScopes("openid", "offline", scope))
		codes := storedCodes(store)

		if inScopeTokenGrammar(c) {
			admitted++
			if err != nil || codes != 1 {
				off = append(off, fmt.Sprintf("%%x%02X в грамматике, а выдача отвергнута (кодов %d): %v", c, codes, err))
			}
			continue
		}
		refused++
		switch {
		case err == nil:
			off = append(off, fmt.Sprintf("%%x%02X вне грамматики, а область %q выдана (кодов %d)", c, scope, codes))
		case !errors.Is(err, oauthceremony.ErrCeremonyMisuse):
			off = append(off, fmt.Sprintf("%%x%02X отвергнут случаем %v, ожидался %v: %v", c, oauthceremony.CodeOf(err), oauthceremony.CodeCeremonyMisuse, err))
		case codes != 0:
			off = append(off, fmt.Sprintf("%%x%02X отвергнут, а кодов в хранилище %d", c, codes))
		}
	}
	for _, line := range off {
		t.Error(line)
	}
	t.Logf("перепись: байтов %d · в грамматике %d · вне её %d · расхождений %d", admitted+refused, admitted, refused, len(off))
	if admitted+refused != 256 || admitted != 92 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d байтов, в грамматике %d (по RFC 6749 §3.3 — 92)", admitted+refused, admitted)
	}
}

// TestGrantedScopeWithAWhitespaceInsideIsRefusedByName — пробел и табуляция
// внутри одной области (предикат задачи): отказ называет поле, область и
// знак, записи гранта в хранилище нет. Близнец — область из крайних знаков
// грамматики: выдана, и именно она, без изменений, доезжает до ответа обмена и
// до интроспекции.
func TestGrantedScopeWithAWhitespaceInsideIsRefusedByName(t *testing.T) {
	twin := "tenant." + scopeTokenEdges

	t.Run("близнец: крайние знаки грамматики выданы без изменений", func(t *testing.T) {
		_, ceremony := tenantScopeCeremony(t)
		result, err := completeWith(t, ceremony, tenantRequest(), grantOfScopes("openid", "offline", twin))
		if err != nil {
			t.Fatalf("выдача области %q отвергнута: %v", twin, err)
		}
		tokens, err := ceremony.Exchange(context.Background(), codeExchange(result.Parameters["code"][0]))
		if err != nil {
			t.Fatalf("обмен кода отказал: %v", err)
		}
		if !slices.Contains(tokens.Scopes, twin) {
			t.Errorf("ответ обмена несёт области %q, выдана %q", tokens.Scopes, twin)
		}
		if got := introspect(t, ceremony, tokens.AccessToken, oauthceremony.TokenKindAccess).Scopes; !slices.Contains(got, twin) {
			t.Errorf("интроспекция называет области %q, выдана %q", got, twin)
		}
	})

	for name, defect := range map[string]string{"пробел": " ", "табуляция": "\t"} {
		scope := "tenant.!#" + defect + "]~" // против близнеца заменён `[`
		t.Run(name, func(t *testing.T) {
			store, ceremony := tenantScopeCeremony(t)
			result, err := completeWith(t, ceremony, tenantRequest(), grantOfScopes("openid", "offline", scope))
			if err == nil {
				t.Fatalf("область %q выдана: параметры ответа %v", scope, result.Parameters)
			}
			if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
				t.Errorf("область %q отвергнута случаем %v, ожидался %v", scope, oauthceremony.CodeOf(err), oauthceremony.CodeCeremonyMisuse)
			}
			if codes := result.Parameters["code"]; len(codes) != 0 || storedCodes(store) != 0 {
				t.Errorf("при отказе выпущен код: в ответе %v, в хранилище %d", codes, storedCodes(store))
			}
			hex := fmt.Sprintf("%%x%02X", defect[0])
			for _, want := range []string{"AuthorizationGrant.GrantedScopes", strconv.Quote(scope), "scope-token", hex} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("отказ не называет %s: %q", want, err)
				}
			}
		})
	}
}

// TestRequestedScopeOutsideTheGrammarIsRefusedToTheClient — запрос
// авторизации, область которого вне грамматики, отвергается `invalid_scope`
// ДО согласия, и отказ годен для доставки клиенту: намерение выдано, адрес
// возврата проверен, DenyAuthorization собирает перенаправление с
// `error=invalid_scope`. Судятся все 256 значений байта в середине
// запрошенной области; пробел делит её на две, и вторую отвергает движок, —
// исход тот же.
//
// Близнец — байт грамматики: Authorize не отказывает.
func TestRequestedScopeOutsideTheGrammarIsRefusedToTheClient(t *testing.T) {
	var admitted, refused int
	var off []string
	for b := range 256 {
		c := byte(b)
		_, ceremony := tenantScopeCeremony(t)
		req := authorizeRequest()
		req.Scopes = []string{"openid", "tenant.a" + string([]byte{c}) + "b"}

		intent, err := ceremony.Authorize(context.Background(), req)
		if inScopeTokenGrammar(c) {
			admitted++
			if err != nil {
				off = append(off, fmt.Sprintf("%%x%02X в грамматике, а запрос отвергнут: %v", c, err))
			}
			continue
		}
		refused++
		if !errors.Is(err, oauthceremony.ErrInvalidScope) {
			off = append(off, fmt.Sprintf("%%x%02X вне грамматики, а Authorize ответил случаем %v: %v", c, oauthceremony.CodeOf(err), err))
			continue
		}
		if intent.RedirectURI() == "" {
			off = append(off, fmt.Sprintf("%%x%02X: отказ недоставим — адрес возврата не проверен", c))
			continue
		}
		denied, derr := ceremony.DenyAuthorization(context.Background(), intent, err)
		if derr != nil || !slices.Equal(denied.Parameters["error"], []string{"invalid_scope"}) {
			off = append(off, fmt.Sprintf("%%x%02X: перенаправление несёт %v, отказ сборки %v", c, denied.Parameters["error"], derr))
		}
	}
	for _, line := range off {
		t.Error(line)
	}
	t.Logf("перепись: байтов %d · в грамматике %d · вне её %d · расхождений %d", admitted+refused, admitted, refused, len(off))
	if admitted+refused != 256 || admitted != 92 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d байтов, в грамматике %d", admitted+refused, admitted)
	}
}

// TestUndeliveredScopeRefusalStillIssuesNoCode — служба, не доставившая
// отказ Authorize и закрывшая то же намерение выдачей, кода не получает: ни
// ответа с кодом, ни записи в хранилище, даже если негодной области нет среди
// выданных. Близнец — то же намерение с областью грамматики: код выпущен.
func TestUndeliveredScopeRefusalStillIssuesNoCode(t *testing.T) {
	for name, scope := range map[string]string{"близнец": "tenant.a!b", "табуляция": "tenant.a\tb"} {
		t.Run(name, func(t *testing.T) {
			store, ceremony := tenantScopeCeremony(t)
			req := authorizeRequest()
			req.Scopes = []string{"openid", scope}
			intent, authorizeErr := ceremony.Authorize(context.Background(), req)

			result, err := ceremony.CompleteAuthorization(context.Background(), intent, grantOfScopes("openid"))
			if name == "близнец" {
				if authorizeErr != nil || err != nil || storedCodes(store) != 1 {
					t.Fatalf("законный запрос не выдал кода: Authorize %v, выдача %v, кодов %d", authorizeErr, err, storedCodes(store))
				}
				return
			}
			if authorizeErr == nil {
				t.Errorf("Authorize принял область %q", scope)
			}
			if !errors.Is(err, oauthceremony.ErrInvalidScope) {
				t.Errorf("выдача по недоставленному отказу — случай %v, ожидался %v: %v",
					oauthceremony.CodeOf(err), oauthceremony.CodeInvalidScope, err)
			}
			if codes := result.Parameters["code"]; len(codes) != 0 || storedCodes(store) != 0 {
				t.Errorf("по недоставленному отказу выпущен код: в ответе %v, в хранилище %d", codes, storedCodes(store))
			}
		})
	}
}

// storedScopePath — путь, которым церемония выдаёт области заново по записи
// гранта из хранилища.
type storedScopePath struct {
	name string
	// corrupt портит выданные области записей этого пути в подставке.
	corrupt func(store *memoryPorts, bad string)
	// perform исполняет путь над выданной парой.
	perform func(ceremony *oauthceremony.Ceremony, code string, tokens oauthceremony.TokenResult) error
	// needsTokens — путю нужна пара, выданная обменом кода.
	needsTokens bool
}

// replaceScope заменяет в перечне выданных область twin областью bad.
func replaceScope(scopes []string, twin, bad string) []string {
	out := slices.Clone(scopes)
	for i, s := range out {
		if s == twin {
			out[i] = bad
		}
	}
	return out
}

func storedScopePaths(twin string) []storedScopePath {
	return []storedScopePath{
		{
			name: "обмен кода",
			corrupt: func(store *memoryPorts, bad string) {
				for _, row := range store.codes {
					row.grant.GrantedScopes = replaceScope(row.grant.GrantedScopes, twin, bad)
				}
			},
			perform: func(ceremony *oauthceremony.Ceremony, code string, _ oauthceremony.TokenResult) error {
				_, err := ceremony.Exchange(context.Background(), codeExchange(code))
				return err
			},
		},
		{
			name:        "оборот токена обновления",
			needsTokens: true,
			corrupt: func(store *memoryPorts, bad string) {
				for _, row := range store.refresh {
					row.grant.GrantedScopes = replaceScope(row.grant.GrantedScopes, twin, bad)
				}
			},
			perform: func(ceremony *oauthceremony.Ceremony, _ string, tokens oauthceremony.TokenResult) error {
				_, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
				return err
			},
		},
		{
			name:        "интроспекция токена доступа",
			needsTokens: true,
			corrupt: func(store *memoryPorts, bad string) {
				for sig, grant := range store.access {
					grant.GrantedScopes = replaceScope(grant.GrantedScopes, twin, bad)
					store.access[sig] = grant
				}
			},
			perform: func(ceremony *oauthceremony.Ceremony, _ string, tokens oauthceremony.TokenResult) error {
				result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
					Token: tokens.AccessToken, KindHint: oauthceremony.TokenKindAccess,
					ClientID: testClientID, ClientSecret: testSecret, AuthMethod: oauthceremony.ClientAuthBasic,
				})
				if err == nil && !result.Active {
					return errors.New("интроспекция ответила active=false")
				}
				return err
			},
		},
	}
}

// TestStoredGrantedScopeOutsideTheGrammarIsAContractBreach — запись гранта,
// выданные области которой хранилище вернуло вне грамматики, — нарушение
// контракта порта: церемония на хранение такой не отдавала, а по записи обмен
// кода и оборот выдают области заново. Судятся обмен кода, оборот токена
// обновления и интроспекция; отказ — CodePortContract, и подробности называют
// поле записи.
//
// Близнец каждого пути — та же запись без порчи: путь исполнен. Против него
// меняется ровно один байт одной области записи.
func TestStoredGrantedScopeOutsideTheGrammarIsAContractBreach(t *testing.T) {
	twin := "tenant." + scopeTokenEdges
	bad := "tenant.!# ]~"

	var judged int
	for _, path := range storedScopePaths(twin) {
		for _, corrupted := range []bool{false, true} {
			judged++
			name := path.name + "/близнец: запись цела"
			if corrupted {
				name = path.name + "/область записи с пробелом"
			}
			t.Run(name, func(t *testing.T) {
				store, ceremony := tenantScopeCeremony(t)
				result, err := completeWith(t, ceremony, tenantRequest(), grantOfScopes("openid", "offline", twin))
				if err != nil {
					t.Fatalf("ПРЕДПОСЫЛКА: выдача отвергнута: %v", err)
				}
				code := result.Parameters["code"][0]
				var tokens oauthceremony.TokenResult
				if path.needsTokens {
					if tokens, err = ceremony.Exchange(context.Background(), codeExchange(code)); err != nil {
						t.Fatalf("ПРЕДПОСЫЛКА: обмен кода отказал: %v", err)
					}
				}
				if corrupted {
					store.mu.Lock()
					path.corrupt(store, bad)
					store.mu.Unlock()
				}

				err = path.perform(ceremony, code, tokens)
				if !corrupted {
					if err != nil {
						t.Fatalf("путь по целой записи не исполнен: %v", err)
					}
					return
				}
				if !errors.Is(err, oauthceremony.ErrPortContract) {
					t.Fatalf("запись с областью %q принята: случай %v, отказ %v", bad, oauthceremony.CodeOf(err), err)
				}
				var refusal *oauthceremony.ProtocolError
				if errors.As(err, &refusal) && !strings.Contains(refusal.Debug, "GrantRecord.GrantedScopes") {
					t.Errorf("подробности отказа не называют поле записи: %q", refusal.Debug)
				}
			})
		}
	}
	t.Logf("перепись: путей %d · судимо %d", len(storedScopePaths(twin)), judged)
	if judged == 0 || judged != 2*len(storedScopePaths(twin)) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d", judged)
	}
}
