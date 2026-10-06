// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene

import (
	"slices"
	"sort"
	"testing"
)

// C5 (замысел issue-2924 З28 п.1, п.2, п.6; CX5-61): ведомость писателей
// таблиц ленты меняется тем же коммитом, что писатели. Ядро взятия и записи
// исхода — claimTx и recordOutcome, и только в них SQL взятия и записи исхода:
// места Claim и Ack сервера NTF-1 переезжают в их строки; добавляются
// Supersede и DeleteUnleased; место закрытия после расшифровки уходит —
// закрытие идёт вызовом recordOutcome. Каждое из новых мест называет вид
// Outbox.
//
// Положительный контроль — строки, которые правка не трогает (Put, уборщик
// истечения, уборка закрытых строк), в ведомости есть: отказ контроля —
// сломанная ведомость, а не отсутствие строк C5.
func TestC5_FeedLedgerCarriesTheCoreWriters(t *testing.T) {
	owners := corelibFeedLedger().owners
	f := func(name string) string { return feedPkg + "." + name }
	keys := make([]string, 0, len(owners))
	for k := range owners {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("строк ведомости %d: %v", len(keys), keys)

	for _, kept := range []string{"(*" + feedPkg + ".Source).write", f("expireSQL"), f("RetentionSubjects")} {
		if _, ok := owners[kept]; !ok {
			t.Fatalf("ФИКСТУРА: в ведомости нет строки %s, которую C5 не трогает", kept)
		}
	}
	for _, want := range []string{f("claimTx"), f("recordOutcome"), f("Supersede"), f("DeleteUnleased")} {
		o, ok := owners[want]
		if !ok {
			t.Errorf("в ведомости нет писателя %s", want)
			continue
		}
		if !slices.Contains(o.sites, "Outbox") {
			t.Errorf("место %s не называет вид Outbox: %v", want, o.sites)
		}
	}
	for _, gone := range []string{f("claimSQL"), f("sealedCloseSQL"), f("ackTerminalSQL"), f("ackDeferSQL"), f("ackRecordedSQL")} {
		if _, ok := owners[gone]; ok {
			t.Errorf("в ведомости осталась строка %s: SQL взятия и записи исхода — только в claimTx и recordOutcome", gone)
		}
	}
}
