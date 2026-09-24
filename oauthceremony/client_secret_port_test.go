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
	"sync"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// expectedVerifications — сколько раз операция обязана позвать порт сверки при
// доказательстве proof. Интроспекция принимает доказательство только
// заголовком (RFC 7662 §2.1, IntrospectionAuthMethods): иной способ церемония
// отвергает по имени до движка, — и тогда порт не зовётся ни для кого.
func expectedVerifications(op authenticatedOperation, proof clientProof) int {
	if !op.accepted(proof.method) {
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
			// Способ, которого операция не принимает, отвергается всякому — и
			// верному секрету телом тоже.
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
					if got, want := oauthceremony.CodeOf(err), op.refusalOf(tc.proof); got != want {
						t.Errorf("отказ случаем %v, ожидался %v: %v", got, want, err)
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
//
// Провод отказа постоянен: описание одно на случай, подсказки нет, и текста
// причины, которым отказал порт, в нём нет — он годится журналу (Debug), а не
// клиенту.
func TestClientSecretVerifierFailureFailsTheOperation(t *testing.T) {
	failures := map[string]struct {
		err  error
		want oauthceremony.FailureCode
	}{
		"хранилище проверочных значений недоступно": {err: errors.New(verifierDownText), want: oauthceremony.CodeServerError},
		"срок вызова истёк":                         {err: context.DeadlineExceeded, want: oauthceremony.CodePortDeadline},
	}

	wires := portFailureWires{}
	defer wires.requireOnePerCode(t)
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
				wires.add(t, op.name+"/"+name, err, false, failure.err.Error())
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
		// Каждому глаголу — своё значение: глагол без значения печатает
		// %!v(MISSING) и не судит ничего.
		args := make([]any, strings.Count(verb, "%"))
		for i := range args {
			args[i] = presented
		}
		rendered["глагол "+verb] = fmt.Sprintf(verb, args...)
	}
	for name, text := range rendered {
		if strings.Contains(text, "%!") {
			t.Errorf("НЕ ВЫПОЛНИЛОСЬ: печать %s не состоялась — судить нечего: %s", name, text)
		}
		if strings.Contains(text, testSecret) || strings.Contains(strings.ToLower(text), hexForm) {
			t.Errorf("%s напечатал секрет: %s", name, text)
		}
	}
	t.Logf("перепись: печатей %d (глаголов %d)", len(rendered), len(verbs))
}

// verifierDownText и directoryDownText — причины, которыми отказывают порты
// доказательства в пробах: текст хранилища, место которому в журнале, а не на
// проводе.
const (
	verifierDownText  = "verifier store: connection refused"
	directoryDownText = "client directory: connection refused"
)

// failingDirectory — справочник клиентов, который на каждый вопрос отвечает
// отказом err.
type failingDirectory struct{ err error }

func (d failingDirectory) LookupClient(context.Context, string) (oauthceremony.ClientRegistration, error) {
	return oauthceremony.ClientRegistration{}, d.err
}

// directoryOperations — операции, в которых мост спрашивает справочник о
// клиенте запроса: три доказательства клиента и точка авторизации, где
// справочник судит клиента без секрета.
func directoryOperations() []authenticatedOperation {
	return append(authenticatedOperations(), authenticatedOperation{
		name:    "запрос авторизации",
		prepare: func(*testing.T, *oauthceremony.Ceremony) string { return "" },
		perform: func(ceremony *oauthceremony.Ceremony, _ string, _ clientProof) (bool, error) {
			intent, err := ceremony.Authorize(context.Background(), authorizeRequest())
			return err == nil && intent.Issued(), err
		},
	})
}

// portFailureWires — формы провода отказов порта по случаю. Отказ порта
// уезжает на провод ПОСТОЯННЫМ текстом: одна форма на случай, какой бы порт, в
// какой операции и какой причиной ни отказал, — а причина остаётся журналу.
type portFailureWires map[oauthceremony.FailureCode]map[string][]string

// add судит провод одного отказа: в описании и подсказке нет ни одного из
// текстов причины; подсказки нет, если hinted ложно. Форма провода
// запоминается для requireOnePerCode.
func (w portFailureWires) add(t *testing.T, name string, err error, hinted bool, causes ...string) {
	t.Helper()

	var refusal *oauthceremony.ProtocolError
	if !errors.As(err, &refusal) {
		t.Fatalf("%s: отказ не нашей формы: %T %v", name, err, err)
	}
	for _, cause := range causes {
		if cause != "" && strings.Contains(refusal.Description+"\x00"+refusal.Hint, cause) {
			t.Errorf("%s: провод несёт текст причины %q: %s", name, cause, wireForm(t, err))
		}
	}
	if !hinted && refusal.Hint != "" {
		t.Errorf("%s: у отказа порта подсказка %q", name, refusal.Hint)
	}
	form := string(wireForm(t, err))
	if w[refusal.Code] == nil {
		w[refusal.Code] = map[string][]string{}
	}
	w[refusal.Code][form] = append(w[refusal.Code][form], name)
}

// requireOnePerCode — у каждого случая ровно одна форма провода, и судить было
// что.
func (w portFailureWires) requireOnePerCode(t *testing.T) {
	t.Helper()

	if len(w) == 0 {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: ни одного отказа порта не дошло до сверки провода")
	}
	var judged int
	for code, forms := range w {
		for _, names := range forms {
			judged += len(names)
		}
		if len(forms) != 1 {
			var sample []string
			for form, names := range forms {
				if len(sample) == 3 {
					break
				}
				sample = append(sample, names[0]+" → "+form)
			}
			t.Errorf("отказ %v уехал на провод %d формами — текст зависит от операции или причины; например:\n  %s",
				code, len(forms), strings.Join(sample, "\n  "))
		}
	}
	t.Logf("перепись провода: случаев %d · отказов %d", len(w), judged)
}

// TestClientDirectoryFailureFailsTheOperation — отказ справочника клиентов —
// отказ ОПЕРАЦИИ, а не «клиент не доказан» и не «клиента нет»: у второго
// порта доказательства правило то же, что у порта сверки. Иначе сбой хранилища
// выглядел бы для клиента как отказ его доказательству, а для журнала службы —
// как поток неверных секретов. Правило держится и на точке авторизации:
// «справочник не ответил» там тоже не «клиента нет». Провод отказа постоянен,
// как у порта сверки.
//
// Близнец — та же церемония над тем же хранилищем с исправным справочником:
// операция исполняется. Против него меняется ровно один факт — порт
// справочника.
func TestClientDirectoryFailureFailsTheOperation(t *testing.T) {
	wires := portFailureWires{}
	defer wires.requireOnePerCode(t)
	for _, op := range directoryOperations() {
		for _, directoryDown := range []bool{false, true} {
			name := op.name + "/близнец: справочник исправен"
			if directoryDown {
				name = op.name + "/справочник не отвечает"
			}
			t.Run(name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				subject := op.prepare(t, newTestCeremony(t, store.ports()))

				ports := store.ports()
				if directoryDown {
					ports.Clients = failingDirectory{err: errors.New(directoryDownText)}
				}
				done, err := op.perform(newTestCeremony(t, ports), subject, rightProof())

				if !directoryDown {
					if !done || err != nil {
						t.Fatalf("операция не исполнилась: исполнилась=%v, отказ %v", done, err)
					}
					return
				}
				if done || err == nil {
					t.Fatalf("операция исполнилась при отказавшем справочнике: %v", err)
				}
				if got := oauthceremony.CodeOf(err); got != oauthceremony.CodeServerError {
					t.Errorf("отказ справочника назван случаем %v, ожидался %v: %v", got, oauthceremony.CodeServerError, err)
				}
				wires.add(t, name, err, false, directoryDownText)
			})
		}
	}
}

// TestClientSecretVerifierCallCarriesThePortDeadline — вызову порта сверки
// назначен СВОЙ срок, срок вызова порта (Config.PortTimeout), а не только срок
// всей операции: зависшая сверка держала бы запрос до срока операции, и
// обещание ClientSecretVerifier («срок вызова — Config.PortTimeout») осталось
// бы словами. Судится каждый вызов порта в каждой операции доказательства — о
// зарегистрированном клиенте и о неизвестном.
func TestClientSecretVerifierCallCarriesThePortDeadline(t *testing.T) {
	const portTimeout = 2 * time.Second // newTestCeremony: PortTimeout 2s, OperationTimeout 5s.

	proofs := []struct {
		name  string
		proof clientProof
	}{
		{name: "зарегистрированный клиент", proof: rightProof()},
		{name: "неизвестный клиент", proof: clientProof{clientID: unknownClientID, secret: testSecret, method: oauthceremony.ClientAuthBasic}},
	}

	var judged int
	for _, op := range authenticatedOperations() {
		for _, tc := range proofs {
			judged++
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ceremony := newTestCeremony(t, store.ports())
				subject := op.prepare(t, ceremony)
				before := len(store.verificationLog())

				// Исход операции судят другие пробы; здесь — срок вызова порта.
				_, _ = op.perform(ceremony, subject, tc.proof)
				calls := store.verificationLog()[before:]

				if len(calls) != 1 {
					t.Fatalf("ПРЕДПОСЫЛКА: порт сверки позван %d раз, ожидался один", len(calls))
				}
				if !calls[0].limited {
					t.Fatal("вызов порта сверки пришёл без срока")
				}
				if left := calls[0].remaining; left > portTimeout {
					t.Errorf("остаток срока вызова порта сверки %v больше срока вызова порта %v — порту назначен срок операции", left, portTimeout)
				}
			})
		}
	}
	t.Logf("перепись: операций %d · доказательств %d · судимо %d", len(authenticatedOperations()), len(proofs), judged)
	if judged == 0 || judged != len(authenticatedOperations())*len(proofs) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d", judged)
	}
}

