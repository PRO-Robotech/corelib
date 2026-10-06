// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package schema строит DDL ленты извещений у источника (§6, З6). Миграцию
// пишет notifygen init; генератор держит побайтовое содержимое каждой
// выпущенной версии (её даёт эта функция) и сверяет с ним файлы дерева (D04).
//
// Миграция несёт только таблицы, индексы и ограничения: ни функции, ни
// процедуры, ни триггера (УК87 (а)).
package schema

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
)

// Version — версия схемы ленты.
type Version int

// Выпущенные версии.
const (
	// V1 — схема NTF-1.
	V1 Version = 1
	// V2 — схема NTF-4 (Х3, Р17): словарь исходов расширен терминальным
	// состоянием suppressed с закрытым перечнем причин.
	V2 Version = 2
	// V3 — схема NTF-5 (C5; замысел issue-2924 З8 п.5, З10 п.2, З12 п.1):
	// класс obligation без срока (expires_at NULL ровно у него), ключ нити
	// thread_key с одним написанием отсутствия и индексом головы нити,
	// терминальное состояние superseded с одной причиной.
	V3 Version = 3
)

// Versions — выпущенные версии по возрастанию.
func Versions() []Version { return []Version{V1, V2, V3} }

// Current — действующая версия.
func Current() Version { return V3 }

// vocabulary — словарь состояний строки ленты одной версии: перечень
// состояний (порядок — порядок CHECK состояния) и таблица «состояние ×
// причина» (CHECK outcome_pair).
type vocabulary struct {
	states []string
	pairs  map[string][]string
	// reasonRequired — CHECK outcome_pair требует причину у состояния с
	// причинами (с V2; outcomePairRequired).
	reasonRequired bool
}

// outcomePair — выражение CHECK outcome_pair словаря.
func (v vocabulary) outcomePair() string {
	if v.reasonRequired {
		return outcomePairRequired(v.pairs)
	}
	return outcomePairCheck(v.pairs)
}

// v1Vocabulary — словарь V1 как выпущен. Не правится: миграция V1 применена
// у служб, и генератор сверяет её побайтово (D04, ban #5).
func v1Vocabulary() vocabulary {
	return vocabulary{
		states: []string{"pending", "sent", "recipient_rejected", "denied", "invalid", "dropped", "expired"},
		pairs: map[string][]string{
			"pending":            nil,
			"sent":               nil,
			"recipient_rejected": nil,
			"denied":             {"revoked"},
			"invalid": {"attrs_invalid", "class_mismatch", "recipient_invalid", "class_not_allowed",
				"recipient_form_not_allowed", "sealed_mismatch", "key_unavailable"},
			"dropped": {"recipient_net"},
			"expired": {"platform_unavailable", "grant_skew", "template_skew", "recipient_net", "unclaimed", "no_ack"},
		},
	}
}

// suppressed — терминальное состояние Х3 NTF-4: письмо подавлено notify.
const suppressed = "suppressed"

// v2Vocabulary — словарь V1 плюс suppressed с закрытым перечнем причин Р17.
// Ни одна из причин подавления не допускается при другом состоянии; у
// состояния с причинами причина обязательна.
func v2Vocabulary() vocabulary {
	v := v1Vocabulary()
	v.states = append(v.states, suppressed)
	v.pairs[suppressed] = []string{"hard_bounce", "soft_bounce", "complaint", "unsubscribe"}
	v.reasonRequired = true
	return v
}

// superseded — терминальное состояние C5: строка стала неактуальна до
// отправки (замысел issue-2924 З8 п.5). Ставит его только feed.Supersede.
const superseded = "superseded"

// SupersededReason — единственная причина состояния superseded: строка
// неактуальна. Вне контракта сети corelib.notify: Ack её не принимает.
const SupersededReason = "not_current"

// v3Vocabulary — словарь V2 плюс superseded с одной причиной.
func v3Vocabulary() vocabulary {
	v := v2Vocabulary()
	v.states = append(v.states, superseded)
	v.pairs[superseded] = []string{SupersededReason}
	return v
}

// ClassObligation — класс строки без срока (NTF-5 Р12, §3 З19): с V3.
const ClassObligation = "obligation"

// shape — строение ленты одной версии сверх словаря состояний: классы
// (порядок — порядок CHECK класса) и признаки C5.
type shape struct {
	voc     vocabulary
	classes []string
	// c5 — expires_at допускает NULL ровно у класса obligation
	// (expiry_matches_class), столбец thread_key с thread_key_present и
	// индексом головы нити.
	c5 bool
}

