// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// Правка Х3 NTF-4 (Р17): словарь исходов «подтвердить» расширен ОДНИМ
// терминальным видом SUPPRESSED(reason), причины закрыты — ровно четыре.
// Перечень выписан здесь из приёмки (Р17), а не выведен из дерева: держатель
// сверяет с ним каждый источник словаря.
const suppressedWord = "suppressed"

func suppressedReasonWords() []string {
	return []string{"complaint", "hard_bounce", "soft_bounce", "unsubscribe"}
}

// contractKind — значение вида исхода контракта по имени. Значения нет —
// проба падает на нём, называя имя: стабы контракта не несут предмета Х3.
// Зовётся ПОСЛЕ сборки фикстуры: отказ фикстуры не выдаёт себя за отсутствие
// значения.
func contractKind(t *testing.T, name string) notifyv1.OutcomeKind {
	t.Helper()
	v := notifyv1.OutcomeKind(0).Descriptor().Values().ByName(protoreflect.Name(name))
	if v == nil {
		t.Fatalf("в контракте corelib.notify нет вида исхода OutcomeKind %s: стабы api/corelib/notify не несут исхода Х3", name)
	}
	return notifyv1.OutcomeKind(v.Number())
}

// contractReason — значение причины исхода контракта по имени (см. contractKind).
func contractReason(t *testing.T, name string) notifyv1.OutcomeReason {
	t.Helper()
	v := notifyv1.OutcomeReason(0).Descriptor().Values().ByName(protoreflect.Name(name))
	if v == nil {
		t.Fatalf("в контракте corelib.notify нет причины исхода OutcomeReason %s: стабы api/corelib/notify не несут причины Х3", name)
	}
	return notifyv1.OutcomeReason(v.Number())
}

// vocabulary — источники закрытого словаря исходов, как их читает держатель.
// Слова — в нижнем регистре, как в схеме и метках.
type vocabulary struct {
	contractKinds   []string
	contractReasons []string
	states          []string
	pairs           map[string][]string
	feedKinds       []string
	feedReasons     []string
}

func liveVocabulary() vocabulary {
	v := vocabulary{states: schema.States(), pairs: schema.OutcomePairs()}
	for n, name := range notifyv1.OutcomeKind_name {
		if n != 0 {
			v.contractKinds = append(v.contractKinds, strings.ToLower(name))
		}
	}
	for n, name := range notifyv1.OutcomeReason_name {
		if n != 0 {
			v.contractReasons = append(v.contractReasons, strings.ToLower(name))
		}
	}
	for _, k := range feed.Kinds() {
		v.feedKinds = append(v.feedKinds, string(k))
	}
	for _, r := range feed.Reasons() {
		v.feedReasons = append(v.feedReasons, string(r))
	}
	return v
}

// census — объём осмотренного по источникам: «находок 0» отличимо от
// «прочитано 0».
func (v vocabulary) census() string {
	n := 0
	for _, rs := range v.pairs {
		n += len(rs)
	}
	return fmt.Sprintf("видов контракта %d, причин контракта %d, состояний схемы %d, клеток таблицы сочетаний %d, видов ленты %d, причин ленты %d",
		len(v.contractKinds), len(v.contractReasons), len(v.states), n, len(v.feedKinds), len(v.feedReasons))
}

