// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// deferral_test.go — предпосылки разбора отложенной работы: каждая объявленная
// форма доходит до решения, перепись отвечает о том дереве, которое обходили, а
// перечень форм от вызывающего не двигается.
//
// Способность разбора упасть и смолчать доказана инъекцией —
// deferral_injection_test.go.
package treehygiene_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

// TestEveryDeferralFormIsCaughtThroughTheSieve — КАЖДАЯ объявленная форма
// доходит до решения, ПРОЙДЯ дешёвый отсев.
//
// Отсев по затравке существует ради времени. Но отсев — это место, где форма
// может исчезнуть молча: образец её ловит, а до образца дело не доходит.
// Поэтому каждая форма проверяется НА СКВОЗНОМ пути — тем же AuditDeferredWork,
// что работает по дереву, а не образцом в отрыве от сита.
//
// ЧЕГО ЭТА ПРОБА НЕ УТВЕРЖДАЕТ, и это сказано прямо: она проверяет, что ловится
// каждая ОБЪЯВЛЕННАЯ форма, а не что объявлены все нужные. Полнота словаря есть
// утверждение о мире, а не о дереве, и машинного предиката не имеет. Что форма
// не исчезнет из словаря молча, держит нижняя граница
// (TestTheDictionaryDoesNotShrinkSilently).
func TestEveryDeferralFormIsCaughtThroughTheSieve(t *testing.T) {
	t.Parallel()
	forms := treehygiene.DeferralForms()
	if len(forms) == 0 {
		t.Fatal("осмотрено: форм 0 — «все формы ловятся» здесь означало бы «форм нет»")
	}
	for i, f := range forms {
		t.Run(fmt.Sprintf("%02d-%s", i, f.Seed), func(t *testing.T) {
			t.Parallel()
			if !treehygiene.HasDeferralSeed(f.Example) {
				t.Fatalf("затравка %q не встречается в примере %q — отсев отсечёт эту "+
					"форму ДО образца, и она перестанет ловиться, оставаясь на вид "+
					"объявленной", f.Seed, f.Example)
			}
			root := synthTree(t, map[string]string{
				"internal/thing/thing.go": "package thing\n\n" + f.Example + "\nfunc F() {}\n",
			})
			findings, census, err := treehygiene.AuditDeferredWork(root, nil)
			if err != nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: обход синтетического дерева: %v", err)
			}
			if census.Read == 0 {
				t.Fatal("проба НЕ ИСПОЛНЯЛАСЬ: синтетическое дерево не прочитано")
			}
			if len(findings) != 1 {
				t.Fatalf("форма %q на примере %q не поймана сквозным путём: %+v",
					f.Pattern, f.Example, findings)
			}
		})
	}
	t.Logf("осмотрено: форм %d, затравок %d", len(forms), len(treehygiene.DeferralSeeds()))
}

// TestDeferralFormsAreHandedOutAsACopy — перечень форм, отданный вызывающему,
// перечнем разбора не является.
//
// Без этого разбор судил бы тем словарём, который ему подсунул последний
// читатель: правка отданного среза уехала бы в общий массив, и следующая проба
// получила бы другой предмет, ничего об этом не сказав.
//
// Сличать надо ЗНАЧЕНИЕ, а не два среза. Первая редакция этой пробы держала
// «до» срезом и была ВАКУУМНОЙ: без копии оба среза смотрят в один массив,
// правка двигает обе стороны сравнения разом, и проба зеленела ровно на том
// дефекте, который стерегла. Поймала это не вычитка, а инъекция — снятие копии
// из DeferralForms вердикта не изменило.
func TestDeferralFormsAreHandedOutAsACopy(t *testing.T) {
	t.Parallel()
	if len(treehygiene.DeferralForms()) == 0 {
		t.Fatal("осмотрено: форм 0 — утверждать о копии нечего")
	}
	want := treehygiene.DeferralForms()[0] // ЗНАЧЕНИЕ: копия структуры, не вид на массив

	mine := treehygiene.DeferralForms()
	mine[0] = treehygiene.DeferralForm{Seed: "подмена", Pattern: "подмена", Example: "подмена"}
	if got := treehygiene.DeferralForms()[0]; got != want {
		t.Fatalf("правка отданного среза уехала в перечень разбора: было %+v, стало %+v",
			want, got)
	}

	if len(treehygiene.DeferralSeeds()) == 0 {
		t.Fatal("осмотрено: затравок 0 — утверждать о копии нечего")
	}
	wantSeed := treehygiene.DeferralSeeds()[0]
	seeds := treehygiene.DeferralSeeds()
	seeds[0] = "подмена"
	if got := treehygiene.DeferralSeeds()[0]; got != wantSeed {
		t.Fatalf("правка отданного среза затравок уехала в перечень разбора: "+
			"было %q, стало %q", wantSeed, got)
	}
	t.Logf("осмотрено: форм %d, затравок %d",
		len(treehygiene.DeferralForms()), len(treehygiene.DeferralSeeds()))
}

// deferralFormFloor — форм не меньше, чем объявлено на день заведения пакета.
//
// Это НИЖНЯЯ ГРАНИЦА, а не перепись: добавление формы её не двигает, поэтому
// числу нечем устареть. Стережёт она ровно тот класс, ради которого у словаря
// заведён один дом: форма, ИСЧЕЗНУВШАЯ из словаря, не даёт ни красного, ни
// зелёного — написанное в ней перестаёт быть и находкой, и чистотой. Сквозная
// проба ниже этого не видит by construction: она обходит то, что объявлено, и
// инъекция это подтвердила — выпадение формы её красной не сделало.
//
// Снять форму по-прежнему можно — но тогда придётся опустить и границу, то есть
// сделать это ЯВНО, а не молча.
const deferralFormFloor = 12

