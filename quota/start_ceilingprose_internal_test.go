// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quota

// start_ceilingprose_internal_test.go — СТРОКА ЖУРНАЛА НА ПОЛОСЕ ОТСУТСТВИЯ
// АВТОРИТЕТА не утверждает того, что перестало быть правдой.
//
// ─────────────────────────────────────────────────────────────────────────────
// ЧТО СЛУЧИЛОСЬ С ПРЕЖНЕЙ РЕДАКЦИЕЙ
//
// Она говорила «no ceiling is stateable in this installation» — то есть «завести
// потолок в этой установке НЕГДЕ». Пока авторитет величин был единственным
// источником, это было правдой. Решение о его судьбе принято иначе: величину
// объявляет ПОСАДКА домена, и место, где её заводят, у оператора появилось.
//
// Утверждение пережило свой предмет и осталось в строке, которую оператор читает
// на каждом подъёме. Хуже прочих такое утверждение тем, что оно отправляет его в
// БЕЗДЕЙСТВИЕ: прочитавший «завести негде» не ищет, где завести.
//
// ─────────────────────────────────────────────────────────────────────────────
// ЧЕГО ЭТА ПРАВКА НЕ ДЕЛАЕТ — И ЭТО НЕСУЩЕЕ
//
// Она не обещает, что объявленная посадкой величина УЖЕ действует. На этой
// стадии она не действует: перенос величины в строку учёта и снятие послабления
// на пути мутации — предмет следующей стадии. Строка поэтому лишается ложного
// придаточного и НЕ приобретает нового обещания: остальные её утверждения
// («на пути запроса потолок не применяется», «тянущий не заведён») верны и
// остаются дословно.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStartLimitSyncJournalDoesNotClaimCeilingsAreUnstateable — отрицание.
//
// Ищется ровно то придаточное, которое стало ложным, а не слово «ceiling»:
// слово законно стоит в строке трижды и в соседних утверждениях.
func TestStartLimitSyncJournalDoesNotClaimCeilingsAreUnstateable(t *testing.T) {
	buf, logger := captureLogger()

	_, err := StartLimitSync(context.Background(), &recordingExecer{}, absent(t),
		nil, "kacho_vpc", Config{}, logger)
	require.NoError(t, err)

	line := buf.String()
	require.NotContains(t, line, "no ceiling is stateable",
		"строка журнала утверждает, что потолок завести НЕГДЕ; величину объявляет "+
			"посадка домена, и утверждение пережило свой предмет")
}

// TestStartLimitSyncJournalStillNamesBothConsequences — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ.
//
// Без него отрицание выше зеленело бы на строке, из которой вынули всё: «нет
// ложного придаточного» выполняется и на пустом журнале. Здесь утверждается, что
// оба следствия по-прежнему названы — и для пути запроса, и для тянущего.
func TestStartLimitSyncJournalStillNamesBothConsequences(t *testing.T) {
	buf, logger := captureLogger()

	_, err := StartLimitSync(context.Background(), &recordingExecer{}, absent(t),
		nil, "kacho_vpc", Config{}, logger)
	require.NoError(t, err)

	line := buf.String()
	for _, want := range []string{
		"limit authority declared absent",
		"on the REQUEST PATH no ceiling applies at all",
		"charged but never refused",
		"the delta puller is not started",
		"ceilings_enforced=false",
	} {
		require.True(t, strings.Contains(line, want),
			"строка журнала перестала называть %q; получено: %s", want, line)
	}
}