// suppressedVocabularyFindings — держатель закрытого словаря Х3: каждый
// источник знает вид SUPPRESSED, у него в таблице сочетаний ровно четыре
// причины Р17, и ни одна из четырёх не допущена при другом состоянии. Пустой
// источник — находка, а не вакуумный успех.
func suppressedVocabularyFindings(v vocabulary) []string {
	var out []string
	empty := func(name string, n int) bool {
		if n == 0 {
			out = append(out, name+": источник пуст — словарь не прочитан")
			return true
		}
		return false
	}
	need := func(name string, have []string, words ...string) {
		if empty(name, len(have)) {
			return
		}
		for _, w := range words {
			if !slices.Contains(have, w) {
				out = append(out, fmt.Sprintf("%s: нет слова %q", name, w))
			}
		}
	}
	need("контракт OutcomeKind", v.contractKinds, suppressedWord)
	need("контракт OutcomeReason", v.contractReasons, suppressedReasonWords()...)
	need("схема States", v.states, suppressedWord)
	need("лента Kinds", v.feedKinds, suppressedWord)
	need("лента Reasons", v.feedReasons, suppressedReasonWords()...)

	if !empty("схема OutcomePairs", len(v.pairs)) {
		got, ok := v.pairs[suppressedWord]
		if !ok {
			out = append(out, fmt.Sprintf("схема OutcomePairs: нет состояния %q", suppressedWord))
		}
		for _, w := range suppressedReasonWords() {
			if ok && !slices.Contains(got, w) {
				out = append(out, fmt.Sprintf("схема OutcomePairs[%s]: нет причины %q", suppressedWord, w))
			}
		}
		for _, w := range got {
			if !slices.Contains(suppressedReasonWords(), w) {
				out = append(out, fmt.Sprintf("схема OutcomePairs[%s]: причина %q вне закрытого перечня Р17", suppressedWord, w))
			}
		}
		states := make([]string, 0, len(v.pairs))
		for s := range v.pairs {
			states = append(states, s)
		}
		sort.Strings(states)
		for _, s := range states {
			if s == suppressedWord {
				continue
			}
			for _, w := range v.pairs[s] {
				if slices.Contains(suppressedReasonWords(), w) {
					out = append(out, fmt.Sprintf("схема OutcomePairs[%s]: причина подавления %q допущена при другом состоянии", s, w))
				}
			}
		}
	}
	return out
}

// DoD S2 п.2 NTF-4: держатель закрытого словаря знает SUPPRESSED(reason) с
// четырьмя причинами — на живом дереве находок 0.
func TestNTF4X3_ClosedVocabularyKnowsSuppressedWithFourReasons(t *testing.T) {
	v := liveVocabulary()
	t.Logf("перепись: %s", v.census())
	findings := suppressedVocabularyFindings(v)
	require.Empty(t, findings, "словарь исходов Х3 неполон:\n%s", strings.Join(findings, "\n"))
}

// completeVocabulary — синтетика: словарь в форме, которую требует Х3.
// Строится в пробе, а не из живого дерева: самопроверка держателя не зависит
// от того, посажена ли правка.
func completeVocabulary() vocabulary {
	reasons := append([]string{"revoked", "attrs_invalid", "unclaimed"}, suppressedReasonWords()...)
	return vocabulary{
		contractKinds:   []string{"sent", "defer", "denied", "expired", suppressedWord},
		contractReasons: reasons,
		states:          []string{"pending", "sent", "denied", "expired", suppressedWord},
		pairs: map[string][]string{
			"pending": nil, "sent": nil, "denied": {"revoked"}, "expired": {"unclaimed"},
			suppressedWord: suppressedReasonWords(),
		},
		feedKinds:   []string{"sent", "defer", "denied", "expired", suppressedWord},
		feedReasons: reasons,
	}
}

func without(ws []string, drop string) []string {
	return slices.DeleteFunc(slices.Clone(ws), func(w string) bool { return w == drop })
}