// TestTheDictionaryDoesNotShrinkSilently — словарь не теряет формы незаметно и
// не содержит двух записей об одном образце.
//
// Второе — не педантизм: одинаковый образец у двух записей означает, что запись
// скопировали и забыли править, и одна из двух объявленных форм не ловится
// вовсе, оставаясь на вид объявленной.
func TestTheDictionaryDoesNotShrinkSilently(t *testing.T) {
	t.Parallel()
	forms := treehygiene.DeferralForms()
	if len(forms) < deferralFormFloor {
		t.Errorf("форм %d при нижней границе %d — форма ушла из словаря. Написанное "+
			"в ней перестало быть и находкой, и чистотой: это НЕВИДИМОСТЬ. Снятие "+
			"формы законно, но тогда опустите границу тем же изменением",
			len(forms), deferralFormFloor)
	}
	seen := map[string]int{}
	for i, f := range forms {
		seen[f.Pattern]++
		if seen[f.Pattern] > 1 {
			t.Errorf("образец %q объявлен дважды (запись %d) — одна из двух форм "+
				"не ловится вовсе, оставаясь на вид объявленной", f.Pattern, i)
		}
		if f.Seed == "" || f.Pattern == "" || f.Example == "" {
			t.Errorf("запись %d неполна: %+v — пустое поле делает форму немой", i, f)
		}
	}
	t.Logf("осмотрено: форм %d, различных образцов %d, нижняя граница %d",
		len(forms), len(seen), deferralFormFloor)
}

// TestDeferralCensusAnswersAboutTheTreeItWalked — перепись называет ИМЕННО то
// дерево, которое обходили: корни выведены из индекса, прочитанное сходится с
// разложенным по корням, вычтенное названо поимённо.
//
// Без этого «ноль находок» неотличимо от «ноль прочитанного», а «область
// покрыта» — от «область выписана».
func TestDeferralCensusAnswersAboutTheTreeItWalked(t *testing.T) {
	t.Parallel()
	root := synthTree(t, map[string]string{
		"internal/a/a.go":      "package a\n",
		"internal/b/b_test.go": "package b\n",
		"deploy/values.yaml":   "replicas: 1\n",
	})
	_, census, err := treehygiene.AuditDeferredWork(root, []treehygiene.DeferralSkip{testCorpusSkip()})
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: обход синтетического дерева: %v", err)
	}
	t.Logf("%s", census)

	if census.Tracked != 4 {
		t.Errorf("в индексе %d, ожидалось 4 (README + три файла пробы)", census.Tracked)
	}
	// Корни ВЫВЕДЕНЫ: файл в корне даёт «.», каталоги — свои имена. Выписанный
	// перечень корней молчал бы о каталоге, заведённом завтра.
	want := []string{".", "deploy", "internal"}
	if strings.Join(census.Roots, " ") != strings.Join(want, " ") {
		t.Errorf("корни %v, ожидались %v — область обхода обязана ВЫВОДИТЬСЯ из индекса",
			census.Roots, want)
	}
	if census.Read != 3 {
		t.Errorf("прочитано %d, ожидалось 3 (четвёртый вычтен как тестовый корпус)", census.Read)
	}
	var byRoot int
	for _, r := range census.Roots {
		byRoot += census.ByRoot[r]
	}
	if byRoot != census.Read {
		t.Errorf("раскладка по корням даёт %d, прочитано %d — перепись не сходится "+
			"сама с собой, и ни одной её половине верить нельзя", byRoot, census.Read)
	}
	if census.Skipped["тестовый корпус"] != 1 {
		t.Errorf("вычтено из тестового корпуса %d, ожидался 1", census.Skipped["тестовый корпус"])
	}
	if !strings.Contains(census.String(), "тестовый корпус=1") {
		t.Errorf("вид вычитания не назван в переписи: %s — вычтенное, о котором "+
			"перепись молчит, неотличимо от непрочитанного", census)
	}
}

// TestDeferralCensusSaysWhenNoSkipsWereDeclared — пустой перечень видов назван
// СЛОВОМ, а не пустым местом.
//
// Пустое поле в переписи читается как «виды были и ничего не вычли» — то есть
// как находка потребителя. Разница несущая: «видов не объявлено» есть законный
// и более СТРОГИЙ вход, и молчать о нём нельзя.
func TestDeferralCensusSaysWhenNoSkipsWereDeclared(t *testing.T) {
	t.Parallel()
	root := synthTree(t, map[string]string{"internal/a/a.go": "package a\n"})
	_, census, err := treehygiene.AuditDeferredWork(root, nil)
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: обход синтетического дерева: %v", err)
	}
	if !strings.Contains(census.String(), "видов не объявлено") {
		t.Errorf("перепись без видов вычитания не сказала об этом словом: %s", census)
	}
	// Положительный контроль: с объявленным видом та же перепись говорит иначе.
	// Без него совпадение с образцом выше было бы неотличимо от строки, которая
	// печатается всегда.
	_, withSkip, err := treehygiene.AuditDeferredWork(root, []treehygiene.DeferralSkip{testCorpusSkip()})
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: обход синтетического дерева: %v", err)
	}
	if strings.Contains(withSkip.String(), "видов не объявлено") {
		t.Errorf("перепись с объявленным видом сказала, что видов нет: %s", withSkip)
	}
	t.Logf("без видов: %s", census)
	t.Logf("с видом:   %s", withSkip)
}
