// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package envknob_test

import (
	"testing"

	"github.com/PRO-Robotech/corelib/envknob"
)

const (
	neutral = "ENVKNOB_PROBE_SUBJECT"
	legacy  = "KACHO_ENVKNOB_PROBE_SUBJECT"
)

// TestNeutralNameIsRead — нейтральное имя читается.
func TestNeutralNameIsRead(t *testing.T) {
	t.Setenv(neutral, "new")
	v, name, ok := envknob.Lookup(neutral, legacy)
	if !ok || v != "new" || name != neutral {
		t.Fatalf("нейтральное имя не прочитано: значение %q, имя %q, найдено %v", v, name, ok)
	}
}

// TestLegacyNameStillWorks — ОКНО: прежнее написание продолжает работать, иначе
// переход был бы сломом у каждого потребителя в день подъёма пина.
func TestLegacyNameStillWorks(t *testing.T) {
	t.Setenv(legacy, "old")
	v, name, ok := envknob.Lookup(neutral, legacy)
	if !ok || v != "old" || name != legacy {
		t.Fatalf("прежнее имя перестало читаться — переход стал сломом: "+
			"значение %q, имя %q, найдено %v", v, name, ok)
	}
	if !envknob.UsesLegacyName(neutral, legacy) {
		t.Fatal("прежнее написание не названо прежним — окно нечем закрыть")
	}
}

// TestNeutralWinsOverLegacy — оба заданы: сильнее НЕЙТРАЛЬНОЕ. Иначе оператор,
// перешедший на новое имя, не смог бы переопределить унаследованное окружение.
func TestNeutralWinsOverLegacy(t *testing.T) {
	t.Setenv(legacy, "old")
	t.Setenv(neutral, "new")
	v, name, _ := envknob.Lookup(neutral, legacy)
	if v != "new" || name != neutral {
		t.Fatalf("прежнее имя перебило нейтральное: значение %q, имя %q", v, name)
	}
	if envknob.UsesLegacyName(neutral, legacy) {
		t.Fatal("выбор назван прежним написанием, хотя выиграло нейтральное")
	}
}

// TestUnsetIsNotFound — ни одно не задано.
func TestUnsetIsNotFound(t *testing.T) {
	if v, name, ok := envknob.Lookup(neutral, legacy); ok || v != "" || name != "" {
		t.Fatalf("незаданная ручка объявлена найденной: значение %q, имя %q", v, name)
	}
}

// TestEmptyValueIsNotAValue — заданная пустой ручка значением не является: у всех
// семи ручек фундамента пустое означает «не задано».
func TestEmptyValueIsNotAValue(t *testing.T) {
	t.Setenv(neutral, "")
	t.Setenv(legacy, "old")
	v, name, ok := envknob.Lookup(neutral, legacy)
	if !ok || v != "old" || name != legacy {
		t.Fatalf("пустое нейтральное значение закрыло окно: значение %q, имя %q, найдено %v",
			v, name, ok)
	}
}

// TestWithoutAWindowOnlyTheNeutralNameIsRead — окна нет: прежнее написание не
// читается вовсе. Нужно там, где прежнего имени не было никогда.
func TestWithoutAWindowOnlyTheNeutralNameIsRead(t *testing.T) {
	t.Setenv(legacy, "old")
	if v, _, ok := envknob.Lookup(neutral, ""); ok || v != "" {
		t.Fatalf("без окна прочитано прежнее написание: %q", v)
	}
}
