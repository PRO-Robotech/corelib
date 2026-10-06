// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// УК62, УК66: перечни меток экспортированы и закрыты. Классы и виды сверяются
// с контрактом в обе стороны: значение контракта без слова в перечне и слово
// перечня без значения контракта — оба красные. Классы LocalOnlyClasses (C5:
// obligation берёт только Local) в контракте сети не бывают и из сверки
// вычитаются явным перечнем, а не пропуском.
func TestUK62_ClassesAndKindsMatchTheContractBothWays(t *testing.T) {
	classes := map[string]bool{}
	for _, c := range feed.Classes() {
		if !slices.Contains(feed.LocalOnlyClasses(), c) {
			classes[string(c)] = true
		}
	}
	require.Len(t, classes, len(feed.Classes())-len(feed.LocalOnlyClasses()),
		"класс LocalOnlyClasses вне перечня Classes")
	contractClasses := map[string]bool{}
	for n, name := range notifyv1.NotificationClass_name {
		if n == 0 {
			continue
		}
		contractClasses[strings.ToLower(name)] = true
	}
	require.Equal(t, contractClasses, classes)

	kinds := map[string]bool{}
	for _, k := range feed.Kinds() {
		kinds[string(k)] = true
	}
	contractKinds := map[string]bool{}
	for n, name := range notifyv1.OutcomeKind_name {
		if n == 0 {
			continue
		}
		contractKinds[strings.ToLower(name)] = true
	}
	require.Equal(t, contractKinds, kinds)
	t.Logf("классов %d, видов %d", len(classes), len(kinds))
}

// УК62: перечень причин — объединение причин таблицы сочетаний схемы (Р11)
// без причин LocalOnlyOutcomes (C5: superseded ставит только Supersede) и
// равен словарю контракта без UNSPECIFIED. Второго перечня нет.
func TestUK62_ReasonsAreTheContractVocabularyAndTheSchemaTable(t *testing.T) {
	got := map[string]bool{}
	for _, r := range feed.Reasons() {
		require.NotEmpty(t, r, "пустое слово в перечне причин")
		got[string(r)] = true
	}
	want := map[string]bool{}
	for n, name := range notifyv1.OutcomeReason_name {
		if n == 0 {
			continue
		}
		want[strings.ToLower(name)] = true
	}
	require.Equal(t, want, got)

	fromSchema := map[string]bool{}
	for _, rs := range schema.OutcomePairs() {
		for _, r := range rs {
			fromSchema[r] = true
		}
	}
	withLocal := maps.Clone(got)
	for _, o := range feed.LocalOnlyOutcomes() {
		require.Contains(t, schema.OutcomePairs()[string(o.Kind)], string(o.Reason), "исход LocalOnlyOutcomes вне таблицы схемы")
		require.False(t, got[string(o.Reason)], "причина LocalOnlyOutcomes %s попала в перечень контракта", o.Reason)
		withLocal[string(o.Reason)] = true
	}
	require.Equal(t, fromSchema, withLocal, "перечень причин разошёлся с таблицей сочетаний схемы")
	t.Logf("причин %d", len(got))
}

// CX1-53 (в), УК68: перечень причин возврата объявлен однажды; предикат NTF-2
// выведен из него, а не выписан.
func TestCX153_RefundReasonsAndThePredicateDerivedFromThem(t *testing.T) {
	var words []string
	for _, r := range feed.RefundReasons() {
		words = append(words, string(r))
	}
	sort.Strings(words)
	require.Equal(t, []string{"platform_unavailable", "unclaimed"}, words)

	p, err := feed.LimitRefundedPredicate("n")
	require.NoError(t, err)
	require.Equal(t, `(n.state = 'expired' AND n.outcome_reason IN ('platform_unavailable', 'unclaimed'))`, p)

	for _, bad := range []string{"", "n; DROP TABLE x", "N", "1n", `n"`} {
		_, err := feed.LimitRefundedPredicate(bad)
		require.Error(t, err, "псевдоним %q", bad)
	}
}

// CX1-67: перечень дефектов Put объявлен однажды — сегодня одно слово.
func TestCX167_PutDefectsAreDeclaredOnce(t *testing.T) {
	require.Equal(t, []feed.PutDefect{feed.PutDefectSecondLimitedPut}, feed.PutDefects())
	require.Equal(t, "second_limited_put", string(feed.PutDefectSecondLimitedPut))
}

// УК81: срок хранения закрытых строк экспортирован; соотношение с окном
// читателя NTF-2 сверяет проба читателя по этой константе.
func TestUK81_RetentionConstantsAreExported(t *testing.T) {
	require.Equal(t, "168h0m0s", feed.ClosedRetention.String())
	require.Equal(t, "24h0m0s", feed.WindowRetention.String())
}

// NTF1-B31 (предел): константы ленты объявлены однажды и равны приёмке.
func TestNTF1B31_ConstantsMatchTheAcceptance(t *testing.T) {
	require.Equal(t, "5m0s", feed.LeaseTTL.String())
	require.Equal(t, "1m0s", feed.SweepInterval.String())
	require.Equal(t, 1000, feed.SweepBatch)
	require.Equal(t, 500, feed.MaxClaim)
	require.Equal(t, "1s", feed.MinDefer.String())
	require.Equal(t, "15m0s", feed.MaxDefer.String())
	require.Equal(t, "720h0m0s", feed.TTLMax.String())
}