// shapeOf — строение выпущенной версии v.
func shapeOf(v Version) (shape, bool) {
	base := []string{"security", "notice"}
	switch v {
	case V1:
		return shape{voc: v1Vocabulary(), classes: base}, true
	case V2:
		return shape{voc: v2Vocabulary(), classes: base}, true
	case V3:
		return shape{voc: v3Vocabulary(), classes: append(base, ClassObligation), c5: true}, true
	}
	return shape{}, false
}

// States — закрытый перечень состояний строки ленты действующей версии (З6):
// pending либо вид терминального исхода.
func States() []string { return v3Vocabulary().states }

// Classes — закрытый перечень классов строки ленты действующей версии.
func Classes() []string {
	sh, _ := shapeOf(Current())
	return sh.classes
}

// OutcomePairs — ЕДИНСТВЕННАЯ таблица «состояние × причина» действующей
// версии (Р11, Р17, словарь контракта corelib.notify и состояние superseded
// ленты у источника). CHECK outcome_pair строится из неё, сервер ленты судит
// по ней Ack. Пустой перечень — у состояния причины нет.
func OutcomePairs() map[string][]string { return v3Vocabulary().pairs }

// DDL — операторы версии v для службы svc, создающие ленту с нуля: up и down.
func DDL(svc string, v Version) (up, down string, err error) {
	if err := tablename.Valid(svc); err != nil {
		return "", "", fmt.Errorf("schema: %w", err)
	}
	sh, ok := shapeOf(v)
	if !ok {
		return "", "", fmt.Errorf("schema: версия %d не выпущена", int(v))
	}
	return tablesUp(svc, sh), tablesDown(svc), nil
}

// UpgradeDDL — операторы перехода ленты службы svc с применённой версии from
// на более новую выпущенную версию to: up и down. Переход прямой, а не цепочка
// шагов: строение ленты from меняется на строение to (C5 — столбцы, классы и
// их ограничения, structureChange), затем CHECK состояния и outcome_pair
// снимаются и ставятся заново со словарём to (vocabularyChange); down — то же
// в обратную сторону, прямо к from. Таблиц переход не создаёт. Переход V1 → V2
// побайтно тот, что выпущен: строение у них одно.
func UpgradeDDL(svc string, from, to Version) (up, down string, err error) {
	if err := tablename.Valid(svc); err != nil {
		return "", "", fmt.Errorf("schema: %w", err)
	}
	a, okFrom := shapeOf(from)
	b, okTo := shapeOf(to)
	if !okFrom || !okTo || from >= to {
		return "", "", fmt.Errorf("schema: переход v%d→v%d не выпущен", int(from), int(to))
	}
	return structureChange(svc, a, b) + vocabularyChange(svc, b.voc),
		structureChange(svc, b, a) + vocabularyChange(svc, a.voc), nil
}

// HeaderPrefix — начало строки заголовка миграции ленты: по ней notifygen
// -check узнаёт службу и версию применённого файла и сверяет его побайтово с
// выпущенным содержимым (D04).
const HeaderPrefix = "-- notifygen feed schema:"

// Migration — файл миграции goose версии v для службы svc: лента с нуля.
func Migration(svc string, v Version) (string, error) {
	up, down, err := DDL(svc, v)
	if err != nil {
		return "", err
	}
	return migrationFile(fmt.Sprintf("service=%s version=%d", svc, int(v)), up, down), nil
}

// Upgrade — файл миграции goose перехода ленты службы svc с версии from на
// to. Заголовок несёт from: по нему notifygen -check узнаёт переход (D04).
func Upgrade(svc string, from, to Version) (string, error) {
	up, down, err := UpgradeDDL(svc, from, to)
	if err != nil {
		return "", err
	}
	return migrationFile(fmt.Sprintf("service=%s version=%d from=%d", svc, int(to), int(from)), up, down), nil
}

func migrationFile(header, up, down string) string {
	var b strings.Builder
	b.WriteString("-- Code generated by notifygen init; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "%s %s\n\n", HeaderPrefix, header)
	b.WriteString("-- +goose Up\n")
	b.WriteString(up)
	b.WriteString("\n-- +goose Down\n")
	b.WriteString(down)
	return b.String()
}

func quoteList(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = "'" + w + "'"
	}
	return strings.Join(q, ", ")
}

// outcomePairCheck — выражение CHECK outcome_pair: у состояния без причин
// причина NULL, у прочих — из своего перечня.
func outcomePairCheck(pairs map[string][]string) string {
	return strings.Join(outcomePairCells(pairs), "\n    OR ")
}

