// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// cachedverdictmain_test.go — пробы этого пакета отказываются работать на
// прогоне, результат которого `go test` положит в кеш.
//
// Предмет и замеры — treecorpus/cachedverdict.go; здесь они не пересказываются.
// Причина, по которой страж нужен ИМЕННО ЗДЕСЬ: половина проб пакета зовёт
// AuditDeferredWork по синтетическому репозиторию, то есть берёт состав дерева
// ПОДПРОЦЕССОМ, невидимым инструменту. Над красным деревом печаталось бы
// `ok (cached)`, отличимое от настоящего прохода одним словом.
//
// Цена названа, а не спрятана: под стражем оказываются и пробы разбора имён,
// дерева не читающие, — их вердикт кешировался бы законно. Плата — их повторное
// исполнение. Разделить пробы внутри одного пакета нечем: TestMain у тестового
// двоичного файла один.
package treehygiene_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/treecorpus"
)

func TestMain(m *testing.M) {
	if msg := treecorpus.CachedVerdictRefusal(); msg != "" {
		fmt.Fprintln(os.Stderr, "treehygiene: "+msg)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