// declaredFailureCodes — каждый объявленный случай пакета. Перечень выводится
// из таблицы кодов (FailureCode.Declared), а не выписан: случай, добавленный в
// таблицу, судится без правки пробы.
func declaredFailureCodes() []oauthceremony.FailureCode {
	var codes []oauthceremony.FailureCode
	for code := oauthceremony.CodeUnspecified + 1; code.Declared(); code++ {
		codes = append(codes, code)
	}
	return codes
}

// portOwnText — текст, который порт положил в свой отказ. На провод он не
// уезжает: провод отказа порта постоянен.
const portOwnText = "port-own-text-that-must-not-reach-the-wire"

// proofPortRun — исход операции, когда порт доказательства отвечает отказом
// failure (nil — порт исправен), и церемония над тем же хранилищем с
// исправными портами — ею близнец проверяет, что отзыв снял токен.
type proofPortRun struct {
	done    bool
	err     error
	healthy *oauthceremony.Ceremony
	subject string
}

// proofPort — порт доказательства клиента, операции, в которых его зовут, и
// доказательства, с которыми его зовут. declared — случай пакета, который
// контракт порта объявляет своим отказом: его разбирает своя ветка и свои
// пробы, и здесь он не судится; CodeUnspecified — не объявляет ни одного.
type proofPort struct {
	name     string
	ops      []authenticatedOperation
	proofs   []clientProof
	declared oauthceremony.FailureCode
	run      func(t *testing.T, op authenticatedOperation, proof clientProof, failure error) proofPortRun
}

