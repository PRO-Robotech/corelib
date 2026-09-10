// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package pgfault — дом ОДНОГО правила: отказ хранилища, разобранный по классу.
//
// # Что здесь живёт, а что НЕТ
//
// Здесь — только КЛАСС отказа: какому роду инварианта не сошлось. Здесь НЕ живёт
// текст: тон сообщений — часть контракта (`api-conventions.md` §Error-format),
// он принадлежит владельцу ресурса и у разных ресурсов законно разный
// («Volume %s is in use» против «Account %s contains projects»). Дом, взявший на
// себя текст, потребовал бы менять контракт ради централизации — то есть платил
// бы наружу за удобство внутри.
//
// Отсюда форма: `Classify` отвечает классом и отдаёт координаты ограничения
// (имя, таблица, столбец), по которым сервис выбирает СВОЙ текст. Разделение
// проведено по тому, что правило называет одним, а не по тому, что удобно свести.
//
// # Почему класс, а не сразу код gRPC
//
// Код отказа зависит не только от класса, но и от ПОЛОСЫ, в которой резолвился
// идентификатор (`api-conventions.md` §«By-lane code-split»): нарушение внешнего
// ключа на своей строке и на чужой ссылке читается разными кодами. Полоса — знание
// вызывающего, не хранилища. Дом, назначающий код, отнял бы у сервиса решение,
// которое тот единственный может принять верно.
//
// # Ветка по умолчанию
//
// `Unclassified` означает ровно «дом об этом коде ничего не утверждает», и это
// НЕ синоним внутреннего сбоя: под него попадают собственные коды продукта
// (учёт величин) и коды доступности сервера. Сервис вправе доразобрать
// `Fault.SQLState` сам — тогда его отступление названо, а не молчаливо. Текст,
// уходящий наружу в этой ветке, — `OpaqueMessage` и только он
// (`security.md` §Hardening-инварианты, п.1: INTERNAL никогда не эхает текст
// драйвера).
//
// # Границы, названные вслух
//
//  1. Дом судит по ТИПУ ошибки драйвера (`*pgconn.PgError`), а не по тексту.
//     Ошибка, потерявшая тип по дороге (пересобранная через `errors.New` из
//     `err.Error()`), сюда не доедет — и это правильно: восстановление класса из
//     текста было бы догадкой.
//  2. Коды готовности сервера к обслуживанию (`pkg/dbready`) — ДРУГОЙ предмет:
//     там спрашивают «повторить ли ожидание на подъёме», здесь — «какой отказ
//     отдать вызывающему». Пересечение кодов есть, вопрос разный, и сводить их
//     значило бы отвечать одним ответом на два вопроса.
package pgfault

import (
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/corelib/validate/nameform"
)

// Class — род отказа хранилища.
//
// Перечень закрыт намеренно: он покрывает ровно то, что корпус правил называет
// одним правилом, плюс смежные коды, которые сервисы дерева уже разбирают. Код
// вне перечня остаётся `Unclassified` и доступен через `Fault.SQLState` — дом
// молчит о том, чего не знает, вместо того чтобы назвать род наугад.
type Class uint8

const (
	// Unclassified — дом об этом отказе ничего не утверждает. НЕ синоним
	// внутреннего сбоя: сюда попадают и собственные коды продукта, и коды
	// доступности сервера.
	Unclassified Class = iota
	// NoRows — запрос не вернул строки (`pgx.ErrNoRows`). Не SQLSTATE, но того
	// же рода вопрос, и разбирается он всеми теми же местами.
	NoRows
	// IntegrityConstraint — 23000, integrity_constraint_violation. В этом дереве
	// поднимается ЯВНО триггером схемы, а не сервером на ограничении таблицы.
	IntegrityConstraint
	// NotNull — 23502, not_null_violation.
	NotNull
	// ForeignKey — 23503, foreign_key_violation. Направление-нейтрален: тот же
	// код приходит и на удаление родителя с детьми, и на вставку ссылки в
	// пустоту. Различает их вызывающий по имени ограничения, не дом.
	ForeignKey
	// Unique — 23505, unique_violation.
	Unique
	// Check — 23514, check_violation. Разбирается на полосы через `CheckLaneOf`.
	Check
	// Exclusion — 23P01, exclusion_violation.
	Exclusion
	// InvalidText — 22P02, invalid_text_representation: значение не приводится к
	// типу столбца.
	InvalidText
	// SerializationConflict — 40001 serialization_failure либо 40P01
	// deadlock_detected. Повторяемый класс: транзакция может быть переиграна.
	SerializationConflict
)

