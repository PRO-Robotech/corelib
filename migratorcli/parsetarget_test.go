// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// parsetarget_test.go — разбор `--target` строг на ОБЕИХ осях, и это union
// строгостей двух прежних форм, а не пересечение.
//
// # Предмет
//
// До сведения одно значение `--target` разбиралось в дереве ДВУМЯ функциями,
// строгими по-разному, и различие никем не решалось (#1383):
//
//	                 "12abc"                    "-5"
//	общий  strconv    ОТКАЗ                      принимал → goose.UpTo(-5)
//	копия  Sscanf     принимал КАК 12            отказ
//
// То есть каждая форма ловила то, что пропускала соседняя, и оператор получал
// разный исход на одном и том же вводе в зависимости от того, какой сервис он
// накатывает. Сведение обязано взять СТРОГОЕ с обеих сторон: ослабить хоть одну
// ось значило бы разменять живую проверку на единообразие.
package migratorcli_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/migratorcli"
)

func TestParseTargetVersionRejectsNegative(t *testing.T) {
	// Ось, строгая у СНЯТОЙ копии и отсутствовавшая у общего разбора. Версия
	// миграции — номер файла, отрицательной не бывает; `goose.UpTo(db, dir, -5)`
	// — это запрос, которому нечего сопоставить.
	for _, in := range []string{"-1", "-5", "-0010", " -42 "} {
		if _, err := migratorcli.ParseTargetVersion(in); err == nil {
			t.Errorf("ParseTargetVersion(%q): отказа нет, а версия отрицательна", in)
		}
	}
}

func TestParseTargetVersionRejectsTrailingGarbage(t *testing.T) {
	// Ось, строгая у ОБЩЕГО разбора и отсутствовавшая у копии: Sscanf на "12abc"
	// возвращает 12 без ошибки, то есть молча накатывает не туда.
	for _, in := range []string{"12abc", "0010x", "1 2", "abc"} {
		if _, err := migratorcli.ParseTargetVersion(in); err == nil {
			t.Errorf("ParseTargetVersion(%q): отказа нет, а хвост не число", in)
		}
	}
}

func TestParseTargetVersionAcceptsWhatTheOperatorActuallyTypes(t *testing.T) {
	// Положительный контроль. Без него оба отрицания выше зеленели бы на
	// разборе, отвергающем ВСЁ.
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"0", 0},             // граница: ноль — законная цель `down --target 0`
		{"1", 1},             //
		{"0010", 10},         // ведущие нули: так номер записан в имени файла
		{" 708001 ", 708001}, // пробелы по краям — обычная копипаста из консоли
	} {
		got, err := migratorcli.ParseTargetVersion(tc.in)
		if err != nil {
			t.Errorf("ParseTargetVersion(%q): неожиданный отказ %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseTargetVersion(%q) = %d, ожидалось %d", tc.in, got, tc.want)
		}
	}
}

func TestParseTargetVersionRefusalNamesTheFlagAndTheValue(t *testing.T) {
	// Отказ обязан восстанавливать оператору следующий шаг: назвать флаг и то,
	// что он прислал. «invalid input» посылает читать код.
	_, err := migratorcli.ParseTargetVersion("-5")
	if err == nil {
		t.Fatal("отказа нет")
	}
	for _, want := range []string{"--target", `"-5"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("текст отказа %q не называет %s", err.Error(), want)
		}
	}
}
