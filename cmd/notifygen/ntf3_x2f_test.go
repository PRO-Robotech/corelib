// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// Пробы полосы X2-F NTF-3 (приёмка sub-phase-NTF-3, отпечаток ac1f9fc9…;
// замысел issue-2918 З10 п.1–5, «Версии тела») на форме fanout генератора:
// notifygen init пишет функцию базы resource-event и её триггер на журнале
// модуля отдельной миграцией (миграция ленты v1 остаётся побайтово выпущенной,
// NTF1-D03); notifygen -check извлекает входы из тела и сверяет его с выводом
// шаблона на них, сверяет SQL-половину с Go-половиной по атрибутам и по строке
// сигнала, а действующее тело — с объявлением журнала; смена объявления —
// новая миграция CREATE OR REPLACE той же функции.
//
// Вход генератора о журнале модуля — объявление journal.yaml, которое
// пишет fanoutDecl и передаёт флаг -journal (initFanout). Это единственная
// точка, где пробы называют форму входа: замысел говорит «notifygen читает
// Mapping» и механизма не называет. Сменится форма входа — меняются эти два
// помощника, утверждения проб — нет.

import (
	"os"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
	"github.com/PRO-Robotech/corelib/notify/spec"
)

// fanoutNotification — шаблон resource-event формы fanout: атрибуты
// закрытого списка Р3, кроме initiator — это имя занято ключом окна
// постановки (notify/spec reservedAttrNames); name — optional (З17).
const fanoutNotification = `name: resource-event
class: notice
ttl: 72h
recipient: fanout
attributes:
  kind: {type: text, presence: required}
  resource_id: {type: text, presence: required}
  scope: {type: text, presence: required}
  change: {type: text, presence: required}
  occurred_at: {type: timestamp, presence: required}
  name: {type: text, presence: optional}
subject:
  ru: "{{ kind }} {{ resource_id }}: {{ change }}"
  en: "{{ kind }} {{ resource_id }}: {{ change }}"
`

const fanoutBody = `blocks:
  - p: "{{ kind }} {{ resource_id }} {{ scope }} {{ change }} {{ occurred_at }}"
  - p: "{{ name }}"
    when: name
`

// fanoutJournal — объявление журнала модуля svc: таблица, колонки, виды с
// формой имени и якорем, словарь рода изменения. Volume — вид с именем
// (NameFormDNS), Repository — без имени (NameFormNone), notification — ключ
// строки сигнала ленты (уровня кластера, без имени).
const fanoutJournal = `module: svc
table: svc_journal
columns:
  kind: resource_kind
  id: resource_id
  change: event_type
  payload: payload
  project: project_id
  initiator: initiator
  occurred_at: created_at
kinds:
  Volume: {name_form: dns, scope: project}
  Repository: {name_form: none, scope: project}
  notification: {name_form: none, scope: cluster}
changes:
  created: CREATED
  updated: UPDATED
  deleted: DELETED
`

const fanoutJournalPath = "svc/journal.yaml"

