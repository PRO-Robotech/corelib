// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// identifiers_test.go — предпосылки разбора имён: он видит КАЖДУЮ форму
// объявления имени и отдаёт координату со знаком.
//
// Что разбор молчит на законной кириллице — сторона близнеца, она в
// identifiers_injection_test.go.
package treehygiene_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

// TestScanSeesEveryFormOfDeclaredName — формы имени, которые легко ускользают.
//
// Разбор видит их все как узел-идентификатор, но УТВЕРЖДАТЬ это надо, а не
// полагать: перепись, сделанная поиском по образцу, занизила счёт на четверть —
// она не видела имён из деструктуризации.
//
// Способность этой проверки упасть доказана рядом: у каждой формы есть
// латинский близнец, на котором она обязана дать ноль. Без него «нашлось»
// означало бы и «разбор различает форму», и «разбор помечает всё подряд».
func TestScanSeesEveryFormOfDeclaredName(t *testing.T) {
	t.Parallel()
	forms := []struct {
		name    string
		src     string
		want    string
		lawful  string
		seenMin int
	}{
		{
			name:   "поле структуры",
			src:    "package p\n\ntype T struct{ поле string }\n",
			want:   "поле",
			lawful: "package p\n\ntype T struct{ field string }\n",
		},
		{
			name:   "метод",
			src:    "package p\n\ntype T struct{}\n\nfunc (T) метод() {}\n",
			want:   "метод",
			lawful: "package p\n\ntype T struct{}\n\nfunc (T) method() {}\n",
		},
		{
			name:   "константа",
			src:    "package p\n\nconst Предел = 1\n",
			want:   "Предел",
			lawful: "package p\n\nconst Limit = 1\n",
		},
		{
			name:   "параметр типа",
			src:    "package p\n\nfunc f[Тип any](x Тип) Тип { return x }\n",
			want:   "Тип",
			lawful: "package p\n\nfunc f[T any](x T) T { return x }\n",
		},
		{
			name:   "псевдоним импорта",
			src:    "package p\n\nimport фмт \"fmt\"\n\nvar _ = фмт.Sprint\n",
			want:   "фмт",
			lawful: "package p\n\nimport fm \"fmt\"\n\nvar _ = fm.Sprint\n",
		},
		{
			name:   "имя из деструктуризации",
			src:    "package p\n\nfunc f() { знач, ошибка := g(); _, _ = знач, ошибка }\n\nfunc g() (int, error) { return 0, nil }\n",
			want:   "знач",
			lawful: "package p\n\nfunc f() { val, err := g(); _, _ = val, err }\n\nfunc g() (int, error) { return 0, nil }\n",
		},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			t.Parallel()
			seen, found, err := treehygiene.ScanNonASCIIIdents("form.go", []byte(form.src))
			if err != nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: синтетика не разобралась: %v", err)
			}
			var hit bool
			for _, f := range found {
				if f.Name == form.want {
					hit = true
				}
			}
			if !hit {
				t.Errorf("имя %q обязано находиться; осмотрено имён %d, найдено %v",
					form.want, seen, found)
			}
			// Латинский близнец той же формы: ОДИН факт против дефекта —
			// написание имени, всё остальное дословно то же.
			lawfulSeen, lawfulFound, err := treehygiene.ScanNonASCIIIdents(
				"lawful.go", []byte(form.lawful))
			if err != nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: близнец не разобрался: %v", err)
			}
			if lawfulSeen == 0 {
				t.Fatal("близнец не дал ни одного имени — его молчание неотличимо " +
					"от «разбор не дошёл до узлов»")
			}
			if len(lawfulFound) != 0 {
				t.Errorf("латинский близнец той же формы объявлен находкой: %v", lawfulFound)
			}
		})
	}
	t.Logf("осмотрено: форм объявления имени %d", len(forms))
}

// TestFindingCarriesPositionNameAndRune — находка есть ДЕЙСТВИЕ, а не сигнал.
//
// Без координаты имя не найти: на глаз оно от латинского двойника неотличимо, а
// текстовая замена попадает внутрь строк и комментариев, где кириллица законна.
// Поэтому находка обязана нести файл, строку, само имя и ЗНАК.
func TestFindingCarriesPositionNameAndRune(t *testing.T) {
	t.Parallel()
	const src = "package p\n\nvar сount = 1\n"
	seen, found, err := treehygiene.ScanNonASCIIIdents("homoglyph.go", []byte(src))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: синтетика не разобралась: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("омоглиф обязан находиться — он и есть предмет разбора; осмотрено %d, "+
			"находок %d: %v", seen, len(found), found)
	}
	f := found[0]
	if !strings.HasPrefix(f.Position, "homoglyph.go:3:") {
		t.Errorf("координата %q не называет файл и строку", f.Position)
	}
	if f.Rune != 'с' {
		t.Errorf("знак %q, ожидался кириллический «с» — печатать надо ИМЕННО знак: "+
			"на глаз он от латинского неотличим", f.Rune)
	}
	// Текст находки называет обе половины: без имени непонятно, что править, без
	// кода знака — чем оно отличается от соседнего латинского.
	text := f.String()
	if !strings.Contains(text, "U+0441") || !strings.Contains(text, "сount") {
		t.Errorf("текст находки не называет знак кодом и имя дословно: %s", text)
	}
}

// TestUnparsedSourceIsARefusalNotAnEmptySuccess — неразбираемый файл возвращает
// признак, а не пустой успех.
//
// Пустой успех завысил бы перепись: файл считался бы осмотренным, не будучи
// прочитанным, и «ноль находок» означало бы «ноль разобранного».
func TestUnparsedSourceIsARefusalNotAnEmptySuccess(t *testing.T) {
	t.Parallel()
	seen, found, err := treehygiene.ScanNonASCIIIdents("broken.go", []byte("package p\n\nfunc {\n"))
	if err == nil {
		t.Fatal("неразбираемый исходник принят за пустой успех — перепись завысила бы " +
			"объём осмотренного")
	}
	if seen != 0 || len(found) != 0 {
		t.Errorf("отказ разбора отдал перепись %d и находок %d — числа о файле, "+
			"который не прочитан", seen, len(found))
	}
}
