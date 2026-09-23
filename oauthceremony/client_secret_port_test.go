// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// client_secret_port_test.go — секрет клиента сверяет служба портом
// ClientSecretVerifier: сколько раз церемония его зовёт, что ему передаёт и
// как отвечает на его вердикт и его отказ.
package oauthceremony_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// expectedVerifications — сколько раз операция обязана позвать порт сверки при
// доказательстве proof. Интроспекция принимает доказательство только
// заголовком (RFC 7662 §2.1): запрос без заголовка движок отвергает раньше, чем
// спросит справочник, — и тогда порт не зовётся ни для кого.
func expectedVerifications(op authenticatedOperation, proof clientProof) int {
	if op.headerOnly && proof.method != oauthceremony.ClientAuthBasic {
		return 0
	}
	return 1
}

// TestClientSecretIsVerifiedByTheServicePortExactlyOnce — секрет клиента
// сверяет служба: порт сверки зовётся РОВНО ОДИН РАЗ за доказательство — и для
// зарегистрированного клиента, и для неизвестного, — получает предъявленное
// как есть, а церемония отвечает по его вердикту. Хеша секрета у церемонии
// нет: в записи клиента его негде нести, и выдача по верному секрету —
// предикат того, что хешер движка в пути не участвует.
//
// Близнец — верный секрет: один вызов и исполненная операция. Отказы меняют
// против него ровно один факт: секрет либо идентификатор клиента.
func TestClientSecretIsVerifiedByTheServicePortExactlyOnce(t *testing.T) {
	cases := []struct {
		name  string
		proof clientProof
		done  bool
	}{
		{name: "верный секрет заголовком", proof: rightProof(), done: true},
		{name: "неверный секрет", proof: clientProof{clientID: testClientID, secret: wrongSecret, method: oauthceremony.ClientAuthBasic}},
		{name: "неизвестный клиент", proof: clientProof{clientID: unknownClientID, secret: testSecret, method: oauthceremony.ClientAuthBasic}},
		{name: "верный секрет телом", proof: clientProof{clientID: testClientID, secret: testSecret, method: oauthceremony.ClientAuthPost}, done: true},
		{name: "неизвестный клиент, секрет телом", proof: clientProof{clientID: unknownClientID, secret: testSecret, method: oauthceremony.ClientAuthPost}},
		{name: "известный клиент без секрета", proof: clientProof{clientID: testClientID, method: oauthceremony.ClientAuthNone}},
		{name: "неизвестный клиент без секрета", proof: clientProof{clientID: unknownClientID, method: oauthceremony.ClientAuthNone}},
	}

	var judged, calledOnce, calledNever int
	for _, op := range authenticatedOperations() {
		for _, tc := range cases {
			judged++
			want := expectedVerifications(op, tc.proof)
			// Без заголовка интроспекция отказывает всякому — и верному секрету
			// телом тоже.
			done := tc.done && want == 1
			if want == 1 {
				calledOnce++
			} else {
				calledNever++
			}
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ceremony := newTestCeremony(t, store.ports())
				subject := op.prepare(t, ceremony)
				before := len(store.verificationLog())

				gotDone, err := op.perform(ceremony, subject, tc.proof)
				calls := store.verificationLog()[before:]

				if len(calls) != want {
					t.Fatalf("порт сверки позван %d раз, ожидалось %d: %+v", len(calls), want, calls)
				}
				for _, call := range calls {
					if call.clientID != tc.proof.clientID || call.presented != tc.proof.secret {
						t.Errorf("порт получил клиента %q и секрет %q, предъявлены %q и %q",
							call.clientID, call.presented, tc.proof.clientID, tc.proof.secret)
					}
				}
				if gotDone != done {
					t.Fatalf("операция исполнилась=%v, ожидалось %v; отказ %v", gotDone, done, err)
				}
				if !done {
					if got := oauthceremony.CodeOf(err); got != op.refusal {
						t.Errorf("отказ случаем %v, ожидался %v: %v", got, op.refusal, err)
					}
				}
			})
		}
	}
	t.Logf("перепись: операций %d · случаев %d · судимо %d (порт зовётся раз — %d, не зовётся — %d)",
		len(authenticatedOperations()), len(cases), judged, calledOnce, calledNever)
	if judged == 0 || judged != len(authenticatedOperations())*len(cases) || calledOnce == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d, из них с вызовом порта %d", judged, calledOnce)
	}
}