// fanoutTree — дерево A01 плюс шаблон resource-event формы fanout и
// объявление журнала; порождённое записано (make notifications). Провал
// построения — «проба не исполнилась», а не красный испытуемого.
func fanoutTree(t *testing.T) *tree {
	t.Helper()
	tr := newTree(t)
	tr.write("svc/notifications/resource-event/notification.yaml", fanoutNotification)
	tr.write("svc/notifications/resource-event/body.ru.yaml", fanoutBody)
	tr.write("svc/notifications/resource-event/body.en.yaml", fanoutBody)
	fanoutDecl(tr, fanoutJournal)
	if r := tr.run(); r.code != 0 {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: make notifications на фикстуре fanout: код %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	return tr
}

// fanoutDecl — объявление журнала модуля (вход генератора о Mapping).
func fanoutDecl(tr *tree, decl string) { tr.write(fanoutJournalPath, decl) }

// initFanout — notifygen init с объявлением журнала модуля.
func initFanout(tr *tree, o options) result {
	tr.t.Helper()
	return runWith(tr, o, "init", "-service", "svc", "-migrations", "svc/migrations", "-journal", fanoutJournalPath)
}

// fanoutFiles — файлы миграций svc/migrations, кроме миграции ленты
// действующей версии, по имени.
func fanoutFiles(t *testing.T, tr *tree) []string {
	t.Helper()
	var out []string
	for _, n := range migrations(t, tr) {
		if n != feedFile() {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func migrationPath(name string) string { return path.Join("svc/migrations", name) }

// Отказ, который печатает вызов init при отсутствии формы fanout, — текст
// флагового разбора; проба печатает его дословно в сообщении.
func requireInitOK(t *testing.T, r result) {
	t.Helper()
	require.Equal(t, 0, r.code, "notifygen init с объявлением журнала: stdout=%q stderr=%q", r.stdout, r.stderr)
}

// З10 п.1–5, Р3: init с объявлением журнала пишет миграцию ленты действующей версии
// побайтово выпущенной (функций в ней нет, NTF1-D03) и РЯДОМ — миграцию
// функции resource-event и её триггера AFTER INSERT на журнале модуля с
// условием WHEN, исключающим строку сигнала; функция читает флаг
// kacho_feed.enabled и пишет строку сигнала notification_feed:<модуль>
// (форма из notify/spec). Повторный init — изменений 0; -check зелёный.
func TestNTF3_X2F_InitWritesResourceEventFunctionBesideFeedMigration(t *testing.T) {
	tr := fanoutTree(t)
	requireInitOK(t, initFanout(tr, testOptions()))

	want, err := schema.Migration("svc", schema.Current())
	require.NoError(t, err)
	require.Equal(t, want, tr.read(migrationPath(feedFile())), "миграция ленты действующей версии — выпущенное содержимое")

	files := fanoutFiles(t, tr)
	require.Len(t, files, 1, "функция resource-event и триггер — одна отдельная миграция: %v", files)
	body := tr.read(migrationPath(files[0]))
	up := strings.ToUpper(body)
	require.Contains(t, up, "CREATE OR REPLACE FUNCTION")
	require.Contains(t, body, "notify_feed_resource_event")
	require.Contains(t, body, "AFTER INSERT ON svc_journal")
	require.Contains(t, body, "FOR EACH ROW WHEN (NEW.resource_kind <> 'notification')")
	require.Contains(t, body, "kacho_feed.enabled", "п.1: функция читает флаг модуля")
	sig, err := spec.FeedSignal("svc")
	require.NoError(t, err)
	require.Contains(t, body, sig.Object, "п.5: строка сигнала из формы notify/spec")

	before := tr.files()
	r := initFanout(tr, testOptions())
	requireInitOK(t, r)
	require.Contains(t, r.stdout, "изменений 0")
	require.Equal(t, before, tr.files(), "повторный init ничего не пишет")
	c := tr.run("-check")
	require.Equal(t, 0, c.code, "-check на свежем выводе: %s%s", c.stdout, c.stderr)
	require.Contains(t, c.stdout, "функций resource-event 1")
}

// «Версии тела», Е10 (1) NTF-1: -check извлекает из тела отпечаток версии
// шаблона и входы, выводит шаблон на них и сверяет побайтно. Инъекция одного
// факта — тело больше не читает флаг (имя настройки испорчено) — красный
// -check с координатой файла; близнец — то же дерево без правки — зелёный.
func TestNTF3_X2F_CheckRoundTripsTheBodyAndFindsAHandEdit(t *testing.T) {
	tr := fanoutTree(t)
	requireInitOK(t, initFanout(tr, testOptions()))
	files := fanoutFiles(t, tr)
	require.Len(t, files, 1)
	c := tr.run("-check")
	require.Equal(t, 0, c.code, "близнец: тело как выведено: %s%s", c.stdout, c.stderr)

	rel := migrationPath(files[0])
	tr.edit(rel, "kacho_feed.enabled", "kacho_feed.enabledx")
	c = tr.run("-check")
	require.NotEqual(t, 0, c.code, "правленое тело функции обязано краснить -check")
	require.Contains(t, c.stderr, rel, "находка называет файл миграции функции")
}

// З10 п.3, CX3J-01 (в), (г): Go-половина (атрибуты шаблона, из которых
// порождён SendResourceEvent) и SQL-половина (атрибуты, которые ставит
// функция) сверяются -check. Инъекция — шаблон получил атрибут region,
// порождённое обновлено, тело функции прежнее — красный с именем атрибута и
// файлом; близнец — шаблон без region — зелёный.
func TestNTF3_X2F_CheckFindsAnAttributeTheSQLHalfDoesNotSet(t *testing.T) {
	tr := fanoutTree(t)
	requireInitOK(t, initFanout(tr, testOptions()))
	files := fanoutFiles(t, tr)
	require.Len(t, files, 1)
	c := tr.run("-check")
	require.Equal(t, 0, c.code, "близнец: половины согласны: %s%s", c.stdout, c.stderr)

	tr.edit("svc/notifications/resource-event/notification.yaml",
		"  name: {type: text, presence: optional}",
		"  name: {type: text, presence: optional}\n  region: {type: text, presence: optional}")
	if r := tr.run(); r.code != 0 {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: make notifications с атрибутом region: %s%s", r.stdout, r.stderr)
	}
	c = tr.run("-check")
	require.NotEqual(t, 0, c.code, "атрибут Go-половины без SQL-половины обязан краснить -check")
	require.Contains(t, c.stderr, "region")
	require.Contains(t, c.stderr, migrationPath(files[0]))
}

// З10 п.5, CX3J-01 (г): строка сигнала тела — из формы notify/spec
// (spec.FeedSignal), той же, из которой пишет Go-половина. Инъекция — объект
// сигнала в теле расходится с формой — красный -check, находка называет файл
// и ожидаемый объект; близнец — тело как выведено — зелёный.
func TestNTF3_X2F_CheckFindsASignalRowDivergence(t *testing.T) {
	tr := fanoutTree(t)
	requireInitOK(t, initFanout(tr, testOptions()))
	files := fanoutFiles(t, tr)
	require.Len(t, files, 1)
	c := tr.run("-check")
	require.Equal(t, 0, c.code, "близнец: строка сигнала из формы: %s%s", c.stdout, c.stderr)

	sig, err := spec.FeedSignal("svc")
	require.NoError(t, err)
	rel := migrationPath(files[0])
	tr.edit(rel, sig.Object, sig.ObjectType+":svx")
	c = tr.run("-check")
	require.NotEqual(t, 0, c.code, "строка сигнала не по форме обязана краснить -check")
	require.Contains(t, c.stderr, rel)
	require.Contains(t, c.stderr, sig.Object, "находка называет ожидаемый объект сигнала")
}

// «Версии тела», CX3J-01 (а) (половина генератора УК3-50): действующее тело
// отстаёт от объявления журнала (добавлен вид Snapshot, новой миграции нет) —
// красный -check с координатой и именем вида. init дописывает НОВУЮ миграцию
// CREATE OR REPLACE той же функции; прежняя побайтно та же (ban #5); -check
// зелёный, функций на журнал 1 при двух телах.
func TestNTF3_X2F_DeclarationChangeIsANewCreateOrReplaceMigration(t *testing.T) {
	tr := fanoutTree(t)
	requireInitOK(t, initFanout(tr, testOptions()))
	first := fanoutFiles(t, tr)
	require.Len(t, first, 1)
	firstBody := tr.read(migrationPath(first[0]))

	fanoutDecl(tr, strings.Replace(fanoutJournal, "  notification: {name_form: none, scope: cluster}\n",
		"  notification: {name_form: none, scope: cluster}\n  Snapshot: {name_form: dns, scope: project}\n", 1))
	c := tr.run("-check")
	require.NotEqual(t, 0, c.code, "тело, отставшее от объявления, обязано краснить -check")
	require.Contains(t, c.stderr, "Snapshot")
	require.Contains(t, c.stderr, migrationPath(first[0]))

	o := testOptions()
	o.now = func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	requireInitOK(t, initFanout(tr, o))
	files := fanoutFiles(t, tr)
	require.Len(t, files, 2, "новая миграция, а не правка применённой: %v", files)
	require.Equal(t, first[0], files[0])
	require.Equal(t, firstBody, tr.read(migrationPath(files[0])), "применённое тело не правится")
	second := tr.read(migrationPath(files[1]))
	require.Contains(t, strings.ToUpper(second), "CREATE OR REPLACE FUNCTION")
	require.Contains(t, second, "notify_feed_resource_event")
	require.Contains(t, second, "Snapshot")
	require.NotContains(t, strings.ToUpper(second), "CREATE TRIGGER", "триггер связан с объектом функции и не пересоздаётся")

	c = tr.run("-check")
	require.Equal(t, 0, c.code, "два тела, действующее — по объявлению: %s%s", c.stdout, c.stderr)
	require.Contains(t, c.stdout, "функций resource-event 1")
}

// Без объявления журнала init ведёт себя как в NTF-1 (миграция ленты и
// только она) — положительный контроль того, что форма fanout включается
// входом, а не появляется у каждого источника.
func TestNTF3_X2F_InitWithoutJournalWritesOnlyTheFeedMigration(t *testing.T) {
	tr := newTree(t)
	tr.generate()
	r := tr.run("init", "-service", "svc", "-migrations", "svc/migrations")
	require.Equal(t, 0, r.code, r.stderr)
	require.Equal(t, []string{feedFile()}, migrations(t, tr))
	_, err := os.Stat(tr.path(fanoutJournalPath))
	require.True(t, os.IsNotExist(err))
}