// String — имя класса для журнала и текста отказа пробы.
func (c Class) String() string {
	switch c {
	case NoRows:
		return "NoRows"
	case IntegrityConstraint:
		return "IntegrityConstraint"
	case NotNull:
		return "NotNull"
	case ForeignKey:
		return "ForeignKey"
	case Unique:
		return "Unique"
	case Check:
		return "Check"
	case Exclusion:
		return "Exclusion"
	case InvalidText:
		return "InvalidText"
	case SerializationConflict:
		return "SerializationConflict"
	}
	return "Unclassified"
}

// SQLSTATE'ы, которые дом классифицирует. Объявлены поимённо, чтобы перечень
// читался вместе с классами, а не восстанавливался из switch.
const (
	sqlStateIntegrityConstraint = "23000"
	sqlStateNotNull             = "23502"
	sqlStateForeignKey          = "23503"
	sqlStateUnique              = "23505"
	sqlStateCheck               = "23514"
	sqlStateExclusion           = "23P01"
	sqlStateInvalidText         = "22P02"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlock            = "40P01"
)

// Fault — отказ хранилища, разобранный по классу.
//
// Координаты (`Constraint`, `Table`, `Column`) существуют ради таблицы
// «ограничение → текст», которую ведёт владелец ресурса. `Message` и `Detail` —
// слова СУБД: они годны для журнала оператора и НИКОГДА для ответа вызывающему.
type Fault struct {
	// Class — род отказа.
	Class Class
	// SQLState — код как есть. Непуст для всякого отказа, пришедшего от сервера,
	// включая тот, чей класс дому неизвестен.
	SQLState string
	// Constraint — имя нарушенного ограничения (пусто, если сервер его не назвал).
	Constraint string
	// Table — таблица, на которой сработало ограничение.
	Table string
	// Column — столбец (для 23502 и части 23514).
	Column string
	// Message — текст СУБД. ТОЛЬКО для журнала.
	Message string
	// Detail — уточнение СУБД. ТОЛЬКО для журнала: оно несёт значения строки.
	Detail string

	// fromDB — отличает «сервер ответил отказом» от «до сервера не дошло».
	// Отдельным полем, а не выводом из непустоты SQLState: сервер вправе не
	// назвать код, и тогда вывод по пустоте был бы неверен.
	fromDB bool
}

// Is — принадлежит ли отказ названному классу.
func (f Fault) Is(c Class) bool { return f.Class == c }

// FromDatabase — пришёл ли отказ от сервера БД вообще. Отличает «сервер отверг»
// от «соединение оборвалось / контекст истёк»: у этих двух разные исходы, и
// сваливать их в один означало бы объявлять отказом хранилища то, чего хранилище
// не говорило.
func (f Fault) FromDatabase() bool { return f.fromDB }

// Classify разбирает ошибку по классу.
//
// Читает цепочку `errors.As`, поэтому обёртка через `%w` класс не теряет.
func Classify(err error) Fault {
	if err == nil {
		return Fault{}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		f := Fault{
			SQLState:   pgErr.Code,
			Constraint: pgErr.ConstraintName,
			Table:      pgErr.TableName,
			Column:     pgErr.ColumnName,
			Message:    pgErr.Message,
			Detail:     pgErr.Detail,
			fromDB:     true,
		}
		f.Class = classOf(pgErr.Code)
		return f
	}
	// Порядок несущий: `pgx.ErrNoRows` проверяется ПОСЛЕ типа сервера. Отказ
	// сервера и отсутствие строки — разные факты, и один запрос вправе нести
	// только один из них; проверка отсутствия строки первой была бы верна, но
	// поставила бы менее определённый ответ впереди более определённого.
	if errors.Is(err, pgx.ErrNoRows) {
		return Fault{Class: NoRows}
	}
	return Fault{}
}

