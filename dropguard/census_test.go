// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/dropguard"
)

// TestARunThatMeasuredNothingDoesNotLookLikeSuccess — the census requirement.
//
// Zero violations is the same output whether the guard counted every table and
// found them empty, or never reached a database at all. The only thing separating
// those two is the count of what was read, so it is printed every time, and the
// verdict word changes with it.
func TestARunThatMeasuredNothingDoesNotLookLikeSuccess(t *testing.T) {
	inv := chain(t)

	nothing := dropguard.NothingMeasured(inv)
	if len(nothing.Violations) != 0 {
		t.Fatalf("precondition: the degraded report carries no findings, got %+v", nothing.Violations)
	}
	var b strings.Builder
	nothing.WriteCensus(&b)
	out := b.String()

	if !strings.Contains(out, "NOT VERIFIED") {
		t.Errorf("a run that measured nothing did not say so:\n%s", out)
	}
	if !strings.Contains(out, "measured 0 of 4") {
		t.Errorf("the census does not give both numbers:\n%s", out)
	}
	for _, coord := range []string{"0002/scratch", "0003/catalogue", "0003/ledger", "0003/never_here"} {
		if !strings.Contains(out, coord) {
			t.Errorf("the census does not name unanswered drop %s:\n%s", coord, out)
		}
	}

	// And the opposite: a run that did the work says OK, with the same two numbers.
	done := dropguard.Report{Service: "demo", FilesScanned: 3, DropsInChain: 4, Measured: 4, Rows: map[string]int64{"0003/ledger": 0}}
	b.Reset()
	done.WriteCensus(&b)
	if got := b.String(); !strings.Contains(got, "OK") || !strings.Contains(got, "measured 4 of 4") {
		t.Errorf("a complete run does not report itself as one:\n%s", got)
	}
	if strings.Contains(b.String(), "NOT VERIFIED") {
		t.Errorf("a complete run was labelled unverified:\n%s", b.String())
	}
}

// TestAPartialRunIsNeitherGreenNorSilent — the middle case: some drops counted,
// some not. It must not round to either end.
func TestAPartialRunIsNeitherGreenNorSilent(t *testing.T) {
	partial := dropguard.Report{
		Service: "demo", FilesScanned: 3, DropsInChain: 4, Measured: 2,
		Rows:       map[string]int64{"0003/ledger": 0, "0003/catalogue": 2},
		Unmeasured: []string{"0002/scratch", "0003/never_here"},
	}
	if partial.OK() {
		t.Error("a run that reached half the drops reported itself as OK")
	}
	var b strings.Builder
	partial.WriteCensus(&b)
	out := b.String()
	if !strings.Contains(out, "PARTIAL") || !strings.Contains(out, "measured 2 of 4") {
		t.Errorf("a partial run does not say which half it did:\n%s", out)
	}
	for _, coord := range []string{"0002/scratch", "0003/never_here"} {
		if !strings.Contains(out, coord) {
			t.Errorf("the census does not name unanswered drop %s:\n%s", coord, out)
		}
	}
}

// TestAChainThatDropsNothingIsNotAScanOfNothing — «прочитано ноль» и «снятий ноль»
// суть РАЗНЫЕ состояния, и вердикт обязан их различать.
//
// # Почему это отдельная проба, а не оттенок формулировки
//
// Оба состояния дают одно и то же число снятий — ноль, — и до правки они давали
// одно и то же слово: NOTHING READ. Пока у каждого сервиса цепь была историей,
// разницы не возникало, и совпадение было невидимо. Свод цепи одного сервиса в
// одну первичную миграцию сделал «снятий ноль» ЗАКОННЫМ состоянием прочитанной
// цепи — и прежний вердикт стал объявлять полностью прочитанную цепь сканом
// пустоты, то есть тревогой на правильном ответе.
//
// Утверждение двустороннее намеренно: одна половина зеленела бы, если бы вердикт
// назвал оба состояния словом NO DROPS, вторая — если бы он вернулся к NOTHING
// READ для обоих.
func TestAChainThatDropsNothingIsNotAScanOfNothing(t *testing.T) {
	// Прочитано, снятий нет: законное состояние сведённой цепи.
	consolidated := dropguard.Report{Service: "demo", FilesScanned: 1, DropsInChain: 0, Measured: 0}
	var b strings.Builder
	consolidated.WriteCensus(&b)
	out := b.String()
	if !strings.Contains(out, "NO DROPS") {
		t.Errorf("прочитанная цепь без снятий не названа своим состоянием:\n%s", out)
	}
	if strings.Contains(out, "NOTHING READ") {
		t.Errorf("прочитанная цепь объявлена сканом пустоты — тревога на правильном ответе:\n%s", out)
	}
	if !strings.Contains(out, "read 1 migration file(s)") {
		t.Errorf("перепись не называет, сколько файлов прочитано; без этого числа два состояния "+
			"неразличимы читателем так же, как они были неразличимы вердиктом:\n%s", out)
	}

	// Законный близнец в другую сторону: не прочитано ничего. Отдельное слово
	// обязано остаться за ним, иначе различение куплено потерей прежнего.
	unread := dropguard.Report{Service: "demo", FilesScanned: 0, DropsInChain: 0, Measured: 0}
	b.Reset()
	unread.WriteCensus(&b)
	if got := b.String(); !strings.Contains(got, "NOTHING READ") {
		t.Errorf("скан, не прочитавший ни одного файла, не назван — «ноль находок» стало бы "+
			"неотличимо от «ноль прочитанного»:\n%s", got)
	}
}
