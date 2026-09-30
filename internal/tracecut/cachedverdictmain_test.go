// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// cachedverdictmain_test.go — пробы этого пакета отказываются работать на
// прогоне, результат которого `go test` положит в кеш.
//
// Предмет и замеры — treecorpus/cachedverdict.go; здесь они не пересказываются.
// Причина, по которой страж нужен ИМЕННО ЗДЕСЬ: coverage_test.go берёт состав
// поддерева через treecorpus, то есть ПОДПРОЦЕССОМ, инструменту невидимым.
// Правка в internal/oauth2 кеш этого пакета не инвалидирует, и над деревом, где
// у спана пропал закрывающий вызов, печаталось бы `ok (cached)`.
//
// Цена названа, а не спрятана: под стражем оказываются и поведенческие пробы,
// дерева не читающие, — их вердикт кешировался бы законно, и платой идёт их
// повторное исполнение (замер: `go test ./internal/tracecut/ -count=1` —
// см. вывод прогона). Разделить пробы внутри одного пакета нечем: TestMain у
// тестового двоичного файла один.
package tracecut_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/treecorpus"
)

func TestMain(m *testing.M) {
	if msg := treecorpus.CachedVerdictRefusal(); msg != "" {
		fmt.Fprintln(os.Stderr, "tracecut: "+msg)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
