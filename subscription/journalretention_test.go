// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// journalretention_test.go — объявление удержания и его ПАРА: колонка срока.
//
// Предмет — задача продукта #1666. Пять журналов подписки объявляли
// [subscription.RetainsEverything] и не имели снятия строк ни на одном пути;
// величина роста задавалась внешним. Здесь утверждается ПАРНОСТЬ объявления:
// «журнал чистится» и «по какой колонке судить возраст» — одно решение, и
// половина его не выражается.
package subscription_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/subscription"
)

// TestSweepingOwnerNamesTheAgeColumn — объявивший чистку обязан назвать колонку
// срока.
//
// Без неё предикат уборки не выражается вовсе, а объявление остаётся половиной
// решения: сервер говорит подписчику «удерживаю не всё», и нижняя возобновимая
// позиция у него есть, а механизма, который её двигает, нет ни одного.
func TestSweepingOwnerNamesTheAgeColumn(t *testing.T) {
	j := probeJournal()
	j.Storage.Retention = subscription.RetainsFromEarliestRow
	j.Storage.AgeColumn = ""

	err := j.Validate()
	if err == nil {
		t.Fatal("объявление чистки без колонки срока принято — предикат уборки не из чего построить")
	}
	if !strings.Contains(err.Error(), "AgeColumn") {
		t.Fatalf("отказ не называет поле: %v", err)
	}
}

// TestRetainingOwnerMustNotNameAnAgeColumn — обратная сторона той же пары.
//
// Колонка срока у владельца, который не чистит, — объявление БЕЗ ЧИТАТЕЛЯ:
// уборки нет, значит поле не читает никто, и следующий примет его за
// действующий механизм (класс «принято-и-проигнорировано»,
// `api-conventions.md`).
func TestRetainingOwnerMustNotNameAnAgeColumn(t *testing.T) {
	j := probeJournal()
	j.Storage.Retention = subscription.RetainsEverything
	j.Storage.AgeColumn = "created_at"

	err := j.Validate()
	if err == nil {
		t.Fatal("колонка срока принята у владельца, который не чистит, — объявление без читателя")
	}
	if !strings.Contains(err.Error(), "AgeColumn") {
		t.Fatalf("отказ не называет поле: %v", err)
	}
}

// TestSweepingOwnerWithAnAgeColumnIsAccepted — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к обеим
// пробам выше.
//
// Без него отрицания зеленели бы на объявлении, которое отвергает всё.
func TestSweepingOwnerWithAnAgeColumnIsAccepted(t *testing.T) {
	j := probeJournal()
	j.Storage.Retention = subscription.RetainsFromEarliestRow
	j.Storage.AgeColumn = "created_at"

	if err := j.Validate(); err != nil {
		t.Fatalf("законное объявление чистки отвергнуто: %v", err)
	}

	j.Storage.Retention = subscription.RetainsEverything
	j.Storage.AgeColumn = ""
	if err := j.Validate(); err != nil {
		t.Fatalf("законное объявление удержания отвергнуто: %v", err)
	}
}

// TestAgeColumnIsCheckedAsAnIdentifier — имя колонки уезжает в текст оператора,
// поэтому оно обязано проверяться тем же предикатом, что и остальные пять.
func TestAgeColumnIsCheckedAsAnIdentifier(t *testing.T) {
	j := probeJournal()
	j.Storage.Retention = subscription.RetainsFromEarliestRow
	j.Storage.AgeColumn = "created_at; DROP TABLE probe_outbox"

	if err := j.Validate(); err == nil {
		t.Fatal("негодное имя колонки срока принято — оно уезжает в текст оператора")
	}
}

// TestJournalRetentionWindowIsPositiveAndNamed — окно удержания есть ВЕЛИЧИНА
// платформы, а не число у каждого владельца.
//
// Проба утверждает не размер, а два свойства, при нарушении которых механизм
// перестаёт работать молча: окно положительно (нулевое снимало бы строку в тот
// же миг, в который её написали) и оно ОДНО — то есть берётся отсюда, а не
// выписывается владельцем.
func TestJournalRetentionWindowIsPositiveAndNamed(t *testing.T) {
	if subscription.JournalRetention <= 0 {
		t.Fatalf("окно удержания журнала %v — уборка снимала бы строку раньше, чем её прочитал хоть один подписчик",
			subscription.JournalRetention)
	}
}
