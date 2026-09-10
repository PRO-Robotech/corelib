// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// refusalbody_test.go — доказательство извлекателя тел в ОБЕ стороны.
//
// # Что здесь опасно и потому доказывается
//
// Извлекатель один на обе стороны сравнения — это его достоинство (разбор не может
// разойтись сам с собой) и его же риск: верни он пустоту на ОБЕИХ сторонах, «тела
// совпали» стало бы верным при любом дереве. Поэтому доказывается не только то, что
// он извлекает, но и то, что вырожденный эталон ОТВЕРГАЕТСЯ, а не принимается.
package quota_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/quota"
)

func TestRefusalFunctionBodies_ProvenByInjection(t *testing.T) {
	t.Run("объявление узнаётся, тело извлекается целиком", func(t *testing.T) {
		got := quota.RefusalFunctionBodies(`
CREATE OR REPLACE FUNCTION kacho_demo.kacho_quota_refuse(a text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'nope' USING ERRCODE = 'KQ001'; END;
$$;
`)
		if len(got) != 1 {
			t.Fatalf("извлечено %d тел, ожидалось 1: %v", len(got), got)
		}
		if !strings.Contains(got["kacho_quota_refuse"], "KQ001") {
			t.Fatalf("тело извлечено не целиком: %q", got["kacho_quota_refuse"])
		}
	})

	t.Run("форма свода (pg_dump) узнаётся наравне с формой миграции", func(t *testing.T) {
		// `pg_dump` печатает без OR REPLACE, разворачивает список параметров в
		// одну строку и переносит LANGUAGE. Форма другая, предмет тот же —
		// распознаватель, знающий лишь одну из них, молчал бы на сведённой схеме.
		got := quota.RefusalFunctionBodies(
			"CREATE FUNCTION kaname.kacho_quota_admit(v_carrier_type text, v_kind text) RETURNS void\n" +
				"    LANGUAGE plpgsql\n    AS $$\nBEGIN RETURN; END;\n$$;\n")
		if _, ok := got["kacho_quota_admit"]; !ok {
			t.Fatalf("форма свода не узнана — записанное в ней прошло бы ВНЕ наблюдения: %v", got)
		}
	})

	t.Run("ВЫЗОВ функции — не её объявление", func(t *testing.T) {
		// Ловушка настоящая: в схеме iam триггерная функция учёта зовёт отказ
		// четырьмя `PERFORM`. Предикат по одному имени принял бы вызов за
		// определение и сверял бы тело чужой функции.
		got := quota.RefusalFunctionBodies(`
CREATE FUNCTION kaname.kacho_quota_count() RETURNS trigger AS $$
BEGIN PERFORM kaname.kacho_quota_refuse('identity', v_identity, v_kind); END;
$$;
`)
		if len(got) != 0 {
			t.Fatalf("вызов принят за объявление: %v", got)
		}
	})

	t.Run("последнее определение в тексте побеждает раннее", func(t *testing.T) {
		got := quota.RefusalFunctionBodies(`
CREATE FUNCTION x.kacho_quota_refuse(a text) RETURNS void AS $$ РАННЕЕ $$;
CREATE OR REPLACE FUNCTION x.kacho_quota_refuse(a text) RETURNS void AS $$ ПОЗДНЕЕ $$;
`)
		if !strings.Contains(got["kacho_quota_refuse"], "ПОЗДНЕЕ") {
			t.Fatalf("побеждает не последнее определение, а %q — в базе окажется другое тело",
				got["kacho_quota_refuse"])
		}
	})

	t.Run("текст без объявлений — пусто, а не выдумка", func(t *testing.T) {
		if got := quota.RefusalFunctionBodies("-- проза про kacho_quota_refuse и ничего больше\n"); len(got) != 0 {
			t.Fatalf("упоминание в комментарии принято за объявление: %v", got)
		}
	})
}

func TestExpectedRefusalBodies_RefusesAVacuousReference(t *testing.T) {
	// ── ЗАКОННЫЙ СЛУЧАЙ: настоящий владелец даёт полный набор ──────────────
	for _, o := range quota.RefusalOwners() {
		bodies, err := quota.ExpectedRefusalBodies(o)
		if err != nil {
			t.Fatalf("%s: эталон не извлёкся: %v", o.Service, err)
		}
		if len(bodies) != len(quota.RefusalFunctionNames()) {
			t.Fatalf("%s: тел %d, имён %d — сравнение шло бы по неполному набору",
				o.Service, len(bodies), len(quota.RefusalFunctionNames()))
		}
	}

	// ── ИНЪЕКЦИЯ: владелец без схемы. Рендер отказывает, и эталон обязан
	//    отказать вслед за ним, а не вернуть пустое множество, которое сойдётся
	//    с любым деревом.
	if _, err := quota.ExpectedRefusalBodies(quota.RefusalOwner{Service: "demo"}); err == nil {
		t.Error("эталон построен для владельца без схемы: пустое множество тел " +
			"сошлось бы с любым деревом, и «тела совпали» перестало бы что-либо означать")
	}
}