// TestClientSecretVerifierFailureFailsTheOperation — отказ порта сверки —
// отказ ОПЕРАЦИИ, а не «клиент не доказан»: сверка, которая не состоялась, не
// вправе стать ни «совпал», ни «не совпал». Истёкший срок остаётся истёкшим
// сроком, прочее — ошибкой сервера.
//
// Близнец — тот же порт без отказа: операция исполняется.
func TestClientSecretVerifierFailureFailsTheOperation(t *testing.T) {
	failures := map[string]struct {
		err  error
		want oauthceremony.FailureCode
	}{
		"хранилище проверочных значений недоступно": {err: errors.New("verifier store: connection refused"), want: oauthceremony.CodeServerError},
		"срок вызова истёк":                         {err: context.DeadlineExceeded, want: oauthceremony.CodePortDeadline},
	}

	for _, op := range authenticatedOperations() {
		t.Run(op.name+"/близнец: порт исправен", func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			subject := op.prepare(t, ceremony)
			if done, err := op.perform(ceremony, subject, rightProof()); !done || err != nil {
				t.Fatalf("операция не исполнилась: исполнилась=%v, отказ %v", done, err)
			}
		})
		for name, failure := range failures {
			t.Run(op.name+"/"+name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ceremony := newTestCeremony(t, store.ports())
				subject := op.prepare(t, ceremony)
				store.setVerifyOverride(func(string) (oauthceremony.SecretVerdict, error) {
					return oauthceremony.SecretVerdictUnspecified, failure.err
				})

				done, err := op.perform(ceremony, subject, rightProof())
				if done || err == nil {
					t.Fatalf("операция исполнилась при отказавшем порте сверки: %v", err)
				}
				if got := oauthceremony.CodeOf(err); got != failure.want {
					t.Errorf("отказ порта сверки назван случаем %v, ожидался %v: %v", got, failure.want, err)
				}
			})
		}
	}
}

// TestClientSecretVerdictOutsideTheContractIsAPortContractBreach — вердикт,
// которого контракт не допускает, — нарушение контракта порта, а не
// доказательство: неназванный вердикт, вердикт вне словаря и «совпал» о
// клиенте, которого справочник не знает. Последнее закрывает
// незарегистрированному клиенту путь внутрь, даже если порт солгал.
//
// Близнец — «совпал» о зарегистрированном клиенте: операция исполняется.
func TestClientSecretVerdictOutsideTheContractIsAPortContractBreach(t *testing.T) {
	matchedForAll := func(string) (oauthceremony.SecretVerdict, error) { return oauthceremony.SecretMatched, nil }
	cases := []struct {
		name    string
		verdict func(string) (oauthceremony.SecretVerdict, error)
		proof   clientProof
		done    bool
	}{
		{name: "близнец: «совпал» о зарегистрированном", verdict: matchedForAll, proof: rightProof(), done: true},
		{name: "неназванный вердикт", proof: rightProof(),
			verdict: func(string) (oauthceremony.SecretVerdict, error) { return oauthceremony.SecretVerdictUnspecified, nil }},
		{name: "вердикт вне словаря", proof: rightProof(),
			verdict: func(string) (oauthceremony.SecretVerdict, error) { return oauthceremony.SecretVerdict(200), nil }},
		{name: "«совпал» о неизвестном клиенте", verdict: matchedForAll,
			proof: clientProof{clientID: unknownClientID, secret: testSecret, method: oauthceremony.ClientAuthBasic}},
	}

	for _, op := range authenticatedOperations() {
		for _, tc := range cases {
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ceremony := newTestCeremony(t, store.ports())
				subject := op.prepare(t, ceremony)
				store.setVerifyOverride(tc.verdict)

				done, err := op.perform(ceremony, subject, tc.proof)
				if done != tc.done {
					t.Fatalf("операция исполнилась=%v, ожидалось %v; отказ %v", done, tc.done, err)
				}
				if !tc.done && !errors.Is(err, oauthceremony.ErrPortContract) {
					t.Errorf("отказ случаем %v, ожидался %v: %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract, err)
				}
			})
		}
	}
}

// TestPresentedSecretIsNeverPrinted — предъявленный секрет, уезжающий порту
// сверки, не печатается ни одним глаголом форматирования: реализация порта,
// записавшая аргумент в журнал, не записала бы секрета. Значение отдаёт
// только Reveal — он и есть положительный близнец.
func TestPresentedSecretIsNeverPrinted(t *testing.T) {
	presented := oauthceremony.NewPresentedSecret(testSecret)
	if got := presented.Reveal(); got != testSecret {
		t.Fatalf("Reveal отдал %q, предъявлено %q", got, testSecret)
	}

	hexForm := fmt.Sprintf("%x", testSecret)
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T %v"}
	rendered := map[string]string{
		"String":     presented.String(),
		"в составе":  fmt.Sprintf("%+v", struct{ Secret oauthceremony.PresentedSecret }{presented}),
		"указателем": fmt.Sprintf("%v", &presented),
		// Закрытое поле отражение не отдаёт методам: String здесь не зовётся,
		// и печать держится только тем, что значение лежит за указателем.
		"в закрытом поле": fmt.Sprintf("%+v %#v %d", struct{ secret oauthceremony.PresentedSecret }{presented},
			struct{ secret oauthceremony.PresentedSecret }{presented}, struct{ secret oauthceremony.PresentedSecret }{presented}),
	}
	for _, verb := range verbs {
		rendered["глагол "+verb] = fmt.Sprintf(verb, presented)
	}
	for name, text := range rendered {
		if strings.Contains(text, testSecret) || strings.Contains(strings.ToLower(text), hexForm) {
			t.Errorf("%s напечатал секрет: %s", name, text)
		}
	}
	t.Logf("перепись: печатей %d (глаголов %d)", len(rendered), len(verbs))
}
