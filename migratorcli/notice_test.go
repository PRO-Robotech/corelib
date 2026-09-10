// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notice_test.go — пробы доставки уведомлений сервера.
//
// Живой базы здесь нет по той же причине, что и у соседнего файла: пробу,
// гейтящуюся кратким режимом, в этом пакете некому гонять (отбор интеграционной
// джобы до `pkg/migratorcli` не достаёт). Поэтому здесь закреплено то, что
// проверяется БЕЗ базы, — форма доставки, перепись и предел, — а то, что база
// нужна по существу (уведомление НАСТОЯЩЕЙ миграции доезжает до вывода
// процесса), доказывает `internal/migratorapply`, который гоняет цель
// test-pg-outside-selection.
//
// Отрицания идут В ПАРЕ с положительными: без них они зеленели бы на приёмнике,
// который не печатает ничего и никогда.
package migratorcli_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/corelib/migratorcli"
)

// TestNoticeReachesTheOperatorWithItsLevelAndText — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ко
// всему файлу: приёмник вообще что-то печатает, и печатает названное сервером.
func TestNoticeReachesTheOperatorWithItsLevelAndText(t *testing.T) {
	var buf bytes.Buffer
	relay := migratorcli.NewNoticeRelay("vpc", &buf)

	relay.Handler()(nil, &pgconn.Notice{
		Severity:            "WARNING",
		SeverityUnlocalized: "WARNING",
		Message:             "quota usage backfill: 3 accounting row(s) corrected",
		Detail:              "kacho_vpc.project_resource_quotas",
		Hint:                "see the census below",
	})

	out := buf.String()
	for _, want := range []string{
		"WARNING: quota usage backfill: 3 accounting row(s) corrected",
		"  DETAIL: kacho_vpc.project_resource_quotas",
		"  HINT: see the census below",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("вывод не несёт %q:\n%s", want, out)
		}
	}
	if relay.Delivered() != 1 || relay.Suppressed() != 0 {
		t.Errorf("счёт разошёлся с напечатанным: доставлено %d, отброшено %d",
			relay.Delivered(), relay.Suppressed())
	}
}

// TestLevelIsTakenUnlocalised — уровень берётся нелокализованный.
//
// Сервер переводит `Severity` по `lc_messages`. Печатай мы переведённое — отбор
// по слову `WARNING` перестал бы находить предупреждения на нерусской… вернее, на
// любой нерусскоязычной посадке МОЛЧА: строки в логе есть, а грепу их не видно.
func TestLevelIsTakenUnlocalised(t *testing.T) {
	var buf bytes.Buffer
	relay := migratorcli.NewNoticeRelay("iam", &buf)

	relay.Handler()(nil, &pgconn.Notice{
		Severity:            "ПРЕДУПРЕЖДЕНИЕ",
		SeverityUnlocalized: "WARNING",
		Message:             "ceilings leaving the posture",
	})

	out := buf.String()
	if !strings.HasPrefix(out, "WARNING: ") {
		t.Errorf("напечатан локализованный уровень — отбор по WARNING его не найдёт:\n%s", out)
	}
	// Пара к отрицанию: локализованное значение обязано УМЕТЬ появиться, когда
	// нелокализованного сервер не прислал. Иначе проба выше зеленела бы и на
	// приёмнике, который уровень вообще не читает.
	buf.Reset()
	relay.Handler()(nil, &pgconn.Notice{Severity: "ЗАМЕЧАНИЕ", Message: "no unlocalised severity"})
	if !strings.HasPrefix(buf.String(), "ЗАМЕЧАНИЕ: ") {
		t.Errorf("единственный присланный уровень потерян:\n%s", buf.String())
	}
}