func proofPorts() []proofPort {
	return []proofPort{
		{
			name: "порт сверки",
			ops:  authenticatedOperations(),
			proofs: []clientProof{
				rightProof(),
				{clientID: unknownClientID, secret: testSecret, method: oauthceremony.ClientAuthBasic},
			},
			run: func(t *testing.T, op authenticatedOperation, proof clientProof, failure error) proofPortRun {
				store := newMemoryPorts()
				registerTestClient(t, store)
				ceremony := newTestCeremony(t, store.ports())
				subject := op.prepare(t, ceremony)
				if failure != nil {
					store.setVerifyOverride(func(string) (oauthceremony.SecretVerdict, error) {
						return oauthceremony.SecretVerdictUnspecified, failure
					})
				}
				done, err := op.perform(ceremony, subject, proof)
				store.setVerifyOverride(nil)
				return proofPortRun{done: done, err: err, healthy: ceremony, subject: subject}
			},
		},
		{
			name:     "справочник",
			ops:      directoryOperations(),
			proofs:   []clientProof{rightProof()},
			declared: oauthceremony.CodeGrantNotFound,
			run: func(t *testing.T, op authenticatedOperation, proof clientProof, failure error) proofPortRun {
				store := newMemoryPorts()
				registerTestClient(t, store)
				healthy := newTestCeremony(t, store.ports())
				subject := op.prepare(t, healthy)
				ports := store.ports()
				if failure != nil {
					ports.Clients = failingDirectory{err: failure}
				}
				done, err := op.perform(newTestCeremony(t, ports), subject, proof)
				return proofPortRun{done: done, err: err, healthy: healthy, subject: subject}
			},
		},
	}
}

