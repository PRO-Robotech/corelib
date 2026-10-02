// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// hookscrub_test.go — перечень [Vars] и перечень, который снимает хук отправки
// `scripts/hooks/pre-push`, сходятся в обе стороны.
//
// Перечень объявлен дважды: здесь — для всего, что зовёт git из Go, и в хуке —
// граница, обрывающая наследование до запуска проверок. Расхождение тихое:
// каждая половина по отдельности выглядит исправной, а дыра открывается ровно
// на той переменной, которую добавили в одно место и забыли в другом. Сверка
// идёт в ОБЕ стороны — иначе половина запрета зеленела бы на пустом перечне.
//
// Разборщик читает ИСПОЛНЯЕМЫЕ строки хука: строка комментария, называющая
// `unset`, в перечень не входит — иначе снятая граница, объяснённая в
// комментарии, осталась бы «на месте». Способность разборщика упасть и
// промолчать на законной форме доказана синтетическими текстами ниже.
package gitenv

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// hookScrubList — имена `GIT_*`, которые снимают исполняемые строки `unset`
// текста хука, отсортированные и без повторов. Строка, оканчивающаяся обратной
// косой, продолжается следующей: в этой форме перечень и записан.
func hookScrubList(text string) []string {
	seen := map[string]bool{}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "unset ") {
			continue
		}
		for {
			cont := strings.HasSuffix(line, `\`)
			for _, f := range strings.Fields(strings.TrimSuffix(line, `\`)) {
				if strings.HasPrefix(f, "#") {
					break
				}
				if strings.HasPrefix(f, "GIT_") {
					seen[f] = true
				}
			}
			if !cont || i+1 >= len(lines) {
				break
			}
			i++
			line = strings.TrimSpace(lines[i])
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// scrubDivergence — что снимает только хук и что снимает только [Vars].
func scrubDivergence(inHook, inVars []string) (onlyHook, onlyVars []string) {
	h := map[string]bool{}
	for _, v := range inHook {
		h[v] = true
	}
	g := map[string]bool{}
	for _, v := range inVars {
		g[v] = true
		if !h[v] {
			onlyVars = append(onlyVars, v)
		}
	}
	for _, v := range inHook {
		if !g[v] {
			onlyHook = append(onlyHook, v)
		}
	}
	sort.Strings(onlyHook)
	sort.Strings(onlyVars)
	return onlyHook, onlyVars
}

// moduleRoot — корень дерева фундамента. Предпосылка проверяется, а не
// предполагается: переехавший пакет судил бы чужой каталог.
func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: корень дерева не вычислен: %v", err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !bytes.Contains(mod, []byte("module github.com/PRO-Robotech/corelib\n")) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s — не корень модуля фундамента (go.mod: %v)", root, err)
	}
	return root
}

func TestHookScrubsExactlyTheVars(t *testing.T) {
	t.Parallel()
	hook := filepath.Join(moduleRoot(t), "scripts", "hooks", "pre-push")
	raw, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("чтение %s: %v — граница, обрывающая наследование окружения до "+
			"запуска проверок, в дереве не найдена", hook, err)
	}
	inHook := hookScrubList(string(raw))
	if len(inHook) == 0 {
		t.Fatalf("в %s НЕТ ни одной снимаемой переменной GIT_*.\n"+
			"Либо граница снята — тогда `go test`, запущенный из хука, унаследует\n"+
			"GIT_DIR, — либо изменилась форма записи и сверка перестала читать свой\n"+
			"предмет. Оба исхода — находка, а не успех.", hook)
	}
	onlyHook, onlyVars := scrubDivergence(inHook, Vars())
	t.Logf("сверено переменных: в хуке %d, в Vars() %d", len(inHook), len(Vars()))
	if len(onlyHook) > 0 {
		t.Errorf("хук снимает, а Vars() — нет: %s\n"+
			"Вызов git из Go унаследует их при любом запуске мимо хука.",
			strings.Join(onlyHook, ", "))
	}
	if len(onlyVars) > 0 {
		t.Errorf("Vars() снимает, а хук — нет: %s\n"+
			"`go test`, запущенный из хука, унаследует их от `git push`.",
			strings.Join(onlyVars, ", "))
	}
}

// TestHookScrubComparisonFindsEachDivergence — разборщик и сверка способны
// упасть: каждый дефект меняет ОДИН факт против законного близнеца.
func TestHookScrubComparisonFindsEachDivergence(t *testing.T) {
	t.Parallel()
	vars := []string{"GIT_DIR", "GIT_PREFIX", "GIT_WORK_TREE"}
	cases := []struct {
		name               string
		hook               string
		onlyHook, onlyVars []string
		empty              bool
	}{
		{name: "законный близнец: перечень продолжен обратной косой",
			hook: "set -u\nunset GIT_DIR GIT_WORK_TREE \\\n      GIT_PREFIX\ncd x\n"},
		{name: "законный близнец: одной строкой, с хвостовым комментарием",
			hook: "unset GIT_PREFIX GIT_DIR GIT_WORK_TREE  # граница\n"},
		{name: "дефект: хук забыл переменную",
			hook:     "unset GIT_DIR \\\n  GIT_WORK_TREE\n",
			onlyVars: []string{"GIT_PREFIX"}},
		{name: "дефект: хук снимает лишнюю",
			hook:     "unset GIT_DIR GIT_WORK_TREE GIT_PREFIX GIT_INDEX_FILE\n",
			onlyHook: []string{"GIT_INDEX_FILE"}},
		{name: "дефект: граница снята, осталась в комментарии",
			hook:  "# unset GIT_DIR GIT_WORK_TREE GIT_PREFIX — сняли\nexit 0\n",
			empty: true},
		{name: "дефект: имя в хвостовом комментарии не снимается",
			hook:     "unset GIT_DIR GIT_WORK_TREE # GIT_PREFIX\n",
			onlyVars: []string{"GIT_PREFIX"}},
	}
	for _, c := range cases {
		got := hookScrubList(c.hook)
		if c.empty {
			if len(got) != 0 {
				t.Errorf("%s: разобрано %v, ждали пустой перечень", c.name, got)
			}
			continue
		}
		onlyHook, onlyVars := scrubDivergence(got, vars)
		if !reflect.DeepEqual(onlyHook, c.onlyHook) || !reflect.DeepEqual(onlyVars, c.onlyVars) {
			t.Errorf("%s: только в хуке %v (ждали %v), только в Vars %v (ждали %v)",
				c.name, onlyHook, c.onlyHook, onlyVars, c.onlyVars)
		}
	}
}