// outcomePairRequired — выражение CHECK outcome_pair с V2: клетки
// outcomePairCheck и сверх них — у состояния с причинами причина не NULL.
// CHECK над NULL не ложен, и одних клеток недостаточно: «denied без причины»
// дала бы NULL в своей клетке и прошла бы (так у V1, применённой и
// неизменной).
func outcomePairRequired(pairs map[string][]string) string {
	var bare []string
	for _, s := range sortedStates(pairs) {
		if len(pairs[s]) == 0 {
			bare = append(bare, s)
		}
	}
	return fmt.Sprintf("(%s)\n    AND (outcome_reason IS NOT NULL OR state IN (%s))",
		strings.Join(outcomePairCells(pairs), "\n    OR "), quoteList(bare))
}

func sortedStates(pairs map[string][]string) []string {
	states := make([]string, 0, len(pairs))
	for s := range pairs {
		states = append(states, s)
	}
	sort.Strings(states)
	return states
}

// outcomePairCells — клетки «состояние × причина» по состояниям в порядке
// имён.
func outcomePairCells(pairs map[string][]string) []string {
	var parts []string
	for _, s := range sortedStates(pairs) {
		if len(pairs[s]) == 0 {
			parts = append(parts, fmt.Sprintf("(state = '%s' AND outcome_reason IS NULL)", s))
			continue
		}
		parts = append(parts, fmt.Sprintf("(state = '%s' AND outcome_reason IN (%s))", s, quoteList(pairs[s])))
	}
	return parts
}

// vocabularyChange — переход ленты svc на словарь voc: CHECK состояния (имя,
// которое сервер дал ограничению колонки, — tablename.StateCheck) и
// outcome_pair снимаются и ставятся заново одним оператором. Результат
// tablename.Of только аргументом Sprintf (УК90, УК92).
func vocabularyChange(svc string, voc vocabulary) string {
	return fmt.Sprintf(`ALTER TABLE %[1]s
  DROP CONSTRAINT %[2]s,
  ADD CONSTRAINT %[2]s CHECK (state IN (%[3]s)),
  DROP CONSTRAINT outcome_pair,
  ADD CONSTRAINT outcome_pair CHECK (
    %[4]s);
`, tablename.Of(svc, tablename.Outbox), tablename.StateCheck(svc), quoteList(voc.states), voc.outcomePair())
}

// c5Constraints — ограничения строки ленты C5 (замысел issue-2924 З10 п.2, З12
// п.1): срок есть ровно у строк не класса obligation (строка security или
// notice без срока невыразима, CX5-31 (б)); отсутствие ключа нити — одно
// написание, NULL (CX5-52 (а)).
var c5Constraints = []string{
	"CONSTRAINT expiry_matches_class CHECK ((expires_at IS NULL) = (class = '" + ClassObligation + "'))",
	"CONSTRAINT thread_key_present CHECK (thread_key IS NULL OR thread_key <> '')",
}

// c5Fragments — части оператора ленты с нуля, которыми V3 отличается от V1 и
// V2: у прежних версий части пусты либо прежние, и их текст побайтно тот, что
// выпущен (D04).
func c5Fragments(svc string, sh shape) (expiresNotNull, threadCol, constraints, threadIndex string) {
	if !sh.c5 {
		return " NOT NULL", "", "", ""
	}
	return "", "  thread_key        text,\n",
		",\n  " + strings.Join(c5Constraints, ",\n  "),
		threadIndexDDL(svc)
}

// threadIndexDDL — частичный индекс головы нити (З12 п.1).
func threadIndexDDL(svc string) string {
	return fmt.Sprintf("CREATE INDEX %s ON %s (thread_key, enqueued_at, id) WHERE state = 'pending' AND thread_key IS NOT NULL;\n",
		tablename.Index(svc, tablename.Outbox, tablename.Thread), tablename.Of(svc, tablename.Outbox))
}

