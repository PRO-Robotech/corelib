// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package migratorcli_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain выдаёт пакету ОДИН Postgres на все его пробы, требующие базы.
//
// Контейнер поднимается ЛЕНИВО: прочие пробы этого пакета базы не просят и за
// него не платят, а под кратким режимом не платит никто.
//
// Шаблон намеренно пуст (`Migrate` не задан): предмет доказательства —
// поведение соединения ВО ВРЕМЯ наката, поэтому цепочку каждая проба
// накатывает сама в пустую базу.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{Name: "migratorcli"}))
}
