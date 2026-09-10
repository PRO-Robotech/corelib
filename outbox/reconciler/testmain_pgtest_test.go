// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// Обе формы очереди, которые разбирает пакет, кладутся в один шаблон: они живут в
// разных схемах Postgres, и каждый хелпер адресует свою по полному имени, поэтому
// пустая таблица соседа ничего не стоит и экономит второй шаблон. Пробы возврата
// отравленных строк спорят на настоящих строках СОБСТВЕННОЙ базы вызывающего —
// клон ею и остаётся, — а NOTIFY триггера kacho_svc доставляется только внутри неё.
//
// Схема kacho_apps ушла отсюда вместе с пробами примитивов сверки (#760): она
// обслуживала только их.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "reconciler",
		Migrate: pgtest.SQL(registerOutboxSchema, tupleOutboxSchema),
	}))
}
