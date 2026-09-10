// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// preserve_before_destroy_test.go — отказ перед сносом обязан восстанавливать ОБА
// следующих шага оператора, а не один (задача #2134).
//
// # Предмет
//
// Страж считает строки таблицы, которую снесёт ещё не применённая миграция, и на
// ненулевом счёте отказывает. До этой пробы его текст называл оператору ровно один
// исполнимый шаг — «разреши снос переменной окружения», — а второй, сохранить
// строки, оставлял словами «establish where they come from». Это исследование, а не
// шаг: команды в нём нет.
//
// Цена именно здесь, а не в общем случае. Ровно этот отказ встретит всякий, кто
// обновляется через снятие домена величин: таблица величин несёт 33 посеянных
// умолчания и сколько угодно назначенных администратором, поэтому счёт ненулевой у
// КАЖДОЙ установки by construction. Оператор, у которого из двух исходов исполним
// один, выберет исполнимый — то есть уничтожит свои данные, следуя подсказке.
//
// # Что здесь утверждается парой
//
// Отрицание («сохранение не названо») зеленело бы на страже, отвергающем всё,
// поэтому положительный близнец стоит в том же файле: пустая таблица не даёт ни
// отказа, ни подсказки — сохранять там нечего.
package dropguard_test

import (
	"context"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/dropguard"
)

// TestRefusalNamesHowToSaveTheRowsAndOnlyThenHowToDestroyThem — несущая половина.
func TestRefusalNamesHowToSaveTheRowsAndOnlyThenHowToDestroyThem(t *testing.T) {
	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 17,
		"gadgets": 0,
	}), preflightChain(t), allPending, nil, dropguard.WholeChain())

	if len(rep.Violations) != 1 {
		t.Fatalf("want exactly one violation, got %+v", rep.Violations)
	}
	msg := rep.Violations[0].Error()

	// 1. Сохранение названо КОМАНДОЙ и названо с ЭТОЙ таблицей. Общая фраза
	//    «сделайте резервную копию» шагом не является: оператор всё равно обязан
	//    сам сочинить запрос, а сочиняет он его в ту минуту, когда накат уже
	//    отказал и время дорого.
	save := dropguard.PreserveCommand("widgets")
	if !strings.Contains(msg, save) {
		t.Errorf("текст отказа не называет команды сохранения %q:\n%s\n"+
			"оператор получает исполнимым только уничтожение, и выберет его", save, msg)
	}

	// 2. Уничтожение по-прежнему названо — положительный контроль правки: чинить
	//    один шаг, отняв другой, значит поменять одну неполноту на другую.
	if !strings.Contains(msg, dropguard.ApprovalEnv) {
		t.Errorf("текст отказа перестал называть осознанное уничтожение (%s):\n%s",
			dropguard.ApprovalEnv, msg)
	}

	// 3. Порядок: сохранение прежде уничтожения. Это не вкус — первый исполнимый
	//    шаг, попавшийся оператору в отказе, и есть тот, который он сделает.
	if i, j := strings.Index(msg, save), strings.Index(msg, dropguard.ApprovalEnv); i >= 0 && j >= 0 && i > j {
		t.Errorf("уничтожение названо прежде сохранения (%d против %d):\n%s", j, i, msg)
	}
}

// TestPreserveCommandIsNotOfferedWhereThereIsNothingToPreserve — положительный
// близнец. Без него утверждение выше зеленело бы на страже, который отказывает
// всегда и подсказывает всегда.
func TestPreserveCommandIsNotOfferedWhereThereIsNothingToPreserve(t *testing.T) {
	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 0,
		"gadgets": 0,
	}), preflightChain(t), allPending, nil, dropguard.WholeChain())

	if !rep.OK() {
		t.Fatalf("две пустые таблицы обязаны проходить, получено %+v", rep.Violations)
	}
	if rep.Counted != 2 {
		t.Fatalf("перепись: сосчитано %d из %d — проход, ничего не измеривший, проходом не является",
			rep.Counted, rep.Pending)
	}
	t.Logf("перепись: сносов в цепи %d, из них предстоящих %d, сосчитано %d, отказов %d",
		rep.DropsInChain, rep.Pending, rep.Counted, len(rep.Violations))
}

// TestPreserveCommandNamesTheTableAndTheReader — команда обязана быть исполнимой в
// той оболочке, где её прочтут: назвать таблицу, оставить место под адрес базы и
// не потерять заголовок столбцов (без него CSV не прочитать обратно).
//
// Исполнимость против ЖИВОЙ схемы утверждает интеграционная проба службы доступа
// (`services/iam/internal/migrations`): здесь база не поднимается, поэтому здесь
// проверяется форма, а не то, что запрос отработает.
func TestPreserveCommandNamesTheTableAndTheReader(t *testing.T) {
	got := dropguard.PreserveCommand("kaname.limits")
	for _, want := range []string{"kaname.limits", "$DSN", "HEADER"} {
		if !strings.Contains(got, want) {
			t.Errorf("команда сохранения не называет %q: %s", want, got)
		}
	}
}
