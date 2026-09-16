// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// brandedknob_injection_test.go — доказательство способности гейта упасть
// И СМОЛЧАТЬ.
//
// Инъекция вносит ОДИН факт против законного близнеца: то же имя, тот же файл,
// различие ровно в том, объявлено ли оно окном перехода. Без второй половины
// гейт ловил бы приставку, а не решение, и первый же законный остаток его
// отключил бы.
package envknob_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/envknob"
)

// injBefore — форма ДО перехода, дословно та, что стояла в дереве: имя с
// приставкой платформы объявлено обычной константой.
const injBefore = `package migratorcli

// EnvDSN — переменная окружения второго приоритета.
const EnvDSN = "KACHO_MIGRATOR_DSN"
`

// injAfter — форма ПОСЛЕ перехода: нейтральное имя плюс окно.
const injAfter = `package migratorcli

// EnvDSN — переменная окружения второго приоритета.
const EnvDSN = "MIGRATOR_DSN"

// LegacyEnvDSN — прежнее написание, принимаемое окном перехода.
const LegacyEnvDSN = "KACHO_MIGRATOR_DSN"
`

// injProse — законный близнец второго рода: приставка названа ПРОЗОЙ, в godoc,
// объясняющем переход. Комментарий литералом не является.
const injProse = `package migratorcli

// Прежде эта ручка называлась KACHO_MIGRATOR_DSN; переход объявлен окном.
func nothing() {}
`

func TestBrandedKnobInjection_ABrandedNameOutsideTheWindowIsFound(t *testing.T) {
	t.Parallel()
	found, literals, err := envknob.ScanBrandedKnobs("migratorcli/parse.go", []byte(injBefore))
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}
	if literals == 0 {
		t.Fatal("осмотрено ноль литералов — разбор не состоялся, и молчание было бы " +
			"о непрочитанном")
	}
	bad := envknob.UnwindowedBrandedKnobs(found)
	if len(bad) != 1 {
		t.Fatalf("находок %d, ожидалась 1 — гейт не способен упасть на форме, которая "+
			"стояла в дереве", len(bad))
	}
	if bad[0].Name != "KACHO_MIGRATOR_DSN" {
		t.Fatalf("находка называет %q", bad[0].Name)
	}
	if bad[0].Const != "EnvDSN" {
		t.Fatalf("находка не называет константу-владельца: %q", bad[0].Const)
	}
	if bad[0].Line != 4 {
		t.Fatalf("находка называет строку %d, дефект в строке 4 — координата ведёт не туда",
			bad[0].Line)
	}
}

func TestBrandedKnobInjection_TheWindowIsSilent(t *testing.T) {
	t.Parallel()
	found, literals, err := envknob.ScanBrandedKnobs("migratorcli/parse.go", []byte(injAfter))
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}
	if literals == 0 {
		t.Fatal("осмотрено ноль литералов")
	}
	if len(found) != 1 {
		t.Fatalf("имён с приставкой найдено %d, ожидалось 1 — распознаватель перестал "+
			"видеть предмет окна", len(found))
	}
	if !found[0].InWindow {
		t.Fatalf("окно перехода объявлено находкой: константа %q", found[0].Const)
	}
	if len(envknob.UnwindowedBrandedKnobs(found)) != 0 {
		t.Fatal("объявленное окном попало в находки — гейт ловит приставку, а не решение")
	}
}

func TestBrandedKnobInjection_ProseIsNotAFinding(t *testing.T) {
	t.Parallel()
	found, _, err := envknob.ScanBrandedKnobs("migratorcli/doc.go", []byte(injProse))
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("проза объявлена находкой (%d) — гейт краснел бы на собственном "+
			"объяснении перехода", len(found))
	}
}

// TestBrandedKnobInjection_ConcatenationIsNotSeen — ГРАНИЦА, названная вслух и
// проверенная: имя, собранное конкатенацией, разбор не видит. В дереве таких
// нет, и проба это закрепляет — появится такая форма, и молчание гейта
// перестанет означать чистоту.
func TestBrandedKnobInjection_ConcatenationIsNotSeen(t *testing.T) {
	t.Parallel()
	src := `package x

const suffix = "MIGRATOR_DSN"

var name = "KACHO_" + suffix
`
	found, _, err := envknob.ScanBrandedKnobs("x/x.go", []byte(src))
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}
	// Приставка стоит литералом сама по себе — она и находится. Полное имя
	// разбор не собирает, и это его названная граница, а не дефект.
	if len(found) != 1 || found[0].Name != "KACHO_" {
		t.Fatalf("граница разбора изменилась: %v — перечитайте её объявление в шапке", found)
	}
	if !strings.HasPrefix(found[0].Name, envknob.BrandedPrefix) {
		t.Fatal("приставка не опознана")
	}
}
