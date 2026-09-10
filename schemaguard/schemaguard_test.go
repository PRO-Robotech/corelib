// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// schemaguard_test.go — решение проверяется БЕЗ БАЗЫ (предикат 3 задачи #1734):
// вердикт есть чистая функция двух чисел и объявленных точек невозврата.
//
// Способность упасть и смолчать доказана ЗДЕСЬ ЖЕ: у каждого утверждения пара
// миров — отказ и его законный близнец, — и вход обоим подаётся напрямую, потому
// что вердикт есть чистая функция двух чисел и объявленных точек невозврата.
//
// ЗДЕСЬ СТОЯЛО «доказана инъекцией» с координатой отдельного файла вида
// `<пакет>` + `_injection_test.go`, и файла с таким именем в дереве не было НИ ДНЯ
// (мёртвое имя целиком здесь не воспроизводится: в форме координаты оно читается
// держателем обещаний как живое утверждение). Отдельного файла инъекции здесь и не
// нужно: функции нечего подавать, кроме её аргументов, и второй файл был бы вторым
// местом об одном предмете. Обещание снято вместе с координатой, которой оно
// обещало, — а не заменено на другую выдуманную (#2519).
package schemaguard_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/PRO-Robotech/corelib/schemaguard"
)

// TestVerdict_TwoNumbersAndTheDeclaredPoints — ядро решения.
//
// Утверждаются ОБЕ стороны каждой оси: односторонняя проба зеленела бы на
// страже, отвергающем всё, и на страже, не отвергающем ничего.
func TestVerdict_TwoNumbersAndTheDeclaredPoints(t *testing.T) {
	cases := []struct {
		name      string
		set       schemaguard.Set
		db        int64
		wantReady bool
		wantIn    []string // подстроки, обязанные быть в тексте ДЛЯ ОПЕРАТОРА
	}{
		{
			name:      "схема вровень с образом — готов",
			set:       schemaguard.Set{Version: 12},
			db:        12,
			wantReady: true,
		},
		{
			name:      "схема ушла вперёд — НЕ готов даже без объявленных точек",
			set:       schemaguard.Set{Version: 12},
			db:        13,
			wantReady: false,
			// Обе величины обязаны быть названы оператору.
			wantIn: []string{"13", "12", "вперёд"},
		},
		{
			name:      "схема ушла вперёд НА МНОГО — тот же исход, fail-closed",
			set:       schemaguard.Set{Version: 1, Points: []int64{1}},
			db:        900,
			wantReady: false,
			wantIn:    []string{"900", "1"},
		},
		{
			name: "образ впереди схемы, точек невозврата НЕ пройдено — готов " +
				"(накат совместимой вперёд миграции не мешает обслуживать)",
			set:       schemaguard.Set{Version: 12, Points: []int64{3}},
			db:        10,
			wantReady: true,
		},
		{
			name:      "образ впереди схемы ЧЕРЕЗ точку невозврата — НЕ готов",
			set:       schemaguard.Set{Version: 12, Points: []int64{11}},
			db:        10,
			wantReady: false,
			wantIn:    []string{"10", "12", "11"},
		},
		{
			name:      "точка невозврата РОВНО на версии образа — считается пройденной",
			set:       schemaguard.Set{Version: 12, Points: []int64{12}},
			db:        10,
			wantReady: false,
			wantIn:    []string{"12"},
		},
		{
			name:      "точка невозврата РОВНО на версии схемы — уже применена, готов",
			set:       schemaguard.Set{Version: 12, Points: []int64{10}},
			db:        10,
			wantReady: true,
		},
		{
			name:      "свежая база (версии 0) при непустом наборе без точек — готов",
			set:       schemaguard.Set{Version: 12},
			db:        0,
			wantReady: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.set.Verdict(c.db)
			if got.Ready != c.wantReady {
				t.Fatalf("готовность: получили %v, ожидали %v (схема %d, образ %d, точки %v, причина %q)",
					got.Ready, c.wantReady, c.db, c.set.Version, c.set.Points, got.Reason)
			}
			if c.wantReady {
				if got.Reason != "" {
					t.Errorf("готовый вердикт несёт причину %q — оператор прочитает её как отказ", got.Reason)
				}
				return
			}
			if got.Reason == "" {
				t.Fatalf("неготовый вердикт БЕЗ причины — оператор не отличит «сломан продукт» "+
					"от «образ не той версии, что схема» (схема %d, образ %d)", c.db, c.set.Version)
			}
			for _, want := range c.wantIn {
				if !strings.Contains(got.Reason, want) {
					t.Errorf("причина не называет %q: %q — обе величины обязаны быть названы оператору",
						want, got.Reason)
				}
			}
			if got.DBVersion != c.db || got.ImageVersion != c.set.Version {
				t.Errorf("вердикт не несёт обеих величин: %+v", got)
			}
		})
	}
}