// TestProofPortFailureWithACaseOfThisPackageIsAContractBreach — порт
// доказательства клиента, ответивший отказом со СЛУЧАЕМ ЭТОГО ПАКЕТА, нарушил
// контракт: у порта сверки в контракте нет ни одного такого отказа, у
// справочника — ровно один, «клиента нет» (ErrGrantNotFound), и его разбирает
// своя ветка. Случай порта не вправе стать исходом операции: отзыв читает
// часть случаев как «нечего снимать» и ответил бы успехом, не сняв токена,
// интроспекция — как «токен негоден», а авторизация вернула бы его клиенту.
//
// Входы — каждый объявленный случай (declaredFailureCodes) × обе формы отказа
// (часовой как есть и обёрнутый вместе с текстом порта) × каждая операция, где
// порт зовётся, × каждое доказательство, с которым он зовётся. Ожидается:
// операция отказывает случаем CodePortContract; случай порта не находится
// errors.Is; текст порта не доезжает до провода; провод один на все входы.
//
// Близнецы каждой пары «порт × операция»: исправный порт — операция исполнена
// (у отзыва токен после неё негоден); простая ошибка — CodeServerError. Против
// них меняется ровно один факт — отказ порта.
func TestProofPortFailureWithACaseOfThisPackageIsAContractBreach(t *testing.T) {
	forms := []struct {
		name string
		of   func(oauthceremony.FailureCode) error
	}{
		{name: "часовой как есть", of: func(code oauthceremony.FailureCode) error {
			return &oauthceremony.ProtocolError{Code: code}
		}},
		{name: "обёрнут с текстом порта", of: func(code oauthceremony.FailureCode) error {
			return fmt.Errorf("%s-wrap: %w", portOwnText, &oauthceremony.ProtocolError{
				Code: code, Description: portOwnText + "-description", Hint: portOwnText + "-hint", Debug: portOwnText + "-debug",
			})
		}},
	}
	codes := declaredFailureCodes()

	wires := portFailureWires{}
	defer wires.requireOnePerCode(t)
	var judged, expected, skipped, twins int
	for _, port := range proofPorts() {
		for _, op := range port.ops {
			t.Run(port.name+"/"+op.name+"/близнец: порт исправен", func(t *testing.T) {
				run := port.run(t, op, rightProof(), nil)
				if !run.done || run.err != nil {
					t.Fatalf("операция не исполнилась: исполнилась=%v, отказ %v", run.done, run.err)
				}
				if op.gone != nil && !op.gone(t, run.healthy, run.subject) {
					t.Error("отзыв исполнен, а токен после него годен")
				}
			})
			t.Run(port.name+"/"+op.name+"/близнец: простая ошибка", func(t *testing.T) {
				run := port.run(t, op, rightProof(), errors.New(portOwnText+": connection refused"))
				if run.done || run.err == nil {
					t.Fatalf("операция исполнилась при отказавшем порте: %v", run.err)
				}
				if got := oauthceremony.CodeOf(run.err); got != oauthceremony.CodeServerError {
					t.Errorf("простая ошибка порта названа случаем %v, ожидался %v: %v", got, oauthceremony.CodeServerError, run.err)
				}
				wires.add(t, port.name+"/"+op.name+"/простая ошибка", run.err, false, portOwnText)
			})
			twins += 2

			for _, proof := range port.proofs {
				for _, code := range codes {
					for _, form := range forms {
						if code == port.declared {
							skipped++
							continue
						}
						expected++
						name := port.name + "/" + op.name + "/" + proof.clientID + "/" + code.String() + "/" + form.name
						t.Run(name, func(t *testing.T) {
							judged++
							injected := form.of(code)
							run := port.run(t, op, proof, injected)
							if run.done || run.err == nil {
								t.Fatalf("операция исполнилась, когда порт ответил случаем %v: исполнилась=%v, отказ %v", code, run.done, run.err)
							}
							if got := oauthceremony.CodeOf(run.err); got != oauthceremony.CodePortContract {
								t.Errorf("отказ порта со случаем %v стал отказом операции случаем %v, ожидался %v: %v",
									code, got, oauthceremony.CodePortContract, run.err)
							}
							if code != oauthceremony.CodePortContract && errors.Is(run.err, &oauthceremony.ProtocolError{Code: code}) {
								t.Errorf("отказ операции несёт случай порта %v в цепочке: errors.Is прочтёт его как исход протокола: %v", code, run.err)
							}
							wires.add(t, name, run.err, true, portOwnText)
						})
					}
				}
			}
		}
	}
	t.Logf("перепись: портов %d · случаев %d · форм %d · судимо %d из %d · объявленных контрактом пропущено %d · близнецов %d",
		len(proofPorts()), len(codes), len(forms), judged, expected, skipped, twins)
	if len(codes) == 0 || judged == 0 || judged != expected || skipped == 0 || twins == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: случаев %d, судимо %d из %d, пропущено %d, близнецов %d", len(codes), judged, expected, skipped, twins)
	}
}