// Инъекция в обе стороны: на законном словаре держатель молчит; каждая
// уроненная причина и уроненный вид в каждом источнике, причина сверх
// перечня, причина подавления при другом состоянии и пустой источник —
// находка с именем источника и слова.
func TestNTF4X3_VocabularyHolderIsProvenByInjection(t *testing.T) {
	require.Empty(t, suppressedVocabularyFindings(completeVocabulary()), "близнец: законный словарь")

	type injection struct {
		name   string
		mutate func(*vocabulary)
		want   string
	}
	var cases []injection
	for _, w := range suppressedReasonWords() {
		cases = append(cases,
			injection{"контракт без " + w, func(v *vocabulary) { v.contractReasons = without(v.contractReasons, w) },
				fmt.Sprintf("контракт OutcomeReason: нет слова %q", w)},
			injection{"лента без " + w, func(v *vocabulary) { v.feedReasons = without(v.feedReasons, w) },
				fmt.Sprintf("лента Reasons: нет слова %q", w)},
			injection{"таблица без " + w, func(v *vocabulary) {
				v.pairs[suppressedWord] = without(v.pairs[suppressedWord], w)
			}, fmt.Sprintf("схема OutcomePairs[suppressed]: нет причины %q", w)},
		)
	}
	cases = append(cases,
		injection{"контракт без вида", func(v *vocabulary) { v.contractKinds = without(v.contractKinds, suppressedWord) },
			`контракт OutcomeKind: нет слова "suppressed"`},
		injection{"схема без состояния", func(v *vocabulary) { v.states = without(v.states, suppressedWord) },
			`схема States: нет слова "suppressed"`},
		injection{"лента без вида", func(v *vocabulary) { v.feedKinds = without(v.feedKinds, suppressedWord) },
			`лента Kinds: нет слова "suppressed"`},
		injection{"таблица без строки", func(v *vocabulary) { delete(v.pairs, suppressedWord) },
			`схема OutcomePairs: нет состояния "suppressed"`},
		injection{"пятая причина", func(v *vocabulary) {
			v.pairs[suppressedWord] = append(slices.Clone(v.pairs[suppressedWord]), "ceiling")
		}, `схема OutcomePairs[suppressed]: причина "ceiling" вне закрытого перечня Р17`},
		injection{"причина подавления у expired", func(v *vocabulary) {
			v.pairs["expired"] = append(slices.Clone(v.pairs["expired"]), "complaint")
		}, `схема OutcomePairs[expired]: причина подавления "complaint" допущена при другом состоянии`},
		injection{"пустой контракт", func(v *vocabulary) { v.contractReasons = nil },
			"контракт OutcomeReason: источник пуст — словарь не прочитан"},
		injection{"пустая таблица", func(v *vocabulary) { v.pairs = nil },
			"схема OutcomePairs: источник пуст — словарь не прочитан"},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := completeVocabulary()
			tc.mutate(&v)
			findings := suppressedVocabularyFindings(v)
			require.Contains(t, findings, tc.want, "находки: %q", findings)
		})
	}
	t.Logf("инъекций %d, близнец 1", len(cases))
}

// Р17, NTF1 (границы Ack до SQL): SUPPRESSED несёт ровно причины Р17 и только
// их; причины Р17 не допустимы при другом виде. Положительный близнец —
// SUPPRESSED(hard_bounce) проходит границы и доходит до базы (одно обращение).
func TestNTF4X3_AckSuppressedBoundsAreRefusedBeforeSQL(t *testing.T) {
	const id = "ntf-0123456789abcdefg"
	s, db := validatingServer(t)
	require.Zero(t, db.calls.Load())

	suppressed := contractKind(t, "SUPPRESSED")
	hard := contractReason(t, "HARD_BOUNCE")
	complaint := contractReason(t, "COMPLAINT")

	_, err := s.Ack(context.Background(), &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
		Outcome: &notifyv1.Outcome{Kind: suppressed, Reason: hard}})
	require.Equal(t, int32(1), db.calls.Load(), "близнец: SUPPRESSED(HARD_BOUNCE) обязан дойти до базы; отказ: %v", err)

	for _, tc := range []struct {
		name  string
		req   *notifyv1.AckRequest
		text  string
		field string
	}{
		{"SUPPRESSED без причины", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: suppressed}},
			"outcome.reason: OUTCOME_REASON_UNSPECIFIED is not allowed with SUPPRESSED", "outcome.reason"},
		{"SUPPRESSED с причиной другого вида", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: suppressed, Reason: notifyv1.OutcomeReason_REVOKED}},
			"outcome.reason: REVOKED is not allowed with SUPPRESSED", "outcome.reason"},
		{"SUPPRESSED с причиной уборщика", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: suppressed, Reason: notifyv1.OutcomeReason_UNCLAIMED}},
			"outcome.reason: UNCLAIMED is not allowed with SUPPRESSED", "outcome.reason"},
		{"SUPPRESSED с defer_for", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: suppressed, Reason: hard}, DeferFor: durationpb.New(time.Minute)},
			"defer_for: must not be set unless outcome.kind is DEFER", "defer_for"},
		{"DEFER с причиной подавления", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_DEFER, Reason: hard}, DeferFor: durationpb.New(time.Minute)},
			"outcome.reason: HARD_BOUNCE is not allowed with DEFER", "outcome.reason"},
		{"INVALID с причиной подавления", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_INVALID, Reason: complaint}},
			"outcome.reason: COMPLAINT is not allowed with INVALID", "outcome.reason"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := validatingServer(t)
			_, err := s.Ack(context.Background(), tc.req)
			code, text, field := refusal(t, err)
			require.Equal(t, codes.InvalidArgument, code, "%v", err)
			require.Equal(t, tc.text, text)
			require.Equal(t, tc.field, field)
			require.Zero(t, db.calls.Load(), "граница входа дошла до базы")
		})
	}
}