// classOf — единственное место, где код превращается в род.
func classOf(code string) Class {
	switch code {
	case sqlStateIntegrityConstraint:
		return IntegrityConstraint
	case sqlStateNotNull:
		return NotNull
	case sqlStateForeignKey:
		return ForeignKey
	case sqlStateUnique:
		return Unique
	case sqlStateCheck:
		return Check
	case sqlStateExclusion:
		return Exclusion
	case sqlStateInvalidText:
		return InvalidText
	case sqlStateSerializationFail, sqlStateDeadlock:
		return SerializationConflict
	}
	return Unclassified
}

// OpaqueMessage — ЕДИНСТВЕННЫЙ текст, которым ветка по умолчанию отвечает
// вызывающему.
//
// `security.md` §Hardening-инварианты, п.1: INTERNAL никогда не эхает текст
// драйвера — иначе наружу уезжают имя узла, база, пользователь и обрывок запроса.
// Константа объявлена здесь, а не у каждого сервиса, чтобы «фиксированный текст»
// было чем проверить: гейт сверяет ИДЕНТИЧНОСТЬ с этим объявлением, а не наличие
// какой-нибудь строки.
const OpaqueMessage = "internal database error"

// Lane — полоса разбора нарушения проверки (23514): чьё это значение.
type Lane uint8

const (
	// LaneNotApplicable — вопрос задан не о том классе. Отдельное значение, а не
	// «ввод по умолчанию»: молчаливый ответ наугад тут означал бы обвинение
	// вызывающего в том, о чём его не спрашивали.
	LaneNotApplicable Lane = iota
	// LaneServiceDefect — сработало ограничение, форму которого сервис проверяет
	// САМ. Значит негодное значение прошло МИМО проверки — наш дефект, и
	// вызывающему нечего исправлять.
	LaneServiceDefect
	// LaneCallerInput — форму проверяет только база; отказ по вводу уместен.
	LaneCallerInput
)

// String — имя полосы для текста отказа пробы.
func (l Lane) String() string {
	switch l {
	case LaneServiceDefect:
		return "LaneServiceDefect"
	case LaneCallerInput:
		return "LaneCallerInput"
	}
	return "LaneNotApplicable"
}

// CheckLaneOf разбирает нарушение проверки на две полосы по одному вопросу: чьё
// это значение — вызывающего или наше.
//
// Разделяет их конструкция имени ограничения (`nameform.IsConstraint`), которую
// задаёт миграция, а не догадка. Разбор жил ПЯТЬЮ дословными копиями — по одной
// у vpc, compute, storage, nlb и iam; правка одной из них до остальных не
// доезжала и не доезжала молча.
//
// Журнала здесь НЕТ намеренно: полоса — это факт, а запись о нём принадлежит
// вызывающему, у которого есть его собственные координаты (вид ресурса, его
// идентификатор). Функция, писавшая бы журнал сама, лишила бы запись именно того,
// ради чего её читают.
func CheckLaneOf(f Fault) Lane {
	if f.Class != Check {
		return LaneNotApplicable
	}
	if nameform.IsConstraint(f.Table, f.Constraint) {
		return LaneServiceDefect
	}
	return LaneCallerInput
}

// LogAttrs — координаты отказа для журнала оператора, одним набором.
//
// Существует затем, чтобы «неразобранный отказ» был счётным одинаково во всех
// сервисах: сегодня одни пишут SQLSTATE, другие текст СУБД, третьи не пишут
// ничего — и «ноль неразобранных за всю жизнь» неотличимо от «никто не смотрел».
func (f Fault) LogAttrs() []any {
	return []any{
		slog.String("sqlstate", f.SQLState),
		slog.String("constraint", f.Constraint),
		slog.String("table", f.Table),
		slog.String("pg_message", f.Message),
	}
}
