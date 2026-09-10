// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package filter_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/filter"
)

// Значение фильтра приходит от клиента и уезжает в запрос — у него есть предел
// длины и один запрещённый знак.
//
// ПРЕДМЕТ (задача продукта #1654). До этой правки значение не проверялось ничем:
// разборщик брал всё между кавычками и отдавал вызывающему, тот привязывал его
// параметром. Два следствия, и оба видел клиент:
//
//  1. НУЛЕВОЙ БАЙТ. Postgres не хранит его в тексте и отвергает параметр с
//     SQLSTATE `22021`. Этого кода нет в общем классификаторе `pkg/db/pgfault`,
//     поэтому отказ уходил в ветвь «нераспознанная ошибка базы» и клиент получал
//     `INTERNAL` — платформа объявляла себя сломанной на вводе, который прислал
//     он сам.
//  2. ДЛИНА. Значение любого размера доезжало до запроса; у `CONTAINS` оно
//     становится образцом `LIKE '%…%'`, и стоимость строки растёт вместе с
//     длиной образца. Запрос дёшев в отправке и не дёшев в обслуживании.
//
// ПОЧЕМУ ПРОБЫ ПАРНЫЕ. Односторонняя проба зеленела бы на разборщике,
// отвергающем всё: «негодное отвергнуто» без «законное принято» ничего не
// утверждает.

// ct2LawfulValues — значения, которые обязаны ПРОХОДИТЬ. Положительная половина
// каждой оси; без неё отрицание ниже вакуумно.
func TestFilterValueLawfulInputsStillPass(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"имя ресурса":   "my-network",
		"пусто":         "",
		"идентификатор": "prj-0123456789abcdefg",
		"вид размещения в верхнем регистре":  "REGIONAL",
		"точечный вид области":               "iam.project",
		"ровно предел":                       strings.Repeat("a", filter.MaxValueLen),
		"непечатное, но законное для текста": "перенос\tтабуляцией",
		"кириллица":                          "сеть-продакшн",
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ast, err := filter.Parse(`name="`+v+`"`, []string{"name"})
			require.NoError(t, err, "законное значение обязано приниматься")
			require.NotNil(t, ast)
			assert.Equal(t, v, ast.Value)
		})
	}
}

// Предел длины: отвергается то, что за ним, и называется поле и правило.
func TestFilterValueLongerThanTheLimitIsRefusedByName(t *testing.T) {
	t.Parallel()
	over := strings.Repeat("a", filter.MaxValueLen+1)
	ast, err := filter.Parse(`name="`+over+`"`, []string{"name"})

	require.Nil(t, ast)
	var pe *filter.ParseError
	require.ErrorAs(t, err, &pe, "отказ обязан быть отказом разбора")
	msg := err.Error()
	assert.Contains(t, msg, `"name"`, "отказ обязан называть ПОЛЕ")
	assert.Contains(t, msg, "256", "отказ обязан называть ПРАВИЛО — предел")
	assert.NotContains(t, msg, over, "значение целиком в отказ не выносится")
}

// Нулевой байт: отвергается, и отказ называет поле и правило.
func TestFilterValueWithANulByteIsRefusedByName(t *testing.T) {
	t.Parallel()
	ast, err := filter.Parse("name=\"my-\x00net\"", []string{"name"})

	require.Nil(t, ast)
	var pe *filter.ParseError
	require.ErrorAs(t, err, &pe)
	msg := err.Error()
	assert.Contains(t, msg, `"name"`, "отказ обязан называть ПОЛЕ")
	assert.Contains(t, msg, "NUL", "отказ обязан называть ПРАВИЛО — запрещённый знак")
	// Знак в отказ не выносится: он невидим в выводе и ломает журнал читателя.
	assert.NotContains(t, msg, "\x00")
}

// Предел объявлен ЧИСЛОМ и доступен вызывающему: иначе тот, кто строит клиента,
// узнаёт его только отказом.
func TestFilterValueLimitIsDeclared(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 256, filter.MaxValueLen)
}

// Предел считается ЗНАКАМИ, а не байтами: иначе кириллическое имя длиной в
// половину предела отвергалось бы как длинное.
func TestFilterValueLimitCountsCharactersNotBytes(t *testing.T) {
	t.Parallel()
	v := strings.Repeat("я", filter.MaxValueLen) // 512 байт, 256 знаков
	ast, err := filter.Parse(`name="`+v+`"`, []string{"name"})
	require.NoError(t, err, "предел обязан считаться знаками")
	require.NotNil(t, ast)

	_, err = filter.Parse(`name="`+v+`я"`, []string{"name"}) // 257 знаков
	require.Error(t, err)
}
