// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// identifiers_injection_test.go — доказательство того, что разбор имён различает
// ИМЯ и ТЕКСТ.
//
// Без этой половины разбор ловил бы ЗНАК: «ноль находок» означало бы и «имена
// латинские», и «разбор ничего не увидел», а первая же законная кириллица в
// комментарии красила бы прогон — в этих деревьях ею написан весь разбор
// находок, десятки тысяч строк.
//
// Каждый мир отличается от близнеца ОДНИМ фактом: тот же знак переезжает из
// текста в имя.
package treehygiene_test

import (
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

// TestCyrillicOutsideNamesIsLawful — ЗАКОННЫЙ БЛИЗНЕЦ: комментарий, документация,
// строковый литерал и строка-ключ.
//
// Каждая форма ниже встречается в обоих деревьях сотнями и обязана остаться
// нетронутой. Положительный контроль стоит здесь же: имена в близнеце ЕСТЬ и
// осмотрены, значит «ноль находок» означает отсутствие предмета, а не пустой
// разбор.
func TestCyrillicOutsideNamesIsLawful(t *testing.T) {
	t.Parallel()
	const lawful = `package p

// Разбор по-русски: почему здесь именно так.

/* Документация тоже по-русски. */

var label = "Облачные сети"

var byKey = map[string]int{"имя": 1}

const refusal = "имя в коде обязано быть латинским"
`
	seen, found, err := treehygiene.ScanNonASCIIIdents("lawful.go", []byte(lawful))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: законная синтетика не разобралась: %v", err)
	}
	if seen == 0 {
		t.Fatal("близнец не дал ни одного имени — «молчит» неотличимо от «не читал»")
	}
	if len(found) != 0 {
		t.Errorf("кириллица в комментарии, документации, строке и ключе-строке законна, "+
			"а разбор нашёл %d: %v", len(found), found)
	}
	t.Logf("осмотрено имён в законном близнеце: %d", seen)
}

// TestSameRuneMovedIntoANameIsAFinding — ИНЪЕКЦИЯ: тот же знак, тот же файл,
// один факт разницы — он переехал из строкового литерала в имя.
//
// Пара доказывает существо, а не форму: если бы разбор ловил знак, обе стороны
// были бы красными; если бы не доходил до узлов-имён — обе зелёными.
func TestSameRuneMovedIntoANameIsAFinding(t *testing.T) {
	t.Parallel()
	const inText = "package p\n\nvar label = \"имя\"\n"
	const inName = "package p\n\nvar имя = \"label\"\n"

	textSeen, textFound, err := treehygiene.ScanNonASCIIIdents("text.go", []byte(inText))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	nameSeen, nameFound, err := treehygiene.ScanNonASCIIIdents("name.go", []byte(inName))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	if len(textFound) != 0 {
		t.Errorf("знак в строковом литерале объявлен находкой: %v", textFound)
	}
	if len(nameFound) != 1 || nameFound[0].Name != "имя" {
		t.Errorf("тот же знак в ИМЕНИ обязан находиться: %v", nameFound)
	}
	// Обе стороны обязаны быть ПРОЧИТАНЫ: иначе разница объясняется не
	// различением имени и текста, а тем, что один из миров не разобрался.
	if textSeen == 0 || nameSeen == 0 {
		t.Fatalf("осмотрено имён: в тексте %d, в имени %d — сторона с нулём ничего "+
			"не утверждает", textSeen, nameSeen)
	}
}

// TestEveryNonASCIIScriptIsCaughtNotJustCyrillic — предпосылка разбора названа и
// проверена: он судит ПРИНАДЛЕЖНОСТЬ знака ASCII, а не алфавит.
//
// Запрет владельца сформулирован про латиницу, и соблазн написать предикат «нет
// ли здесь кириллицы» велик. Такой предикат промолчал бы на греческой «ο» и на
// полноширинной латинице — знаках, дающих тот же омоглиф и ту же невидимость для
// отбора по имени.
func TestEveryNonASCIIScriptIsCaughtNotJustCyrillic(t *testing.T) {
	t.Parallel()
	worlds := []struct {
		name string
		src  string
	}{
		{"греческая омикрон", "package p\n\nvar cοunt = 1\n"},
		{"полноширинная латиница", "package p\n\nvar ｃount = 1\n"},
		{"кириллическая эс", "package p\n\nvar сount = 1\n"},
	}
	for _, w := range worlds {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()
			_, found, err := treehygiene.ScanNonASCIIIdents("world.go", []byte(w.src))
			if err != nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
			}
			if len(found) != 1 {
				t.Fatalf("омоглиф %s не пойман: %v — предикат обязан судить "+
					"принадлежность ASCII, а не алфавит", w.name, found)
			}
		})
	}
	// Законный близнец всех трёх миров: то же имя целиком латиницей.
	seen, found, err := treehygiene.ScanNonASCIIIdents("lawful.go", []byte("package p\n\nvar count = 1\n"))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	if seen == 0 {
		t.Fatal("близнец не дал ни одного имени — его молчание ничего не означает")
	}
	if len(found) != 0 {
		t.Errorf("чисто латинское имя объявлено находкой: %v", found)
	}
	t.Logf("осмотрено: миров с не-ASCII знаком %d, законный близнец один", len(worlds))
}
