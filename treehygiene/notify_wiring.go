// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_wiring.go — NTF1-D08 (З31): CI дерева исполняет `-check -base`
// против первого родителя и гейты дерева; снятый шаг — красный.
//
// Ведомость обязательных вызовов объявлена здесь однажды — две записи; тонкие
// вызывающие kacho и kaname зовут AuditNotifyWiring со своим каталогом
// рабочих процессов и Makefile и копии ведомости не держат. Рабочие процессы
// разбираются YAML, тела run: — словами оболочки, а не поиском подстроки.
// По каждой записи нужен шаг: в рабочем процессе с триггерами pull_request и
// push; тело — ровно слова записи; у шага и задания нет if: и нет
// continue-on-error: true; checkout задания — fetch-depth: 0; `make -n
// <цель>` — код 0. Гейт не пропускается при -short.
package treehygiene

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// notifyWiringLedger — ведомость D08: слова тела шага.
var notifyWiringLedger = []string{
	"make notifications-check BASE=HEAD^1",
	"make notify-tree-gates",
}

// NotifyWiringLedger — ведомость D08 (копия).
func NotifyWiringLedger() []string { return slices.Clone(notifyWiringLedger) }

// WiringReport — исход гейта по одному дереву.
type WiringReport struct {
	Corelib string
	// Workflows, Jobs, Steps — осмотрено файлов, заданий, шагов.
	Workflows, Jobs, Steps int
	// Found — найдено шагов по каждой записи ведомости.
	Found    map[string]int
	Findings []Finding
}

func (r WiringReport) String() string {
	parts := make([]string, 0, len(notifyWiringLedger))
	for _, rec := range notifyWiringLedger {
		parts = append(parts, fmt.Sprintf("«%s» %d", rec, r.Found[rec]))
	}
	return fmt.Sprintf("вызов проверок NTF-1: corelib %s · записей ведомости %d · рабочих процессов %d · заданий %d · шагов %d · найдено: %s",
		r.Corelib, len(notifyWiringLedger), r.Workflows, r.Jobs, r.Steps, strings.Join(parts, ", "))
}

type wfStep struct {
	line              int
	run, uses, ifCond string
	continueOnErr     bool
	with              map[string]string
}

type wfJob struct {
	name          string
	ifCond        string
	continueOnErr bool
	steps         []wfStep
}

type workflow struct {
	file     string
	triggers map[string]bool
	jobs     []wfJob
}

// AuditNotifyWiring — D08 по каталогу рабочих процессов workflowDir и
// Makefile дерева.
func AuditNotifyWiring(workflowDir, makefile string) (WiringReport, error) {
	r := WiringReport{Corelib: CorelibVersion(), Found: map[string]int{}}
	for _, rec := range notifyWiringLedger {
		r.Found[rec] = 0
	}
	entries, err := os.ReadDir(workflowDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return r, fmt.Errorf("treehygiene: каталог рабочих процессов %s не читается: %w", workflowDir, err)
	}
	var wfs []workflow
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		p := filepath.Join(workflowDir, e.Name())
		w, err := parseWorkflow(p)
		if err != nil {
			return r, err
		}
		wfs = append(wfs, w)
	}
	if len(wfs) == 0 {
		return r, fmt.Errorf("treehygiene: в %s нет ни одного рабочего процесса — пустой обход не вердикт", workflowDir)
	}
	add := func(pos, rec, why string) {
		r.Findings = append(r.Findings, Finding{Position: pos, Kind: "notify-wiring", Why: "«" + rec + "»: " + why})
	}
	var files []string
	for _, w := range wfs {
		files = append(files, w.file)
		r.Workflows++
		for _, j := range w.jobs {
			r.Jobs++
			r.Steps += len(j.steps)
			for _, s := range j.steps {
				if s.run == "" {
					continue
				}
				words, werr := shellWords(s.run)
				for _, rec := range notifyWiringLedger {
					want := strings.Fields(rec)
					pos := fmt.Sprintf("%s:%d", w.file, s.line)
					if werr != nil || !slices.Equal(words, want) {
						if nearMiss(s.run, want) {
							why := "тело шага не равно словам записи"
							if want[1] == "notifications-check" && slices.Contains(words, "notifications-check") && !slices.Contains(words, want[2]) {
								why = "база не первый родитель (" + strings.TrimSpace(s.run) + ")"
							}
							add(pos, rec, why)
						}
						continue
					}
					r.Found[rec]++
					judgeStep(w, j, s, pos, rec, add)
				}
			}
		}
	}
	for _, rec := range notifyWiringLedger {
		if r.Found[rec] == 0 {
			add(strings.Join(files, ","), rec, "задания нет — шага с телом записи нет ни в одном рабочем процессе")
		}
		want := strings.Fields(rec)
		args := append([]string{"-n", "-f", filepath.Base(makefile)}, want[1:]...)
		cmd := exec.Command("make", args...) // #nosec G204 -- argv[0] фиксирован, цель — слово ведомости, Makefile — дерева вызывающего
		cmd.Dir = filepath.Dir(makefile)
		cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
		out, err := cmd.CombinedOutput()
		switch {
		case err != nil:
			add(makefile, rec, fmt.Sprintf("цели нет: make -n %s — %v: %s", want[1], err, strings.TrimSpace(string(out))))
		case strings.TrimSpace(string(out)) == "" || strings.Contains(string(out), "Nothing to be done"):
			// Цель объявлена (.PHONY), а правила нет: make -n выходит нулём и не
			// исполняет ничего.
			add(makefile, rec, "цели нет: make -n "+want[1]+" не исполняет ни одной команды")
		}
	}
	sortFindings(r.Findings)
	return r, nil
}

