// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// Пробы сверки формы fanout сверх RED полосы X2-F: объявление без функции и
// функция без объявления — находки с координатой; объявление журнала вне
// формы — отказ init с именем файла.

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Объявление журнала есть, функции нет — красный -check с координатой
// объявления; близнец — после init зелёный.
func TestFanoutCheckFindsADeclarationWithoutTheFunction(t *testing.T) {
	tr := fanoutTree(t)
	c := tr.run("-check")
	require.NotEqual(t, 0, c.code)
	require.Contains(t, c.stderr, fanoutJournalPath+": функции resource-event на журнале svc_journal нет")

	requireInitOK(t, initFanout(tr, testOptions()))
	c = tr.run("-check")
	require.Equal(t, 0, c.code, c.stderr)
}

// Функция есть, объявления нет — красный -check с координатой миграции;
// близнец — объявление на месте.
func TestFanoutCheckFindsAFunctionWithoutTheDeclaration(t *testing.T) {
	tr := fanoutTree(t)
	requireInitOK(t, initFanout(tr, testOptions()))
	files := fanoutFiles(t, tr)
	require.Len(t, files, 1)
	require.NoError(t, os.Remove(tr.path(fanoutJournalPath)))
	c := tr.run("-check")
	require.NotEqual(t, 0, c.code)
	require.Contains(t, c.stderr, migrationPath(files[0])+": функция resource-event на журнале svc_journal без объявления")
}

// Объявление вне формы — отказ init, файла функции нет: неизвестный ключ,
// неполный набор колонок, род вне набора, нет слова строки сигнала, ключ
// строки сигнала не объявлен, -journal не journal.yaml. Близнец — fanoutJournal.
func TestFanoutInitRefusesADeclarationOutsideItsForm(t *testing.T) {
	cases := map[string]string{
		"неизвестный ключ":     fanoutJournal + "extra: 1\n",
		"колонок не хватает":   strings.Replace(fanoutJournal, "  initiator: initiator\n", "", 1),
		"род вне набора":       strings.Replace(fanoutJournal, "deleted: DELETED", "deleted: REMOVED", 1),
		"нет рода UPDATED":     strings.Replace(fanoutJournal, "updated: UPDATED", "updated: CREATED", 1),
		"нет ключа сигнала":    strings.Replace(fanoutJournal, "  notification: {name_form: none, scope: cluster}\n", "", 1),
		"кавычка в имени вида": strings.Replace(fanoutJournal, "Volume:", "\"Vol'ume\":", 1),
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			tr := fanoutTree(t)
			fanoutDecl(tr, decl)
			r := initFanout(tr, testOptions())
			require.NotEqual(t, 0, r.code, "объявление вне формы принято")
			require.Empty(t, migrations(t, tr), "отказ init оставил миграции")
		})
	}
	tr := fanoutTree(t)
	tr.write("svc/other.yaml", fanoutJournal)
	r := runWith(tr, testOptions(), "init", "-service", "svc", "-migrations", "svc/migrations", "-journal", "svc/other.yaml")
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.stderr, "journal.yaml")
}