// TestCensusIsPrintedEvenWhenNothingWasSaid — несущее свойство.
//
// Клиент не знает, сколько сервер ПОДНЯЛ, — только сколько ему доставили.
// Поэтому «ноль доставленных» само по себе не отличает молчаливую цепочку от
// отсутствующего обработчика, и различает их только сама строка переписи.
func TestCensusIsPrintedEvenWhenNothingWasSaid(t *testing.T) {
	var buf bytes.Buffer
	migratorcli.NewNoticeRelay("geo", &buf).WriteCensus()

	out := buf.String()
	for _, want := range []string{"migration-notices geo:", "0 delivered", "0 suppressed", "the handler was installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("перепись молчаливого наката не несёт %q:\n%s", want, out)
		}
	}
	// Второе условие нуля названо ТУТ ЖЕ. Сообщение, не прошедшее порог сессии,
	// сервер не отправляет вовсе — приёмнику оно неотличимо от несказанного,
	// поэтому строка, объявляющая цепочку молчавшей, утверждала бы факт, которого
	// у клиента нет (#2560).
	if !strings.Contains(out, "threshold") {
		t.Errorf("перепись не называет порог сессии вторым условием нуля — «0 delivered» "+
			"читается как «цепочка молчала», хотя сказанное могло быть погашено сервером:\n%s", out)
	}
	if strings.Contains(out, "raised nothing") {
		t.Errorf("перепись утверждает, что цепочка ничего не подняла: такого факта у клиента "+
			"нет by construction:\n%s", out)
	}
}

// TestCensusCountsWhatItPrintedAndWhatItDropped — предел объявлен и ОТБРОШЕННОЕ
// СЧИТАЕТСЯ. Подавление, о котором сказано, — не то же, что подавление молчком.
func TestCensusCountsWhatItPrintedAndWhatItDropped(t *testing.T) {
	var buf bytes.Buffer
	relay := migratorcli.NewNoticeRelay("nlb", &buf)

	const over = 7
	for i := 0; i < migratorcli.NoticeLimit+over; i++ {
		relay.Handler()(nil, &pgconn.Notice{SeverityUnlocalized: "NOTICE", Message: "row"})
	}

	if got := relay.Delivered(); got != migratorcli.NoticeLimit {
		t.Errorf("доставлено %d при пределе %d", got, migratorcli.NoticeLimit)
	}
	if got := relay.Suppressed(); got != over {
		t.Errorf("отброшено %d, а сверх предела подано %d — счёт не сходится", got, over)
	}

	out := buf.String()
	if strings.Count(out, "limit") == 0 {
		t.Errorf("оператору не сказано, что предел достигнут:\n%s", out[:min(len(out), 400)])
	}
	// Сказано РОВНО ОДИН раз: строка на каждое отброшенное уведомление вернула бы
	// ровно тот поток, ради которого предел заведён.
	if got := strings.Count(out, "further notices are counted, not printed"); got != 1 {
		t.Errorf("о достижении предела сказано %d раз(а), ожидался один", got)
	}

	buf.Reset()
	relay.WriteCensus()
	for _, want := range []string{"500 delivered", "7 suppressed"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("перепись не несёт %q:\n%s", want, buf.String())
		}
	}
}

// TestOpenDBRefusesToRunWithoutAPlaceToDeliver — отказ вместо тихой потери.
//
// Приёмник — обязательный параметр именно затем, чтобы «доставка в никуда» была
// невыразима. `nil` остаётся единственным способом её выразить, и он отвергается.
func TestOpenDBRefusesToRunWithoutAPlaceToDeliver(t *testing.T) {
	_, err := migratorcli.OpenDB(t.Context(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
		migratorcli.SpecPostgres, nil)
	if err == nil {
		t.Fatal("накат без приёмника уведомлений принят — сообщения сервера терялись бы молча")
	}
	if !strings.Contains(err.Error(), "no notice relay given") {
		t.Errorf("отказ не называет причины: %v", err)
	}
}

// TestOpenDBRefusesADriverThatCannotDeliver — fail-closed на чужом драйвере.
//
// Доставка живёт на уровне драйвера. Второй драйвер, принятый молча, применил бы
// цепочку и выбросил каждое сказанное сервером слово — тот же дефект, только уже
// объяснённый в коде.
func TestOpenDBRefusesADriverThatCannotDeliver(t *testing.T) {
	_, err := migratorcli.OpenDB(t.Context(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
		migratorcli.DialectSpec{Name: "postgres", GooseDialect: "postgres", SQLDriver: "othersql"},
		migratorcli.NewNoticeRelay("svc", io.Discard))
	if err == nil {
		t.Fatal("чужой драйвер принят — уведомления терялись бы, а вердикт был бы зелёным")
	}
	for _, want := range []string{"open db (driver=othersql)", "notices are delivered through"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("отказ не несёт %q: %v", want, err)
		}
	}
}