// TestDescribe_DerivesFromTheEmbeddedSet — версия и точки ВЫВОДЯТСЯ из набора,
// а не выписываются (предикат 2 задачи #1734).
func TestDescribe_DerivesFromTheEmbeddedSet(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_initial.sql":  {Data: []byte("-- +goose Up\nCREATE TABLE a();\n")},
		"0002_add.sql":      {Data: []byte("-- +goose Up\nALTER TABLE a ADD b int;\n")},
		"708001_cursor.sql": {Data: []byte("-- +goose Up\nCREATE INDEX i ON a(b);\n")},
		"README.md":         {Data: []byte("не миграция")},
	}
	got, err := schemaguard.Describe(fsys)
	if err != nil {
		t.Fatalf("разбор набора: %v", err)
	}
	if got.Version != 708001 {
		t.Errorf("старшая версия: получили %d, ожидали 708001 — версия читается из имени, "+
			"а порядок обхода лексикографический, поэтому «последний файл» ей не равен", got.Version)
	}
	if got.Files != 3 {
		t.Errorf("осмотрено файлов %d, ожидали 3 (не-.sql в счёт не идёт) — объём осмотренного "+
			"обязан быть отличим от числа находок", got.Files)
	}
	if len(got.Points) != 0 {
		t.Errorf("точек невозврата %v, ожидали ни одной", got.Points)
	}
}

// TestDescribe_MarkerIsReadFromTheUpSectionAndNeedsAReason — обе стороны
// признака: годная форма считается, негодные — нет.
func TestDescribe_MarkerIsReadFromTheUpSectionAndNeedsAReason(t *testing.T) {
	m := schemaguard.PointOfNoReturnMarker
	fsys := fstest.MapFS{
		"0001_marked.sql": {Data: []byte(
			"-- +goose Up\n" + m + " снимается колонка legacy_id\nALTER TABLE a DROP COLUMN legacy_id;\n")},
		"0002_empty_reason.sql": {Data: []byte(
			"-- +goose Up\n" + m + "\nALTER TABLE a DROP COLUMN x;\n")},
		"0003_only_in_down.sql": {Data: []byte(
			"-- +goose Up\nALTER TABLE a ADD y int;\n-- +goose Down\n" + m + " не считается\n")},
	}
	got, err := schemaguard.Describe(fsys)
	if err != nil {
		t.Fatalf("разбор набора: %v", err)
	}
	if len(got.Points) != 1 || got.Points[0] != 1 {
		t.Fatalf("точки невозврата %v, ожидали ровно [1]:\n"+
			"    пустое обоснование признаком не считается (иначе токен — печать, которую ставят "+
			"не читая);\n"+
			"    признак в Down-секции не считается (там он про другой путь)", got.Points)
	}
}

// TestDescribe_EmptySetIsARefusalNotVersionZero — пустой набор не молчит.
func TestDescribe_EmptySetIsARefusalNotVersionZero(t *testing.T) {
	_, err := schemaguard.Describe(fstest.MapFS{"README.md": {Data: []byte("x")}})
	if !errors.Is(err, schemaguard.ErrEmptySet) {
		t.Fatalf("пустой набор дал %v, ожидали ErrEmptySet — молчаливая «версия 0» сделала бы "+
			"образ со сломанной директивой встраивания готовым на ЛЮБОЙ схеме", err)
	}
}

// TestDescribe_UnparsableVersionIsARefusalNotASkip — молчаливый пропуск занизил
// бы старшую версию, а заниженная версия уводит разбор выкатки не туда.
func TestDescribe_UnparsableVersionIsARefusalNotASkip(t *testing.T) {
	_, err := schemaguard.Describe(fstest.MapFS{
		"0001_ok.sql":     {Data: []byte("-- +goose Up\n")},
		"initial_bad.sql": {Data: []byte("-- +goose Up\n")},
	})
	if err == nil {
		t.Fatalf("имя без версии принято молча — старшая версия набора занижена, и образ " +
			"объявил бы себя отставшим от собственной схемы")
	}
}

// TestCheck_FailsClosedWhenTheVersionCannotBeRead — неполученный ответ не есть «да».
//
// Рядом стоит ПОЛОЖИТЕЛЬНЫЙ контроль: без него отрицание зеленело бы на
// проверке, отвергающей всё.
func TestCheck_FailsClosedWhenTheVersionCannotBeRead(t *testing.T) {
	set := schemaguard.Set{Version: 5}
	boom := errors.New("соединение отвергнуто")

	if err := set.Check(func(context.Context) (int64, error) { return 0, boom })(context.Background()); err == nil {
		t.Fatalf("версия схемы не прочиталась, а проверка ответила «готов» — неполученный ответ " +
			"выдан за «да» ровно на том пути, ради которого проверка заведена")
	} else if !strings.Contains(err.Error(), boom.Error()) {
		t.Errorf("причина отказа не несёт исходной ошибки (%v) — оператор не узнает, ЧТО не "+
			"прочиталось", err)
	}

	if err := set.Check(func(context.Context) (int64, error) { return 5, nil })(context.Background()); err != nil {
		t.Errorf("положительный контроль: схема вровень с образом, а проверка отвергла: %v", err)
	}
}
