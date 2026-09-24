// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// client_secret_test.go — доказательство клиента секретом: отказ неизвестному
// клиенту и отказ неверному секрету неразличимы, а сверку исполняет служба.
//
// Класс, а не экземпляр: клиент доказывает себя в ТРЁХ операциях церемонии —
// обмене, интроспекции и отзыве, — и у движка два разных пути сверки (точка
// токена и отзыв идут одним, интроспекция — своим). Пробы судят все три
// операции и все три способа доказательства.
package oauthceremony_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

const (
	// unknownClientID — клиент, которого нет в справочнике службы.
	unknownClientID = "svc-that-was-never-registered"
	// wrongSecret — секрет, не совпадающий с секретом зарегистрированного
	// клиента.
	wrongSecret = "wrong-horse-battery-staple"
)

// clientProof — чем клиент доказывает себя в одной операции.
type clientProof struct {
	clientID string
	secret   string
	method   oauthceremony.ClientAuthMethod
}

// rightProof — верное доказательство зарегистрированного клиента: положительный
// близнец каждого отказа ниже.
func rightProof() clientProof {
	return clientProof{clientID: testClientID, secret: testSecret, method: oauthceremony.ClientAuthBasic}
}

// authenticatedOperation — операция, в которой клиент доказывает себя.
//
// prepare готовит предмет операции ВЕРНЫМ доказательством — код, токен
// доступа, токен обновления, — чтобы отказ операции мог прийти только от
// доказательства, переданного perform. perform отвечает, исполнилась ли
// операция: у интроспекции «исполнилась» — это ответ `active: true` о живом
// токене.
//
// refusal — случай, которым операция отказывает недоказанному клиенту: точки
// токена и отзыва отвечают `invalid_client`, интроспекция — своим случаем
// движка. accepts — способы доказательства, которые операция принимает; пусто
// — каждый способ словаря. Способ вне accepts операция отвергает по имени до
// движка (CodeCeremonyMisuse), не спросив ни справочника, ни порта сверки:
// так интроспекция отвечает всему, кроме заголовка Authorization
// (RFC 7662 §2.1, IntrospectionAuthMethods).
//
// gone отвечает, снят ли предмет после исполненной операции, — у той, что его
// снимает (отзыв); пусто — операция предмета не снимает.
type authenticatedOperation struct {
	name    string
	refusal oauthceremony.FailureCode
	accepts []oauthceremony.ClientAuthMethod
	prepare func(t *testing.T, ceremony *oauthceremony.Ceremony) string
	perform func(ceremony *oauthceremony.Ceremony, subject string, proof clientProof) (bool, error)
	gone    func(t *testing.T, ceremony *oauthceremony.Ceremony, subject string) bool
}

// accepted отвечает, принимает ли операция способ доказательства.
func (op authenticatedOperation) accepted(method oauthceremony.ClientAuthMethod) bool {
	return op.accepts == nil || slices.Contains(op.accepts, method)
}

// refusalOf — случай, которым операция отказывает недоказанному клиенту с
// доказательством proof: способ, которого операция не принимает, отвергается
// до движка как ошибка вызывающего.
func (op authenticatedOperation) refusalOf(proof clientProof) oauthceremony.FailureCode {
	if !op.accepted(proof.method) {
		return oauthceremony.CodeCeremonyMisuse
	}
	return op.refusal
}

func authenticatedOperations() []authenticatedOperation {
	return []authenticatedOperation{
		{
			name:    "обмен кода",
			refusal: oauthceremony.CodeInvalidClient,
			prepare: func(t *testing.T, ceremony *oauthceremony.Ceremony) string {
				code, _ := issueCode(t, ceremony)
				return code
			},
			perform: func(ceremony *oauthceremony.Ceremony, code string, proof clientProof) (bool, error) {
				request := codeExchange(code)
				request.ClientID, request.ClientSecret, request.AuthMethod = proof.clientID, proof.secret, proof.method
				tokens, err := ceremony.Exchange(context.Background(), request)
				return err == nil && tokens.AccessToken != "", err
			},
		},
		{
			name:    "интроспекция",
			refusal: oauthceremony.CodeRequestUnauthorized,
			accepts: oauthceremony.IntrospectionAuthMethods(),
			prepare: func(t *testing.T, ceremony *oauthceremony.Ceremony) string {
				return exchangeCode(t, ceremony).AccessToken
			},
			perform: func(ceremony *oauthceremony.Ceremony, token string, proof clientProof) (bool, error) {
				result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
					Token:        token,
					KindHint:     oauthceremony.TokenKindAccess,
					ClientID:     proof.clientID,
					ClientSecret: proof.secret,
					AuthMethod:   proof.method,
				})
				return err == nil && result.Active, err
			},
		},
		{
			name:    "отзыв",
			refusal: oauthceremony.CodeInvalidClient,
			prepare: func(t *testing.T, ceremony *oauthceremony.Ceremony) string {
				return exchangeCode(t, ceremony).RefreshToken
			},
			perform: func(ceremony *oauthceremony.Ceremony, token string, proof clientProof) (bool, error) {
				err := ceremony.Revoke(context.Background(), oauthceremony.RevocationRequest{
					Token:        token,
					KindHint:     oauthceremony.TokenKindRefresh,
					ClientID:     proof.clientID,
					ClientSecret: proof.secret,
					AuthMethod:   proof.method,
				})
				return err == nil, err
			},
			gone: func(t *testing.T, ceremony *oauthceremony.Ceremony, token string) bool {
				t.Helper()
				proof := rightProof()
				result, err := ceremony.Introspect(context.Background(), oauthceremony.IntrospectionRequest{
					Token:        token,
					KindHint:     oauthceremony.TokenKindRefresh,
					ClientID:     proof.clientID,
					ClientSecret: proof.secret,
					AuthMethod:   proof.method,
				})
				if err != nil {
					t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: интроспекция отозванного токена отказала: %v", err)
				}
				return !result.Active
			},
		},
	}
}