// flakyDirectory — справочник, который на первые healthy вопросов отвечает
// исправно (из inner), а на остальные — отказом err: клиент доказывает себя,
// а запрос движка по гранту уже не собрать.
type flakyDirectory struct {
	inner   oauthceremony.ClientDirectory
	healthy int
	err     error

	mu    sync.Mutex
	calls int
}

func (d *flakyDirectory) LookupClient(ctx context.Context, clientID string) (oauthceremony.ClientRegistration, error) {
	d.mu.Lock()
	d.calls++
	failing := d.err != nil && d.calls > d.healthy
	d.mu.Unlock()
	if failing {
		return oauthceremony.ClientRegistration{}, d.err
	}
	return d.inner.LookupClient(ctx, clientID)
}

// refusals — сколько вопросов справочник отверг.
func (d *flakyDirectory) refusals() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err == nil || d.calls <= d.healthy {
		return 0
	}
	return d.calls - d.healthy
}

// grantLookupFailures — отказы справочника, который отвечает уже после
// доказательства клиента: простой сбой, срок и каждый случай пакета, кроме
// «клиента нет». «Клиента нет» у записи гранта — сигнал контракта (клиента
// сняли), и его судят TestArtifactOfARemovedClientIsAnInvalidArtifact (артефакт
// без повтора) и TestReplayOfATokenWhoseClientIsGoneStillRevokesTheFamily
// (повтор).
func grantLookupFailures() []struct {
	name string
	err  error
	want oauthceremony.FailureCode
} {
	failures := []struct {
		name string
		err  error
		want oauthceremony.FailureCode
	}{
		{name: "справочник не отвечает", err: errors.New(directoryDownText), want: oauthceremony.CodeServerError},
		{name: "срок вызова истёк", err: context.DeadlineExceeded, want: oauthceremony.CodePortDeadline},
	}
	for _, code := range declaredFailureCodes() {
		if code == oauthceremony.CodeGrantNotFound {
			continue
		}
		failures = append(failures, struct {
			name string
			err  error
			want oauthceremony.FailureCode
		}{name: "случай пакета " + code.String(), err: &oauthceremony.ProtocolError{Code: code}, want: oauthceremony.CodePortContract})
	}
	return failures
}