// structureChange — переход строения ленты svc с from на to: столбцы и
// ограничения C5, CHECK класса. Одно строение — пустая строка. Ограничение
// класса снимается и ставится под именем, которое сервер дал ему в ленте с
// нуля (tablename.ClassCheck); индекс нити снимает удаление его столбца.
func structureChange(svc string, from, to shape) string {
	if from.c5 == to.c5 && slices.Equal(from.classes, to.classes) {
		return ""
	}
	var parts []string
	switch {
	case to.c5 && !from.c5:
		parts = append(parts, "ALTER COLUMN expires_at DROP NOT NULL", "ADD COLUMN thread_key text")
	case from.c5 && !to.c5:
		for i := len(c5Constraints) - 1; i >= 0; i-- {
			parts = append(parts, "DROP CONSTRAINT "+strings.Fields(c5Constraints[i])[1])
		}
		parts = append(parts, "DROP COLUMN thread_key")
	}
	parts = append(parts,
		"DROP CONSTRAINT "+tablename.ClassCheck(svc),
		"ADD CONSTRAINT "+tablename.ClassCheck(svc)+" CHECK (class IN ("+quoteList(to.classes)+"))")
	switch {
	case to.c5 && !from.c5:
		for _, c := range c5Constraints {
			parts = append(parts, "ADD "+c)
		}
	case from.c5 && !to.c5:
		parts = append(parts, "ALTER COLUMN expires_at SET NOT NULL")
	}
	out := fmt.Sprintf("ALTER TABLE %s\n  %s;\n", tablename.Of(svc, tablename.Outbox), strings.Join(parts, ",\n  "))
	if to.c5 && !from.c5 {
		out += threadIndexDDL(svc)
	}
	return out
}

// tablesUp — лента с нуля строения sh. Результат tablename.Of только
// аргументом Sprintf, повтор вида — %[n]s (ведомость писателей таблиц ленты,
// УК90, УК92).
func tablesUp(svc string, sh shape) string {
	expiresNotNull, threadCol, constraints, threadIndex := c5Fragments(svc, sh)
	return fmt.Sprintf(`CREATE TABLE %[1]s (
  id                text        PRIMARY KEY,
  template          text        NOT NULL,
  schema_rev        integer     NOT NULL CHECK (schema_rev >= 1),
  class             text        NOT NULL CHECK (class IN (%[8]s)),
  recipient_address text        NOT NULL,
  attrs             jsonb       NOT NULL,
  secret_attrs      bytea,
  state             text        NOT NULL CHECK (state IN (%[4]s)),
  outcome_reason    text,
  outcome_at        timestamptz,
  recorded_kind     text,
  recorded_reason   text,
  outcome_token     uuid,
  lease_token       uuid,
  lease_until       timestamptz,
  first_claimed_at  timestamptz,
  last_defer_reason text,
  not_before        timestamptz CHECK (not_before IS NULL OR isfinite(not_before)),
  enqueued_at       timestamptz NOT NULL DEFAULT now(),
  expires_at        timestamptz%[9]s,
%[10]s  CONSTRAINT closed_carries_no_secret CHECK (state = 'pending' OR secret_attrs IS NULL),
  CONSTRAINT outcome_matches_state CHECK ((state <> 'pending') = (outcome_at IS NOT NULL)),
  CONSTRAINT outcome_pair CHECK (
    %[5]s)%[11]s
);
CREATE INDEX %[6]s ON %[1]s (class, enqueued_at, id) WHERE state = 'pending';
CREATE INDEX %[7]s ON %[1]s (outcome_at) WHERE state <> 'pending';
%[12]sCREATE TABLE %[2]s (
  template       text        NOT NULL,
  scope          text        NOT NULL,
  window_seconds integer     NOT NULL CHECK (window_seconds > 0),
  key            text        NOT NULL,
  window_start   timestamptz NOT NULL,
  count          integer     NOT NULL CHECK (count >= 0),
  PRIMARY KEY (template, scope, window_seconds, key, window_start)
);
CREATE TABLE %[3]s (
  notification_id text        NOT NULL REFERENCES %[1]s (id) ON DELETE CASCADE,
  template        text        NOT NULL,
  scope           text        NOT NULL,
  window_seconds  integer     NOT NULL CHECK (window_seconds > 0),
  key             text        NOT NULL,
  window_start    timestamptz NOT NULL,
  PRIMARY KEY (notification_id, scope, window_seconds)
);
`, tablename.Of(svc, tablename.Outbox), tablename.Of(svc, tablename.Window), tablename.Of(svc, tablename.Contrib),
		quoteList(sh.voc.states), sh.voc.outcomePair(),
		tablename.Index(svc, tablename.Outbox, tablename.Pending), tablename.Index(svc, tablename.Outbox, tablename.Closed),
		quoteList(sh.classes), expiresNotNull, threadCol, constraints, threadIndex)
}

func tablesDown(svc string) string {
	return fmt.Sprintf("DROP TABLE %s;\nDROP TABLE %s;\nDROP TABLE %s;\n",
		tablename.Of(svc, tablename.Contrib), tablename.Of(svc, tablename.Window), tablename.Of(svc, tablename.Outbox))
}