// indistinguishablePairs — пары доказательств, отказы которым обязаны совпасть:
// неизвестный клиент против зарегистрированного с неверным доказательством,
// каждым способом. Против своего близнеца по паре меняется ровно один факт —
// идентификатор клиента.
func indistinguishablePairs() []struct {
	name           string
	unknown, known clientProof
} {
	return []struct {
		name           string
		unknown, known clientProof
	}{
		{
			name:    "секрет заголовком",
			unknown: clientProof{clientID: unknownClientID, secret: wrongSecret, method: oauthceremony.ClientAuthBasic},
			known:   clientProof{clientID: testClientID, secret: wrongSecret, method: oauthceremony.ClientAuthBasic},
		},
		{
			name:    "секрет телом",
			unknown: clientProof{clientID: unknownClientID, secret: wrongSecret, method: oauthceremony.ClientAuthPost},
			known:   clientProof{clientID: testClientID, secret: wrongSecret, method: oauthceremony.ClientAuthPost},
		},
		{
			name:    "без секрета",
			unknown: clientProof{clientID: unknownClientID, method: oauthceremony.ClientAuthNone},
			known:   clientProof{clientID: testClientID, method: oauthceremony.ClientAuthNone},
		},
	}
}

// wireForm — то, что поверхность пишет клиенту об отказе (RFC 6749 §5.2):
// состояние HTTP и тело из проводного кода, описания и подсказки. Подробности
// (Debug) проводу не годятся и сюда не входят — их судит сверка отказа целиком.
func wireForm(t *testing.T, err error) []byte {
	t.Helper()

	var refusal *oauthceremony.ProtocolError
	if !errors.As(err, &refusal) {
		t.Fatalf("отказ не нашей формы: %T %v", err, err)
	}
	body, marshalErr := json.Marshal(map[string]string{
		"error":             refusal.WireCode(),
		"error_description": refusal.Description,
		"error_hint":        refusal.Hint,
	})
	if marshalErr != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: тело отказа не собрано: %v", marshalErr)
	}
	return append([]byte(strconv.Itoa(refusal.HTTPStatus())+"\n"), body...)
}

// journalForm — отказ целиком, как его получает журнал службы.
func journalForm(err error) string {
	var refusal *oauthceremony.ProtocolError
	if !errors.As(err, &refusal) {
		return "not ours: " + err.Error()
	}
	return refusal.Code.String() + "\x00" + refusal.Error() + "\x00" + refusal.Debug
}

// TestUnknownClientIsRefusedExactlyLikeAWrongSecret — отказ неизвестному
// клиенту неотличим от отказа зарегистрированному клиенту с неверным
// доказательством: то же состояние HTTP, то же тело побайтово и тот же отказ
// целиком, в каждой операции и каждым способом доказательства. Иначе точка
// токена — прибор для перебора зарегистрированных клиентов.
//
// Положительный близнец — та же операция с верным доказательством: она
// исполняется, то есть отказ пар приходит от доказательства, а не от предмета.
func TestUnknownClientIsRefusedExactlyLikeAWrongSecret(t *testing.T) {
	var operations, pairs, twins int
	for _, op := range authenticatedOperations() {
		operations++

		t.Run(op.name+"/близнец: верное доказательство", func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			subject := op.prepare(t, ceremony)

			done, err := op.perform(ceremony, subject, rightProof())
			if err != nil || !done {
				t.Fatalf("операция с верным доказательством не исполнилась: исполнилась=%v, отказ %v", done, err)
			}
		})
		twins++

		for _, pair := range indistinguishablePairs() {
			pairs++
			t.Run(op.name+"/"+pair.name, func(t *testing.T) {
				refusals := map[string]error{}
				for side, proof := range map[string]clientProof{"неизвестный": pair.unknown, "известный": pair.known} {
					store := newMemoryPorts()
					registerTestClient(t, store)
					ceremony := newTestCeremony(t, store.ports())
					subject := op.prepare(t, ceremony)

					done, err := op.perform(ceremony, subject, proof)
					if err == nil || done {
						t.Fatalf("%s клиент прошёл: исполнилась=%v, отказ %v", side, done, err)
					}
					refusals[side] = err
				}

				unknown, known := refusals["неизвестный"], refusals["известный"]
				for side, err := range refusals {
					if want := op.refusalOf(pair.known); oauthceremony.CodeOf(err) != want {
						t.Errorf("%s клиент отвергнут случаем %v, ожидался %v: %v", side, oauthceremony.CodeOf(err), want, err)
					}
				}
				if u, k := wireForm(t, unknown), wireForm(t, known); string(u) != string(k) {
					t.Errorf("провод различает неизвестного клиента и неверное доказательство:\n"+
						"  неизвестный: %s\n  известный:   %s", u, k)
				}
				if u, k := journalForm(unknown), journalForm(known); u != k {
					t.Errorf("отказы различаются:\n  неизвестный: %q\n  известный:   %q", u, k)
				}
				t.Logf("отказ: %s", wireForm(t, known))
			})
		}
	}
	t.Logf("перепись: операций %d · пар %d · близнецов %d", operations, pairs, twins)
	if operations == 0 || pairs == 0 || twins != operations {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: операций %d, пар %d, близнецов %d", operations, pairs, twins)
	}
}
