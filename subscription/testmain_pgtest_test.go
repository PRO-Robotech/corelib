// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain выдаёт пакету ОДИН Postgres вместо одного на пробу.
//
// Схема журнала здесь фиксированная и применяется один раз в образец, а каждая
// проба получает свой клон: и уведомление на канале, и наблюдение блокировок
// журнала — свойства ОДНОЙ базы, поэтому пробы не видят чужих строк и чужих
// писателей.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "subscription",
		Migrate: pgtest.SQL(journalSchema),
	}))
}

// journalChannel / journalSchema — журнал формы vpc/compute/geo: без проектной
// колонки, с триггером пробуждения. Именно эта форма у трёх владельцев из
// четырёх, поэтому проба стоит на ней, а не на самой удобной.
const journalChannel = "subscription_probe_outbox"

const journalSchema = `
CREATE TABLE probe_outbox (
    sequence_no   bigserial    PRIMARY KEY,
    resource_kind text         NOT NULL,
    resource_id   text         NOT NULL,
    event_type    text         NOT NULL,
    payload       jsonb        NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz  NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION probe_outbox_notify() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
    PERFORM pg_notify('` + journalChannel + `', '');
    RETURN NEW;
END;
$fn$;

CREATE TRIGGER probe_outbox_notify_trigger
AFTER INSERT ON probe_outbox
FOR EACH ROW EXECUTE FUNCTION probe_outbox_notify();
`
