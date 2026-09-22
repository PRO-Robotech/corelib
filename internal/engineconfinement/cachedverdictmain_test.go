// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// cachedverdictmain_test.go — проба этого пакета отказывается работать на
// прогоне, результат которого `go test` положит в кеш: состав дерева она берёт
// из индекса git подпроцессом, невидимым инструменту, и над деревом с новым
// нарушителем печаталось бы `ok (cached)`. Предмет и замеры —
// treecorpus/cachedverdict.go.
package engineconfinement_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/treecorpus"
)

func TestMain(m *testing.M) {
	if msg := treecorpus.CachedVerdictRefusal(); msg != "" {
		fmt.Fprintln(os.Stderr, "engineconfinement: "+msg)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
