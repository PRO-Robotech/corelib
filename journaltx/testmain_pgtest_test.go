// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package journaltx_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain выдаёт пакету один Postgres; каждая проба получает свой клон.
//
// Схема — журнал формы З2 замысла NTF-3: колонка инициатора без значения у
// вставки, с умолчанием из настройки транзакции и `NOT NULL`. Выражение
// умолчания здесь то же, что поставит миграция модуля, — иначе проба судила бы
// помощника на входе, которого в бою не бывает.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "journaltx",
		Migrate: pgtest.SQL(probeSchema),
	}))
}

const probeSchema = `
CREATE TABLE probe_journal (
    sequence_no bigserial   PRIMARY KEY,
    resource_id text        NOT NULL,
    initiator   text        NOT NULL DEFAULT NULLIF(current_setting('kacho_journal.initiator', true), ''),
    created_at  timestamptz NOT NULL DEFAULT now()
);
`
