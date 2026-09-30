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
		Migrate: pgtest.SQL(journalSchema, attributedJournalSchema),
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

// attributedChannel / attributedJournalSchema — журнал формы З2 замысла NTF-3:
// проектная колонка, колонка инициатора с умолчанием из настройки транзакции
// помощника и `NOT NULL`, колонка времени строки. Отдельная таблица, а не
// правка `probe_outbox`: прочие пробы пакета стоят на форме журналов до
// миграций NTF-3, и она обязана оставаться проверяемой.
const attributedChannel = "subscription_attributed_outbox"

const attributedJournalSchema = `
CREATE TABLE attributed_outbox (
    sequence_no   bigserial    PRIMARY KEY,
    resource_kind text         NOT NULL,
    resource_id   text         NOT NULL,
    project_id    text         NOT NULL DEFAULT '',
    event_type    text         NOT NULL,
    payload       jsonb        NOT NULL DEFAULT '{}'::jsonb,
    initiator     text         NOT NULL DEFAULT NULLIF(current_setting('kacho_journal.initiator', true), ''),
    created_at    timestamptz  NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION attributed_outbox_notify() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
    PERFORM pg_notify('` + attributedChannel + `', '');
    RETURN NEW;
END;
$fn$;

CREATE TRIGGER attributed_outbox_notify_trigger
AFTER INSERT ON attributed_outbox
FOR EACH ROW EXECUTE FUNCTION attributed_outbox_notify();
`
