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
)

// Versions — выпущенные версии по возрастанию.
func Versions() []Version { return []Version{V1, V2} }

// Current — действующая версия.
func Current() Version { return V2 }

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

// vocabularyOf — словарь выпущенной версии v.
func vocabularyOf(v Version) (vocabulary, bool) {
	switch v {
	case V1:
		return v1Vocabulary(), true
	case V2:
		return v2Vocabulary(), true
	}
	return vocabulary{}, false
}

// States — закрытый перечень состояний строки ленты действующей версии (З6):
// pending либо вид терминального исхода.
func States() []string { return v2Vocabulary().states }

// OutcomePairs — ЕДИНСТВЕННАЯ таблица «состояние × причина» действующей
// версии (Р11, Р17, словарь контракта corelib.notify). CHECK outcome_pair
// строится из неё, сервер ленты судит по ней Ack. Пустой перечень — у
// состояния причины нет.
func OutcomePairs() map[string][]string { return v2Vocabulary().pairs }

// DDL — операторы версии v для службы svc, создающие ленту с нуля: up и down.
func DDL(svc string, v Version) (up, down string, err error) {
	if err := tablename.Valid(svc); err != nil {
		return "", "", fmt.Errorf("schema: %w", err)
	}
	voc, ok := vocabularyOf(v)
	if !ok {
		return "", "", fmt.Errorf("schema: версия %d не выпущена", int(v))
	}
	return tablesUp(svc, voc), tablesDown(svc), nil
}

// UpgradeDDL — операторы перехода ленты службы svc с применённой версии from
// на версию to: up и down. Выпущен один переход — V1 → V2: он снимает и
// ставит заново два ограничения строки ленты, CHECK состояния и outcome_pair,
// со словарём версии; таблиц не создаёт.
func UpgradeDDL(svc string, from, to Version) (up, down string, err error) {
	if err := tablename.Valid(svc); err != nil {
		return "", "", fmt.Errorf("schema: %w", err)
	}
	if from != V1 || to != V2 {
		return "", "", fmt.Errorf("schema: переход v%d→v%d не выпущен", int(from), int(to))
	}
	return vocabularyChange(svc, v2Vocabulary()), vocabularyChange(svc, v1Vocabulary()), nil
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

// tablesUp — лента с нуля со словарём voc. Результат tablename.Of только
// аргументом Sprintf, повтор вида — %[n]s (ведомость писателей таблиц ленты,
// УК90, УК92).
func tablesUp(svc string, voc vocabulary) string {
	return fmt.Sprintf(`CREATE TABLE %[1]s (
  id                text        PRIMARY KEY,
  template          text        NOT NULL,
  schema_rev        integer     NOT NULL CHECK (schema_rev >= 1),
  class             text        NOT NULL CHECK (class IN ('security', 'notice')),
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
  expires_at        timestamptz NOT NULL,
  CONSTRAINT closed_carries_no_secret CHECK (state = 'pending' OR secret_attrs IS NULL),
  CONSTRAINT outcome_matches_state CHECK ((state <> 'pending') = (outcome_at IS NOT NULL)),
  CONSTRAINT outcome_pair CHECK (
    %[5]s)
);
CREATE INDEX %[6]s ON %[1]s (class, enqueued_at, id) WHERE state = 'pending';
CREATE INDEX %[7]s ON %[1]s (outcome_at) WHERE state <> 'pending';
CREATE TABLE %[2]s (
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
		quoteList(voc.states), voc.outcomePair(),
		tablename.Index(svc, tablename.Outbox, tablename.Pending), tablename.Index(svc, tablename.Outbox, tablename.Closed))
}

func tablesDown(svc string) string {
	return fmt.Sprintf("DROP TABLE %s;\nDROP TABLE %s;\nDROP TABLE %s;\n",
		tablename.Of(svc, tablename.Contrib), tablename.Of(svc, tablename.Window), tablename.Of(svc, tablename.Outbox))
}
