// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// coverage_test.go — СОСТАВНАЯ половина держателя выреза телеметрии, по
// живому поддереву.
//
// Судятся два утверждения о КЛАССЕ, а не о перечне форм:
//
//  1. каждое открытие спана в поддереве закрывается через обёртку ФУНДАМЕНТА;
//  2. множество имён спанов в поддереве РАВНО множеству, которое гоняет
//     поведенческая половина (enginespan_test.go), — в обе стороны.
//
// Способность суда упасть и смолчать доказана на синтетике рядом
// (judge_injection_test.go); здесь — правило этого дерева и сверка переписи
// со знаменателем.
package tracecut_test

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/gitenv"
	"github.com/PRO-Robotech/corelib/internal/tracecut"
	"github.com/PRO-Robotech/corelib/treecorpus"
)

// subtreeDir — каталог внесённого движка относительно этого пакета.
const subtreeDir = "../oauth2"

// judgeSubtree судит отслеживаемые файлы Go внесённого поддерева.
func judgeSubtree(t *testing.T) tracecut.Report {
	t.Helper()
	files, err := treecorpus.UnderWithSuffix(subtreeDir, ".go")
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: состав %s не снялся: %v", subtreeDir, err)
	}
	sources := make([]tracecut.Source, 0, len(files))
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s не прочитался: %v", f, err)
		}
		sources = append(sources, tracecut.Source{Path: f, Src: src})
	}
	rep, err := tracecut.Judge(sources, wrapper)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
	}
	return rep
}

// trackedGoFiles — знаменатель ДРУГИМ выражением: отбор образцом git, а не
// суффиксом после обхода.
func trackedGoFiles(t *testing.T) int {
	t.Helper()
	out, err := gitenv.Command(subtreeDir, "ls-files", "-z", "--", "*.go").Output()
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: git ls-files в %s: %v", subtreeDir, err)
	}
	n := 0
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			n++
		}
	}
	return n
}

// TestEverySubtreeSpanIsClosedByTheFoundationWrapper — вырез НЕ РАЗЪЕХАЛСЯ.
func TestEverySubtreeSpanIsClosedByTheFoundationWrapper(t *testing.T) {
	rep := judgeSubtree(t)
	t.Logf("%s: %s", subtreeDir, rep.Census)
	if want := trackedGoFiles(t); rep.Census.Files != want {
		t.Errorf("суд прочитал файлов .go %d, а в индексе их %d — перепись разошлась со знаменателем",
			rep.Census.Files, want)
	}
	for _, f := range rep.Findings {
		t.Errorf("%s", f)
	}
}

// TestBehaviouralPathListEqualsTheSubtreeComposition — перечень НЕ ОТСТАЁТ.
//
// Лишнее имя и недостающее имя — разные отказы с разной починкой, поэтому
// печатаются поимённо и порознь.
func TestBehaviouralPathListEqualsTheSubtreeComposition(t *testing.T) {
	inTree := map[string]bool{}
	for _, o := range judgeSubtree(t).Openings {
		inTree[o.Name] = true
	}
	inProbe := map[string]bool{}
	for _, p := range enginePaths() {
		inProbe[p.span] = true
	}

	var undriven, stale []string
	for name := range inTree {
		if !inProbe[name] {
			undriven = append(undriven, name)
		}
	}
	for name := range inProbe {
		if !inTree[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(undriven)
	sort.Strings(stale)

	if len(undriven) != 0 {
		t.Errorf("движок открывает спаны, которых поведенческая проба НЕ ГОНЯЕТ: %q. "+
			"Пустая обёртка на этих путях осталась бы зелёной — заведи им случай в "+
			"enginePaths (enginespan_test.go)", undriven)
	}
	if len(stale) != 0 {
		t.Errorf("поведенческая проба гоняет спаны, которых в %s больше НЕТ: %q — "+
			"перечень обязан догнать дерево", subtreeDir, stale)
	}
	t.Logf("имён спанов в дереве: %d; гоняет поведенческая проба: %d", len(inTree), len(inProbe))
}
