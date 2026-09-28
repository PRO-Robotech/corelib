// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// judge_injection_test.go — суд над поддеревом способен упасть и способен
// смолчать.
//
// Проверка дерева (coverage_test.go) зовёт тот же [tracecut.Judge] на живом
// поддереве; здесь он получает синтетику. Инъекция — открытие спана,
// закрытое мимо обёртки фундамента: обязана дать находку С КООРДИНАТОЙ.
// Законный близнец той же формы обязан дать ноль. Пустой обход обязан дать
// отказ, а не «ноль находок».
package tracecut_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/internal/tracecut"
)

const wrapper = "github.com/PRO-Robotech/corelib/internal/otelx"

// neighbour — второй файл синтетического поддерева, без спанов: суд обязан
// прочитать и его, иначе перепись не отличит «прочитал всё» от «прочитал
// один файл».
const neighbour = "package oauth2\n\nfunc Sum(a, b int) int { return a + b }\n"

func judgeWorld(t *testing.T, engine string) tracecut.Report {
	t.Helper()
	rep, err := tracecut.Judge([]tracecut.Source{
		{Path: "oauth2/engine.go", Src: []byte(engine)},
		{Path: "oauth2/sum.go", Src: []byte(neighbour)},
	}, wrapper)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: суд над синтетикой отказал: %v", err)
	}
	if rep.Census.Files != 2 || rep.Census.Openings != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: перепись %s, ожидались 2 файла и 1 открытие", rep.Census)
	}
	return rep
}

// TestJudgeIsSilentOnTheLawfulTwin — законный близнец: ноль находок при
// ненулевой переписи.
func TestJudgeIsSilentOnTheLawfulTwin(t *testing.T) {
	t.Parallel()
	rep := judgeWorld(t, lawfulSource)
	for _, f := range rep.Findings {
		t.Errorf("законный близнец дал находку: %s", f)
	}
	if want := strings.Count(lawfulSource, "\n") + strings.Count(neighbour, "\n"); rep.Census.Lines != want {
		t.Errorf("строк осмотрено %d, ожидалось %d", rep.Census.Lines, want)
	}
}

// TestJudgeFindsASpanClosedPastTheWrapper — ИНЪЕКЦИЯ: каждый мир отличается
// от близнеца одним фактом и обязан дать ровно одну находку, называющую
// координату открытия и причину.
func TestJudgeFindsASpanClosedPastTheWrapper(t *testing.T) {
	t.Parallel()
	worlds := []struct {
		name   string
		from   string
		to     string
		reason string
	}{
		{
			name:   "метод End самого спана",
			from:   "defer otelx.End(span, &err)",
			to:     "defer span.End()",
			reason: "мимо обёртки",
		},
		{
			name:   "обвязка апстрима",
			from:   `"github.com/PRO-Robotech/corelib/internal/otelx"`,
			to:     `"github.com/ory/x/otelx"`,
			reason: "github.com/ory/x/otelx",
		},
		{
			name:   "закрытия нет",
			from:   "defer otelx.End(span, &err)",
			to:     "_, _ = span, otelx.End",
			reason: "не закрыт",
		},
		{
			name:   "закрыт дважды",
			from:   "defer otelx.End(span, &err)",
			to:     "defer otelx.End(span, &err)\n\tdefer span.End()",
			reason: "ровно один раз",
		},
		{
			name:   "имя спана не литерал",
			from:   `Start(ctx, "Fosite.NewAccessRequest")`,
			to:     `Start(ctx, spanName)`,
			reason: "не строковый литерал",
		},
	}
	for _, w := range worlds {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()
			rep := judgeWorld(t, mutate(t, w.from, w.to))
			if len(rep.Findings) != 1 {
				t.Fatalf("находок %d, ожидалась одна: %v", len(rep.Findings), rep.Findings)
			}
			got := rep.Findings[0].String()
			if !strings.HasPrefix(got, "oauth2/engine.go:11 ") {
				t.Errorf("находка %q не называет координату открытия oauth2/engine.go:11", got)
			}
			if !strings.Contains(got, w.reason) {
				t.Errorf("находка %q не называет причину (%q)", got, w.reason)
			}
		})
	}
}

// TestJudgeRefusesAnEmptyWalk — ни одного файла: это отказ, а не чистое
// поддерево.
func TestJudgeRefusesAnEmptyWalk(t *testing.T) {
	t.Parallel()
	rep, err := tracecut.Judge(nil, wrapper)
	if !errors.Is(err, tracecut.ErrEmptyWalk) {
		t.Fatalf("пустой обход дал %v и %d находок, ожидался отказ ErrEmptyWalk", err, len(rep.Findings))
	}
}

// TestJudgeRefusesASubtreeWithoutOpenings — файлы есть, открытий нет: вырез
// снят целиком либо разбор ослеп; «ноль находок» тут не вердикт.
func TestJudgeRefusesASubtreeWithoutOpenings(t *testing.T) {
	t.Parallel()
	_, err := tracecut.Judge([]tracecut.Source{{Path: "oauth2/sum.go", Src: []byte(neighbour)}}, wrapper)
	if !errors.Is(err, tracecut.ErrNoOpenings) {
		t.Fatalf("поддерево без открытий дало %v, ожидался отказ ErrNoOpenings", err)
	}
	if err != nil && !strings.Contains(err.Error(), "файлов 1") {
		t.Errorf("отказ %q не называет объём осмотренного", err)
	}
}

// TestJudgeRefusesAnUnparsableFile — непрочитанный файл роняет суд целиком:
// иначе открытие в нём выпало бы из переписи молча.
func TestJudgeRefusesAnUnparsableFile(t *testing.T) {
	t.Parallel()
	_, err := tracecut.Judge([]tracecut.Source{
		{Path: "oauth2/engine.go", Src: []byte(lawfulSource)},
		{Path: "oauth2/broken.go", Src: []byte("package oauth2\n\nfunc (")},
	}, wrapper)
	if err == nil || !strings.Contains(err.Error(), "oauth2/broken.go") {
		t.Fatalf("битый файл дал %v, ожидался отказ с его координатой", err)
	}
}
