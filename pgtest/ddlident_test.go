// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// ddlident_test.go — имя базы попадает в текст DDL, и попасть туда оно обязано
// ОДНИМ идентификатором.
//
// # Почему это не закрывается параметром запроса
//
// `CREATE DATABASE` / `DROP DATABASE` не принимают связанных параметров:
// в Postgres место идентификатора в DDL не является позицией параметра, поэтому
// «переписать на $1» — не исход. Единственная корректная форма — цитирование
// идентификатора, и проверять надо именно её.
//
// # Откуда берётся операнд
//
// Имя собирается из `Config.Name`, который задаёт вызывающий пакет в своём
// TestMain. Сегодня таких значений в дереве 42 вхождения / 21 различное, и все —
// строчные ASCII-идентификаторы (ни одного символа вне `[a-z]`), то есть
// враждебного среди них нет. Свойство держится не этим: значение приходит из
// кода, который эта функция не контролирует, а цена корректной формы — ноль.
// Цитирование строчного идентификатора семантически тождественно его отсутствию
// (нецитированный идентификатор складывается к нижнему регистру, а он уже в нём),
// поэтому у нынешних вызывающих не меняется ничего.
package pgtest

import (
	"strings"
	"testing"
)

// hostileName — имя, в котором есть и кавычка, и разделитель команд: ровно та
// пара, которой из одного стейтмента делают два.
const hostileName = `evil"; DROP DATABASE postgres; --`

// quotedForm — как идентификатор обязан выглядеть в тексте: в двойных кавычках,
// внутренние кавычки удвоены.
func quotedForm(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// TestCreateDatabaseStmtQuotesTheIdentifier — предмет и положительный контроль
// рядом: враждебное имя не выходит за границы идентификатора, законное имя
// по-прежнему даёт рабочий стейтмент.
func TestCreateDatabaseStmtQuotesTheIdentifier(t *testing.T) {
	t.Run("враждебное имя остаётся ОДНИМ идентификатором", func(t *testing.T) {
		stmt := createDatabaseStmt(hostileName, "")

		if strings.Contains(stmt, `CREATE DATABASE `+hostileName) {
			t.Errorf("идентификатор вставлен в DDL сырым:\n  %s\n"+
				"После имени начинается вторая команда — стейтмент перестал быть одним.", stmt)
		}
		if !strings.Contains(stmt, quotedForm(hostileName)) {
			t.Errorf("идентификатор не процитирован:\n  получено: %s\n  ожидалась подстрока: %s",
				stmt, quotedForm(hostileName))
		}
	})

	t.Run("TEMPLATE — тот же идентификатор, та же форма", func(t *testing.T) {
		stmt := createDatabaseStmt("kacho_vpc_t0001", hostileName)

		if strings.Contains(stmt, `TEMPLATE `+hostileName) {
			t.Errorf("имя шаблона вставлено сырым:\n  %s", stmt)
		}
		if !strings.Contains(stmt, quotedForm(hostileName)) {
			t.Errorf("имя шаблона не процитировано:\n  %s", stmt)
		}
	})

	t.Run("DROP — та же форма", func(t *testing.T) {
		stmt := dropDatabaseStmt(hostileName)

		if strings.Contains(stmt, `IF EXISTS `+hostileName) {
			t.Errorf("идентификатор вставлен в DROP сырым:\n  %s", stmt)
		}
		if !strings.Contains(stmt, quotedForm(hostileName)) {
			t.Errorf("идентификатор в DROP не процитирован:\n  %s", stmt)
		}
		// WITH (FORCE) обязан остаться ЗА идентификатором, а не уехать внутрь
		// кавычек: без него утёкший пул продержал бы базу до конца прогона.
		if !strings.HasSuffix(stmt, ` WITH (FORCE)`) {
			t.Errorf("DROP потерял WITH (FORCE):\n  %s", stmt)
		}
	})

	// Положительный контроль: обычное имя не должно ни исчезнуть, ни поменяться
	// по существу. Без него все проверки выше зеленели бы на функции, которая
	// возвращает пустую строку.
	t.Run("законное имя по-прежнему создаёт свою базу", func(t *testing.T) {
		stmt := createDatabaseStmt("kacho_vpc_t0001", "kacho_vpc_template")

		for _, want := range []string{"CREATE DATABASE", "kacho_vpc_t0001", "TEMPLATE", "kacho_vpc_template"} {
			if !strings.Contains(stmt, want) {
				t.Errorf("законный стейтмент потерял %q:\n  %s", want, stmt)
			}
		}
		if strings.Contains(stmt, `""`) {
			t.Errorf("в законном имени нечего экранировать, а удвоенная кавычка появилась:\n  %s", stmt)
		}
	})
}
