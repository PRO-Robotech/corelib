// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"strings"
	"testing"
)

// TestShipperSQLSpeaksTheDeclaredStates — операторы вывоза говорят ТЕМИ ЖЕ
// состояниями, что объявлены константами пакета.
//
// # Зачем проба, если состояния можно было подставить форматированием
//
// Подставленное форматированием состояние видно только в рантайме, а два гейта
// дерева читают ТЕКСТ: гейт курсорных индексов узнаёт частичный индекс по
// дословному предикату запроса, гейт производителей — по строковому литералу.
// Литералы в операторах эти гейты возвращают, а расхождение литерала с
// константой ловит эта проба — то есть единственный источник истины остаётся
// один, просто проверяется он здесь, а не подстановкой.
//
// Проба утверждает ОБЕ стороны: каждое состояние встречается там, где должно, и
// снятые состояния не встречаются нигде. Без второй половины она зеленела бы на
// операторе, который пишет состояние, которого в словаре таблицы уже нет.
func TestShipperSQLSpeaksTheDeclaredStates(t *testing.T) {
	s := buildShipperSQL("kaname.audit_outbox")

	if want := "status <> '" + StatusSent + "'"; !strings.Contains(s.claim, want) {
		t.Errorf("клейм не несёт предиката %q дословно — частичный индекс журнала\n"+
			"перестанет засчитываться гейтом курсорных индексов:\n%s", want, s.claim)
	}
	if want := "status = '" + StatusSent + "'"; !strings.Contains(s.sent, want) {
		t.Errorf("пометка доставленной не пишет состояние %q:\n%s", StatusSent, s.sent)
	}
	if want := "status = '" + StatusPending + "'"; !strings.Contains(s.defer_, want) {
		t.Errorf("назначение повтора не пишет состояние %q:\n%s", StatusPending, s.defer_)
	}

	// Отрицание: снятые состояния не должны встречаться ни в одном операторе.
	for _, gone := range []string{"in_flight", "failed"} {
		for name, q := range map[string]string{"клейм": s.claim, "пометка": s.sent, "повтор": s.defer_} {
			if strings.Contains(q, gone) {
				t.Errorf("%s пишет состояние %q, которого нет в словаре таблицы:\n%s", name, gone, q)
			}
		}
	}
}