// TestDirectoryFailureAfterTheClientProofFailsTheOperation — справочник,
// отказавший ПОСЛЕ доказательства клиента, когда мост собирает запрос движка
// по записи гранта, — тоже отказ операции, и случаем того же закрытого
// перечня, что отказ на доказательстве. Движок этот отказ сжимает сам: у
// интроспекции — в «токен негоден», у отзыва — во «временно недоступно», — и
// без записи в ведомость сбой справочника стал бы ответом «негоден» о годном
// токене.
//
// Близнец — тот же справочник без отказа: операция исполняется. Против него
// меняется ровно один факт — отказ справочника на вопросе после первого.
func TestDirectoryFailureAfterTheClientProofFailsTheOperation(t *testing.T) {
	wires := portFailureWires{}
	defer wires.requireOnePerCode(t)
	failures := grantLookupFailures()

	var judged int
	for _, op := range authenticatedOperations() {
		for _, failure := range append([]struct {
			name string
			err  error
			want oauthceremony.FailureCode
		}{{name: "близнец: справочник исправен"}}, failures...) {
			judged++
			name := op.name + "/" + failure.name
			t.Run(name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				subject := op.prepare(t, newTestCeremony(t, store.ports()))
				directory := &flakyDirectory{inner: store, healthy: 1, err: failure.err}
				ports := store.ports()
				ports.Clients = directory

				done, err := op.perform(newTestCeremony(t, ports), subject, rightProof())
				if failure.err == nil {
					if !done || err != nil {
						t.Fatalf("операция не исполнилась: исполнилась=%v, отказ %v", done, err)
					}
					return
				}
				if directory.refusals() == 0 {
					t.Fatal("ПРЕДПОСЫЛКА: справочник не спросили после доказательства клиента — судить нечего")
				}
				if done || err == nil {
					t.Fatalf("операция исполнилась при отказавшем справочнике: исполнилась=%v, отказ %v", done, err)
				}
				if got := oauthceremony.CodeOf(err); got != failure.want {
					t.Errorf("отказ справочника назван случаем %v, ожидался %v: %v", got, failure.want, err)
				}
				var injected *oauthceremony.ProtocolError
				if errors.As(failure.err, &injected) && injected.Code != oauthceremony.CodePortContract && errors.Is(err, injected) {
					t.Errorf("отказ операции несёт случай справочника %v в цепочке: %v", injected.Code, err)
				}
				wires.add(t, name, err, failure.want == oauthceremony.CodePortContract, directoryDownText)
			})
		}
	}
	t.Logf("перепись: операций %d · отказов %d · судимо %d", len(authenticatedOperations()), len(failures), judged)
	if judged == 0 || judged != len(authenticatedOperations())*(len(failures)+1) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d", judged)
	}
}

