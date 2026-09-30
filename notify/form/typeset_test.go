// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package form_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/form"
	"github.com/PRO-Robotech/corelib/notify/spec"
)

// kindSample — строка таблицы пробы: нулевое значение вида и непустое по форме.
type kindSample struct {
	zero, valid any
}

// kindTable — таблица пробы по видам. Перечень видов проба берёт НЕ отсюда, а
// из notify/spec (единственного объявления): вид перечня без строки здесь —
// находка, а не пропуск.
var kindTable = map[form.Kind]kindSample{
	form.KindText:      {zero: "", valid: "probe"},
	form.KindSecret:    {zero: "", valid: "123456"},
	form.KindPath:      {zero: "", valid: "/"},
	form.KindToken:     {zero: "", valid: "0123456789abcdefghijklmnopqrstuv"},
	form.KindTimestamp: {zero: time.Time{}, valid: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)},
}

// kindSetFindings сверяет notify/form с перечнем видов в одну сторону: каждый
// вид перечня form знает — нуль отвергает ErrEmpty, значение по форме
// принимает. Возвращает находки (имя вида и причину) и число сверенных видов.
func kindSetFindings(kinds []form.Kind, table map[form.Kind]kindSample) (findings []string, checked int) {
	for _, k := range kinds {
		row, ok := table[k]
		if !ok {
			findings = append(findings, fmt.Sprintf("%s: вида перечня notify/spec нет в таблице пробы", k))
			continue
		}
		checked++
		if _, err := form.Require(k, row.zero); !errors.Is(err, form.ErrEmpty) {
			findings = append(findings, fmt.Sprintf("%s: нуль не отвергнут ErrEmpty (получено %v)", k, err))
		}
		if _, err := form.Require(k, row.valid); err != nil {
			findings = append(findings, fmt.Sprintf("%s: значение по форме не принято (%v)", k, err))
		}
	}
	return findings, checked
}

// NTF1-B29 (перечень типов). Близнец — перечень без инъекции: пять видов,
// каждый отвергает нуль и принимает значение по форме.
func TestKindSetOfFormMatchesSpec(t *testing.T) {
	kinds := spec.AttrKinds()
	findings, checked := kindSetFindings(kinds, kindTable)
	t.Logf("видов в перечне notify/spec: %d, сверено: %d", len(kinds), checked)
	require.Empty(t, findings)
	require.Equal(t, 5, checked, "на этой редакции видов пять")
}

// Инъекция: в копию перечня добавлен вид duration и строка таблицы для него —
// проверка notify/form отвечает ErrUnknownType на обоих значениях, проба
// красная и называет duration.
func TestKindSetInjectionUnknownKindIsRefused(t *testing.T) {
	const duration form.Kind = "duration"
	kinds := append(append([]form.Kind(nil), spec.AttrKinds()...), duration)
	table := map[form.Kind]kindSample{duration: {zero: "", valid: "5m"}}
	for k, v := range kindTable {
		table[k] = v
	}
	for _, raw := range []any{"", "5m"} {
		_, err := form.Require(duration, raw)
		require.ErrorIs(t, err, form.ErrUnknownType, "%q", raw)
		_, _, err = form.Presence(duration, raw)
		require.ErrorIs(t, err, form.ErrUnknownType, "%q", raw)
	}
	findings, _ := kindSetFindings(kinds, table)
	require.Len(t, findings, 2, "нуль и значение duration — обе находки")
	for _, f := range findings {
		require.Contains(t, f, "duration")
	}
}

// Вид перечня без строки таблицы — проба красная и называет вид.
func TestKindSetInjectionKindWithoutTableRowIsNamed(t *testing.T) {
	table := map[form.Kind]kindSample{}
	for k, v := range kindTable {
		if k != form.KindToken {
			table[k] = v
		}
	}
	findings, _ := kindSetFindings(spec.AttrKinds(), table)
	require.Len(t, findings, 1)
	require.Contains(t, findings[0], "token")
}

// УК66, CX1-50: B30 по перечню видов — нуль каждого вида Presence называет
// «не задано» без ошибки (атрибут optional опускается), значение по форме —
// «задано»; Require на нуле — ErrEmpty (атрибут required).
func TestPresenceOverEveryKindOfTheSpec(t *testing.T) {
	kinds := spec.AttrKinds()
	require.NotEmpty(t, kinds, "перечень видов пуст — проверять нечего, это не зелёный")
	for _, k := range kinds {
		row, ok := kindTable[k]
		require.True(t, ok, "вида %s нет в таблице пробы", k)

		v, present, err := form.Presence(k, row.zero)
		require.NoError(t, err, "%s: нуль", k)
		require.Equal(t, form.Absent, present, "%s: нуль — не задано", k)
		_, verr := v.Value()
		require.ErrorIs(t, verr, form.ErrUnset, "%s: у незаданного значения нет строки", k)

		v, present, err = form.Presence(k, row.valid)
		require.NoError(t, err, "%s: значение", k)
		require.Equal(t, form.Given, present, "%s: значение — задано", k)
		require.Equal(t, k, v.Kind())
		s, err := v.Value()
		require.NoError(t, err)
		require.NotEmpty(t, s)
	}
	t.Logf("видов сверено: %d", len(kinds))
}