// nearMiss — шаг зовёт ту же цель make, но телом не равен записи.
func nearMiss(run string, want []string) bool {
	return strings.Contains(run, want[0]) && strings.Contains(run, want[1])
}

func judgeStep(w workflow, j wfJob, s wfStep, pos, rec string, add func(pos, rec, why string)) {
	for _, t := range []string{"pull_request", "push"} {
		if !w.triggers[t] {
			add(pos, rec, "у рабочего процесса нет триггера "+t)
		}
	}
	if s.ifCond != "" {
		add(pos, rec, "у шага if: "+s.ifCond)
	}
	if j.ifCond != "" {
		add(pos, rec, "у задания "+j.name+" if: "+j.ifCond)
	}
	if s.continueOnErr {
		add(pos, rec, "у шага continue-on-error: true")
	}
	if j.continueOnErr {
		add(pos, rec, "у задания "+j.name+" continue-on-error: true")
	}
	checkout := false
	for _, st := range j.steps {
		if strings.HasPrefix(st.uses, "actions/checkout@") {
			checkout = true
			if st.with["fetch-depth"] != "0" {
				add(pos, rec, "checkout задания "+j.name+" — fetch-depth ≠ 0 ("+st.with["fetch-depth"]+"): первого родителя в клоне нет")
			}
		}
	}
	if !checkout {
		add(pos, rec, "у задания "+j.name+" нет checkout")
	}
}

func parseWorkflow(path string) (workflow, error) {
	w := workflow{file: path, triggers: map[string]bool{}}
	data, err := os.ReadFile(path) // #nosec G304 -- файл каталога рабочих процессов дерева вызывающего, гейт сборки
	if err != nil {
		return w, fmt.Errorf("treehygiene: %s не читается: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return w, fmt.Errorf("treehygiene: %s не разбирается YAML: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return w, fmt.Errorf("treehygiene: %s — не отображение верхнего уровня", path)
	}
	top := doc.Content[0]
	if on := mapGet(top, "on"); on != nil {
		switch on.Kind {
		case yaml.ScalarNode:
			w.triggers[on.Value] = true
		case yaml.SequenceNode:
			for _, n := range on.Content {
				w.triggers[n.Value] = true
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(on.Content); i += 2 {
				w.triggers[on.Content[i].Value] = true
			}
		}
	}
	jobs := mapGet(top, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return w, nil
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		jn := jobs.Content[i+1]
		j := wfJob{name: jobs.Content[i].Value, ifCond: scalar(mapGet(jn, "if")), continueOnErr: isTrue(mapGet(jn, "continue-on-error"))}
		if steps := mapGet(jn, "steps"); steps != nil && steps.Kind == yaml.SequenceNode {
			for _, sn := range steps.Content {
				s := wfStep{line: sn.Line, run: scalar(mapGet(sn, "run")), uses: scalar(mapGet(sn, "uses")),
					ifCond: scalar(mapGet(sn, "if")), continueOnErr: isTrue(mapGet(sn, "continue-on-error")),
					with: map[string]string{}}
				if with := mapGet(sn, "with"); with != nil && with.Kind == yaml.MappingNode {
					for k := 0; k+1 < len(with.Content); k += 2 {
						s.with[with.Content[k].Value] = with.Content[k+1].Value
					}
				}
				j.steps = append(j.steps, s)
			}
		}
		w.jobs = append(w.jobs, j)
	}
	return w, nil
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func isTrue(n *yaml.Node) bool { return n != nil && n.Kind == yaml.ScalarNode && n.Value == "true" }

// shellWords — слова тела run: по правилам оболочки: пробелы разделяют,
// одинарные кавычки буквальны, двойные снимают экранирование \" \\ \$ \`,
// обратная косая вне кавычек экранирует знак, перевод строки и «;» — отдельное
// слово-разделитель, «#» в начале слова — комментарий до конца строки.
func shellWords(s string) ([]string, error) {
	var (
		words []string
		cur   strings.Builder
		inW   bool
	)
	flush := func() {
		if inW {
			words = append(words, cur.String())
			cur.Reset()
			inW = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '\n' || c == ';':
			flush()
			if len(words) > 0 && words[len(words)-1] != ";" {
				words = append(words, ";")
			}
		case c == '#' && !inW:
			for i < len(s) && s[i] != '\n' {
				i++
			}
			i--
		case c == '\\':
			if i+1 < len(s) {
				i++
				if s[i] != '\n' {
					cur.WriteByte(s[i])
					inW = true
				}
			}
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("незакрытая одинарная кавычка")
			}
			cur.WriteString(s[i+1 : i+1+end])
			inW = true
			i += end + 1
		case c == '"':
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' && j+1 < len(s) && strings.ContainsRune("\"\\$`", rune(s[j+1])) {
					j++
				}
				cur.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, errors.New("незакрытая двойная кавычка")
			}
			inW = true
			i = j
		default:
			cur.WriteByte(c)
			inW = true
		}
	}
	flush()
	for len(words) > 0 && words[len(words)-1] == ";" {
		words = words[:len(words)-1]
	}
	return words, nil
}