// TestReplayOutlivesADirectoryFailureWhileBuildingTheRequest — повтор,
// замеченный мостом ДО сборки запроса движка, остаётся ответом операции и
// тогда, когда справочник отказал на сборке: отказ сборки — следствие, а
// семейство отзывается по гранту, записанному до неё. Отзыв обёрнутым токеном
// при этом исполнен — и потому отвечает успехом.
//
// Близнец каждого пути — тот же повтор с исправным справочником: ответ и
// отзыв те же. Против него меняется ровно один факт — отказ справочника.
func TestReplayOutlivesADirectoryFailureWhileBuildingTheRequest(t *testing.T) {
	type path struct {
		name string
		// replay проходит путь до повтора над ceremonyOver(ports) и отдаёт
		// грант семейства и исход повтора.
		replay func(t *testing.T, store *memoryPorts, replayPorts oauthceremony.Ports) (grantID string, err error)
		// judge судит исход повтора.
		judge func(t *testing.T, err error)
		// reason — причина, с которой отозвано семейство.
		reason oauthceremony.RevocationReason
	}
	paths := []path{
		{
			name: "повтор кода",
			replay: func(t *testing.T, store *memoryPorts, replayPorts oauthceremony.Ports) (string, error) {
				ceremony := newTestCeremony(t, store.ports())
				code, _ := issueCode(t, ceremony)
				first, err := ceremony.Exchange(context.Background(), codeExchange(code))
				if err != nil {
					t.Fatalf("ПРЕДПОСЫЛКА: первый обмен отказал: %v", err)
				}
				grantID := grantOf(t, ceremony, first.AccessToken)
				_, err = newTestCeremony(t, replayPorts).Exchange(context.Background(), codeExchange(code))
				return grantID, err
			},
			judge:  requireCodeReplayRefusal,
			reason: oauthceremony.RevocationCodeReplay,
		},
		{
			name: "повтор токена обновления",
			replay: func(t *testing.T, store *memoryPorts, replayPorts oauthceremony.Ports) (string, error) {
				ceremony := newTestCeremony(t, store.ports())
				first := exchangeCode(t, ceremony)
				grantID := grantOf(t, ceremony, first.AccessToken)
				if _, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken)); err != nil {
					t.Fatalf("ПРЕДПОСЫЛКА: оборот отказал: %v", err)
				}
				_, err := newTestCeremony(t, replayPorts).Exchange(context.Background(), refreshRequest(first.RefreshToken))
				return grantID, err
			},
			judge:  requireReplayRefusal,
			reason: oauthceremony.RevocationRefreshReplay,
		},
		{
			name: "отзыв обёрнутым токеном",
			replay: func(t *testing.T, store *memoryPorts, replayPorts oauthceremony.Ports) (string, error) {
				ceremony := newTestCeremony(t, store.ports())
				first := exchangeCode(t, ceremony)
				grantID := grantOf(t, ceremony, first.AccessToken)
				if _, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken)); err != nil {
					t.Fatalf("ПРЕДПОСЫЛКА: оборот отказал: %v", err)
				}
				return grantID, newTestCeremony(t, replayPorts).Revoke(context.Background(), oauthceremony.RevocationRequest{
					Token:        first.RefreshToken,
					KindHint:     oauthceremony.TokenKindRefresh,
					ClientID:     testClientID,
					ClientSecret: testSecret,
					AuthMethod:   oauthceremony.ClientAuthBasic,
				})
			},
			judge: func(t *testing.T, err error) {
				t.Helper()
				if err != nil {
					t.Errorf("отзыв обёрнутым токеном отказал, хотя семейство отозвано: %v", err)
				}
			},
			reason: oauthceremony.RevocationClientRevoke,
		},
	}

	var judged int
	for _, p := range paths {
		for _, directoryDown := range []bool{false, true} {
			judged++
			name := p.name + "/близнец: справочник исправен"
			if directoryDown {
				name = p.name + "/справочник отказал на сборке запроса"
			}
			t.Run(name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				directory := &flakyDirectory{inner: store, healthy: 1}
				if directoryDown {
					directory.err = errors.New(directoryDownText)
				}
				ports := store.ports()
				ports.Clients = directory

				grantID, err := p.replay(t, store, ports)
				if directoryDown && directory.refusals() == 0 {
					t.Fatal("ПРЕДПОСЫЛКА: справочник не спросили после доказательства клиента — судить нечего")
				}
				p.judge(t, err)
				if !store.familyRevoked(grantID) {
					t.Error("семейство повторённого артефакта пережило повтор")
				}
				requireRevokedFor(t, store, grantID, p.reason)
			})
		}
	}
	t.Logf("перепись: путей %d · судимо %d", len(paths), judged)
	if judged == 0 || judged != 2*len(paths) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d", judged)
	}
}
