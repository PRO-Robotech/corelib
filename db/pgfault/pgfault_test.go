// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// pgfault_test.go — проба дома, выражающего ОДНО правило корпуса: отказ
// хранилища, разобранный по классу.
//
// Правило `data-integrity.md` §«Within-service инварианты» формулирует
// отображение как одно: нарушение внешнего ключа — одно, уникальности — другое,
// проверки — третье, исключения — четвёртое. Дом обязан выражать ровно это, и не
// больше: ТЕКСТ отказа принадлежит сервису (тон — часть контракта,
// `api-conventions.md` §Error-format), дом решает только КЛАСС.
//
// Проба утверждает обе стороны каждой оси: класс распознаётся — и не
// распознаётся там, где предмета нет. Односторонняя проба зеленела бы на
// классификаторе, отвечающем одним и тем же на любой вход.
package pgfault_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/corelib/db/pgfault"
)

// pgErr — производитель входа: настоящая ошибка драйвера, а не её пересказ.
// Класс дефекта, который она ловит, — классификатор, судящий по тексту
// сообщения вместо кода.
func pgErr(code, constraint, table string) error {
	return &pgconn.PgError{
		Code:           code,
		ConstraintName: constraint,
		TableName:      table,
		Message:        "some driver text that must never reach the caller",
		Detail:         "some driver detail",
	}
}

func TestClassifyRecognisesTheIntegrityClasses(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want pgfault.Class
	}{
		{"нет строки", pgx.ErrNoRows, pgfault.NoRows},
		{"нет строки под обёрткой", fmt.Errorf("select account: %w", pgx.ErrNoRows), pgfault.NoRows},
		{"уникальность 23505", pgErr("23505", "accounts_name_unique", "accounts"), pgfault.Unique},
		{"внешний ключ 23503", pgErr("23503", "projects_account_fk", "projects"), pgfault.ForeignKey},
		{"проверка 23514", pgErr("23514", "accounts_name_check", "accounts"), pgfault.Check},
		{"исключение 23P01", pgErr("23P01", "subnets_no_overlap_v4", "subnets"), pgfault.Exclusion},
		{"обязательность 23502", pgErr("23502", "", "accounts"), pgfault.NotNull},
		{"целостность 23000", pgErr("23000", "membership_carrying_rights_is_kept", "memberships"), pgfault.IntegrityConstraint},
		{"форма значения 22P02", pgErr("22P02", "", ""), pgfault.InvalidText},
		{"сериализация 40001", pgErr("40001", "", ""), pgfault.SerializationConflict},
		{"взаимоблокировка 40P01", pgErr("40P01", "", ""), pgfault.SerializationConflict},
		{"под обёрткой %w", fmt.Errorf("insert: %w", pgErr("23505", "c", "t")), pgfault.Unique},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pgfault.Classify(c.in)
			if got.Class != c.want {
				t.Fatalf("класс %v, ожидался %v (SQLSTATE %q)", got.Class, c.want, got.SQLState)
			}
			if !got.Is(c.want) {
				t.Fatalf("Is(%v) отвечает «нет» на собственном классе", c.want)
			}
		})
	}
}

// Обратная сторона той же оси: вход, предмета не имеющий, классом НЕ становится.
// Без неё проба зеленела бы на классификаторе, отвечающем «уникальность» всему.
func TestClassifyLeavesForeignInputUnclassified(t *testing.T) {
	cases := []struct {
		name string
		in   error
	}{
		{"nil", nil},
		{"обычная ошибка", errors.New("connection reset by peer")},
		{"код учёта KQ001", pgErr("KQ001", "", "")},
		{"код доступности 57P03", pgErr("57P03", "", "")},
		{"код доступности 53300", pgErr("53300", "", "")},
		{"неизвестный код 42P01", pgErr("42P01", "", "")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pgfault.Classify(c.in)
			if got.Class != pgfault.Unclassified {
				t.Fatalf("класс %v на входе без предмета — классификатор отвечает шире, чем знает", got.Class)
			}
		})
	}
}

// SQLSTATE остаётся ДОСТУПНЫМ и на неразобранном входе: сервис вправе доразобрать
// код, которого дом не классифицирует (полоса учёта, полоса доступности), — и
// именно так его отступление перестаёт быть молчаливым.
func TestFaultCarriesTheRawCoordinatesForTheServiceToGoOn(t *testing.T) {
	f := pgfault.Classify(pgErr("KQ001", "quota_trg", "volumes"))
	if f.SQLState != "KQ001" {
		t.Fatalf("SQLSTATE %q — доразобрать неклассифицированный код нечем", f.SQLState)
	}
	if !f.FromDatabase() {
		t.Fatalf("FromDatabase()=false на ошибке драйвера — «отказ хранилища» неотличим от прочего")
	}
	f2 := pgfault.Classify(errors.New("plain"))
	if f2.FromDatabase() {
		t.Fatalf("FromDatabase()=true на обычной ошибке — признак отвечает всем")
	}
	f3 := pgfault.Classify(pgErr("23505", "accounts_name_unique", "accounts"))
	if f3.Constraint != "accounts_name_unique" || f3.Table != "accounts" {
		t.Fatalf("координаты ограничения потеряны: constraint=%q table=%q — таблица «ограничение → текст» у сервиса станет неразрешимой",
			f3.Constraint, f3.Table)
	}
}

// Ветка по умолчанию даёт ФИКСИРОВАННЫЙ непрозрачный текст (`security.md`
// Hardening #1). Утверждается не «текст такой-то», а то, что текста драйвера в
// нём НЕТ: иначе проба закрепила бы формулировку, а не свойство.
func TestOpaqueMessageCarriesNothingFromTheDriver(t *testing.T) {
	const driverText = "some driver text that must never reach the caller"
	if pgfault.OpaqueMessage == "" {
		t.Fatal("непрозрачный текст пуст — ветка по умолчанию не имеет что сказать")
	}
	if got := pgfault.OpaqueMessage; got == driverText {
		t.Fatalf("непрозрачный текст совпал с текстом драйвера: %q", got)
	}
}

// Полоса 23514 — «чьё это значение». Разбор жил ПЯТЬЮ копиями с дословно
// совпадающим журналом; здесь он один, и обе его стороны утверждаются.
func TestCheckLaneSeparatesOurDefectFromCallerInput(t *testing.T) {
	// Ограничение формы имени сервис проверяет САМ, поэтому его срабатывание
	// означает, что негодное значение прошло мимо проверки: наш дефект.
	ours := pgfault.Classify(pgErr("23514", "accounts_name_check", "accounts"))
	if lane := pgfault.CheckLaneOf(ours); lane != pgfault.LaneServiceDefect {
		t.Fatalf("полоса %v на ограничении формы имени — отказ обвинил бы вызывающего, а исправить ему нечего", lane)
	}
	// Всё остальное проверяет только база — отказ по вводу уместен.
	theirs := pgfault.Classify(pgErr("23514", "accounts_description_check", "accounts"))
	if lane := pgfault.CheckLaneOf(theirs); lane != pgfault.LaneCallerInput {
		t.Fatalf("полоса %v на прикладном ограничении — наш дефект объявлен там, где его нет", lane)
	}
	// Предпосылка полосы: она задаётся только для 23514. Спрошенная о другом
	// классе, она обязана сказать «не моё», а не назвать полосу наугад.
	other := pgfault.Classify(pgErr("23505", "accounts_name_check", "accounts"))
	if lane := pgfault.CheckLaneOf(other); lane != pgfault.LaneNotApplicable {
		t.Fatalf("полоса %v вне 23514 — разбор отвечает о том, чего не спрашивали", lane)
	}
}
