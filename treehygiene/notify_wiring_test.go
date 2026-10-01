// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

const wiringMakefile = `
.PHONY: notifications-check notify-tree-gates
notifications-check:
	go tool notifygen -check -base "$(BASE)"
notify-tree-gates:
	go test ./internal/repohygiene/ -run NotifyTree -count=1
`

const wiringWorkflow = `
name: ci
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  workflow_dispatch: {}
jobs:
  notify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: notifications-check
        run: make notifications-check BASE=HEAD^1
      - name: notify-tree-gates
        run: make notify-tree-gates
`

func wiringTree(t *testing.T, workflow, makefile string) (string, string) {
	return wiringTreeNamed(t, "ci.yaml", workflow, makefile)
}

func wiringTreeNamed(t *testing.T, name, workflow, makefile string) (string, string) {
	t.Helper()
	root := t.TempDir()
	wf := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, name), []byte(strings.TrimLeft(workflow, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := filepath.Join(root, "Makefile")
	if err := os.WriteFile(mk, []byte(strings.TrimLeft(makefile, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return wf, mk
}

func wiring(t *testing.T, workflow, makefile string) treehygiene.WiringReport {
	t.Helper()
	wf, mk := wiringTree(t, workflow, makefile)
	r, err := treehygiene.AuditNotifyWiring(wf, mk)
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, kindsOf(r.Findings))
	return r
}

// Близнец NTF1-D08: шаги ведомости на месте — гейт молчит и печатает по
// одному найденному шагу на запись.
func TestNTF1D08_TwinIsSilent(t *testing.T) {
	r := wiring(t, wiringWorkflow, wiringMakefile)
	if len(r.Findings) != 0 {
		t.Fatalf("близнец дал находки:\n%s", kindsOf(r.Findings))
	}
	if len(r.Found) != 2 {
		t.Fatalf("записей ведомости %d", len(r.Found))
	}
	for rec, n := range r.Found {
		if n != 1 {
			t.Fatalf("запись %q: шагов %d", rec, n)
		}
	}
	if r.Workflows != 1 || r.Jobs != 1 || r.Steps != 3 {
		t.Fatalf("перепись: %s", r)
	}
}

// NTF1-D08, инъекции (1)–(8): каждая — находка с записью ведомости и файлом.
func TestNTF1D08_InjectionsAreFound(t *testing.T) {
	for name, tc := range map[string]struct {
		workflow, makefile, want string
	}{
		"(1) шаг notifications-check снят": {strings.Replace(wiringWorkflow,
			"      - name: notifications-check\n        run: make notifications-check BASE=HEAD^1\n", "", 1), wiringMakefile, "задания нет"},
		"(2) BASE=origin/main": {strings.Replace(wiringWorkflow, "BASE=HEAD^1", "BASE=origin/main", 1),
			wiringMakefile, "база не первый родитель"},
		"(3) continue-on-error": {strings.Replace(wiringWorkflow,
			"run: make notifications-check BASE=HEAD^1", "run: make notifications-check BASE=HEAD^1\n        continue-on-error: true", 1),
			wiringMakefile, "continue-on-error"},
		"(4) if на шаге": {strings.Replace(wiringWorkflow,
			"run: make notify-tree-gates", "run: make notify-tree-gates\n        if: github.event_name == 'pull_request'", 1),
			wiringMakefile, "if:"},
		"(5) fetch-depth 1": {strings.Replace(wiringWorkflow, "fetch-depth: 0", "fetch-depth: 1", 1),
			wiringMakefile, "fetch-depth"},
		"(6) триггер push снят": {strings.Replace(wiringWorkflow, "  push:\n    branches: [main]\n", "", 1),
			wiringMakefile, "push"},
		"(7) шаг notify-tree-gates снят": {strings.Replace(wiringWorkflow,
			"      - name: notify-tree-gates\n        run: make notify-tree-gates\n", "", 1), wiringMakefile, "задания нет"},
		"(8) цель notifications-check снята": {wiringWorkflow, strings.Replace(wiringMakefile,
			"notifications-check:\n\tgo tool notifygen -check -base \"$(BASE)\"\n", "", 1), "цели нет"},
	} {
		t.Run(name, func(t *testing.T) {
			r := wiring(t, tc.workflow, tc.makefile)
			found := false
			for _, f := range r.Findings {
				if strings.Contains(f.Why, tc.want) && strings.Contains(f.Position, "ci.yaml") || strings.Contains(f.Why, tc.want) && strings.Contains(f.Position, "Makefile") {
					found = true
				}
			}
			if !found {
				t.Fatalf("находки «%s» нет:\n%s", tc.want, kindsOf(r.Findings))
			}
		})
	}
}

// Тело шага судится словами оболочки: кавычки и лишние пробелы — те же
// слова; иная команда в том же шаге — не запись ведомости.
func TestNTF1D08_RunBodyIsJudgedByShellWords(t *testing.T) {
	r := wiring(t, strings.Replace(wiringWorkflow, "run: make notify-tree-gates", "run: |\n          \"make\"   'notify-tree-gates'", 1), wiringMakefile)
	if len(r.Findings) != 0 {
		t.Fatalf("кавычки сменили слова:\n%s", kindsOf(r.Findings))
	}
	r = wiring(t, strings.Replace(wiringWorkflow, "run: make notify-tree-gates", "run: make notify-tree-gates || true", 1), wiringMakefile)
	if len(r.Findings) == 0 {
		t.Fatal("тело «make notify-tree-gates || true» принято за запись ведомости")
	}
}

// Пустой обход — отказ, а не зелёный.
func TestNTF1D08_EmptyWalkIsARefusal(t *testing.T) {
	root := t.TempDir()
	mk := filepath.Join(root, "Makefile")
	if err := os.WriteFile(mk, []byte(wiringMakefile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := treehygiene.AuditNotifyWiring(filepath.Join(root, "nothing"), mk); err == nil {
		t.Fatal("каталог рабочих процессов без файлов принят")
	}
}

// Ведомость D08 объявлена однажды: две записи, копия не меняет следующего
// ответа (УК55).
func TestNTF1D08_LedgerIsDeclaredOnce(t *testing.T) {
	l := treehygiene.NotifyWiringLedger()
	if len(l) != 2 {
		t.Fatalf("записей ведомости %d", len(l))
	}
	l[0] = "мусор"
	if treehygiene.NotifyWiringLedger()[0] == "мусор" {
		t.Fatal("ведомость отдана по ссылке")
	}
}

// NTF1-D08 (9): инъекция (1) в дереве kaname (рабочий процесс ci.yml) —
// находка прогона по kaname.
func TestNTF1D08_InjectionInKanameIsFound(t *testing.T) {
	wf, mk := wiringTreeNamed(t, "ci.yml", strings.Replace(wiringWorkflow,
		"      - name: notifications-check\n        run: make notifications-check BASE=HEAD^1\n", "", 1), wiringMakefile)
	r, err := treehygiene.AuditNotifyWiring(wf, mk)
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	if len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Position, "ci.yml") || !strings.Contains(r.Findings[0].Why, "задания нет") {
		t.Fatalf("находки «задания нет» по ci.yml нет:\n%s", kindsOf(r.Findings))
	}
}
