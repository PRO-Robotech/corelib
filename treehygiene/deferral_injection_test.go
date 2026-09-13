// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// deferral_injection_test.go — доказательство способности разбора отложенной
// работы УПАСТЬ и СМОЛЧАТЬ.
//
// Обе стороны гоняют ТУ ЖЕ функцию, что зовёт гейт потребителя
// (treehygiene.AuditDeferredWork), на синтетическом репозитории во временном
// каталоге: своей рабочей копии инъекция не касается.
//
// Каждый мир отличается от своего законного близнеца ОДНИМ фактом: иначе
// неизвестно, какой из двух дал красное, и вердикт недействителен, хотя выглядит
// обычным зелёным.
//
// Формы маркеров здесь собираются из ЧАСТЕЙ по той же причине, что и в самом
// разборе: написанные целиком, они сделали бы этот файл нарушителем запрета,
// который он проверяет.
package treehygiene_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

// audit — обход синтетического дерева; отказ обхода есть «НЕ ВЫПОЛНИЛОСЬ», а не
// вердикт, поэтому он останавливает пробу, а не идёт в счёт находок.
func audit(t *testing.T, root string, skips ...treehygiene.DeferralSkip) (
	[]treehygiene.DeferralFinding, treehygiene.DeferralCensus) {
	t.Helper()
	findings, census, err := treehygiene.AuditDeferredWork(root, skips)
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: обход синтетического дерева: %v", err)
	}
	if census.Read == 0 {
		t.Fatal("проба НЕ ИСПОЛНЯЛАСЬ: синтетическое дерево не прочитано — " +
			"«находок нет» здесь означало бы «ничего не читал»")
	}
	return findings, census
}

// TestMarkerInProductionCodeIsCaughtWithItsCoordinate — сторона ДЕФЕКТА и её
// законный близнец, отличающийся ОДНИМ знаком препинания.
//
// Форма обращения к читателю кода (имя маркера с двоеточием) есть обещание;
// голое слово внутри предложения — разговор о маркере. Без близнеца запрет ловил
// бы СЛОВО, и первым делом пришлось бы переписать собственную документацию о
// запрете — а потом снять сам запрет.
func TestMarkerInProductionCodeIsCaughtWithItsCoordinate(t *testing.T) {
	t.Parallel()
	const head = "package thing\n\n"
	const tail = "\nfunc F() {}\n"

	defect := synthTree(t, map[string]string{
		"internal/thing/thing.go": head + "// " + "TODO" + ": дочинить после выпуска" + tail,
	})
	findings, _ := audit(t, defect)
	if len(findings) != 1 || !strings.Contains(findings[0].Where, "internal/thing/thing.go:3") {
		t.Fatalf("маркер в прод-коде не пойман с координатой: %+v", findings)
	}

	// ОДИН факт против дефекта: убрано двоеточие, всё остальное дословно то же.
	lawful := synthTree(t, map[string]string{
		"internal/thing/thing.go": head + "// файл " + "TODO" + " упразднён" + tail,
	})
	found, census := audit(t, lawful)
	if len(found) != 0 {
		t.Fatalf("разбор нашёл обещание там, где имя маркера лишь названо: %+v", found)
	}
	// Положительный контроль близнеца: затравка в файле ЕСТЬ, то есть молчание
	// объясняется решением образца, а не тем, что до образца дело не дошло.
	if !treehygiene.HasDeferralSeed(head + "// файл " + "TODO" + " упразднён") {
		t.Fatal("в близнеце нет затравки — его молчание неотличимо от отсева, " +
			"и об образце он не утверждает ничего")
	}
	t.Logf("%s", census)
}

// TestMarkerOutsideTheHandwrittenRootsIsCaught — сторона дефекта, которую
// ВЫПИСАННАЯ область не ловила бы: маркер в корне, которого в перечне быть не
// могло, и в файле, который кодом не является.
//
// Область обхода обязана ВЫВОДИТЬСЯ из индекса: каталог, появившийся после
// написания перечня, иначе покрыт не будет, и об этом никто не узнает.
func TestMarkerOutsideTheHandwrittenRootsIsCaught(t *testing.T) {
	t.Parallel()
	root := synthTree(t, map[string]string{
		"deploy/helm/values.yaml": "replicas: 1\n# " + "FIXME" + ": поднять предел\n",
	})
	findings, census := audit(t, root)
	if len(findings) != 1 || !strings.Contains(findings[0].Where, "deploy/helm/values.yaml") {
		t.Fatalf("маркер вне кода не пойман (%+v): область обхода обязана ВЫВОДИТЬСЯ "+
			"из индекса дерева", findings)
	}
	// Корень «deploy» обязан стоять в переписи: иначе «нашли» было бы верно, а
	// «область покрыта» — недоказуемо.
	if census.ByRoot["deploy"] == 0 {
		t.Errorf("корень deploy не попал в раскладку переписи: %s", census)
	}
}

// TestSubtractedKindIsTheConsumersAndItWorks — вид вычитания приходит СНАРУЖИ и
// действительно вычитает; без него тот же файл судится.
//
// Три прогона, а не два. Первый — контроль (вид объявлен, маркер в вычитаемом
// файле, молчание). Второй — ОДИН факт: тот же текст в файле, под вид не
// подпадающем. Третий — ОДИН факт в другую сторону: тот же вычитаемый файл, но
// вид не объявлен. Без третьего молчание первого неотличимо от разбора,
// разучившегося доходить до файла вовсе.
func TestSubtractedKindIsTheConsumersAndItWorks(t *testing.T) {
	t.Parallel()
	body := "package thing\n\n// " + "TODO" + ": фикстура гейта пишет форму дефекта\n"

	inTests := synthTree(t, map[string]string{"internal/thing/thing_test.go": body})
	silent, census := audit(t, inTests, testCorpusSkip())
	if len(silent) != 0 {
		t.Fatalf("вид вычитания не сработал: %+v", silent)
	}
	if census.Skipped["тестовый корпус"] != 1 {
		t.Fatalf("вычтено %d, ожидался 1 — молчание объясняется не вычитанием, "+
			"а тем, что разбор не дошёл до файла", census.Skipped["тестовый корпус"])
	}

	inProd := synthTree(t, map[string]string{"internal/thing/thing.go": body})
	found, _ := audit(t, inProd, testCorpusSkip())
	if len(found) != 1 {
		t.Fatalf("тот же текст в НЕ вычитаемом файле обязан находиться: %+v", found)
	}

	withoutSkip, _ := audit(t, inTests)
	if len(withoutSkip) != 1 {
		t.Fatalf("без объявленного вида тот же файл обязан судиться — иначе перечень "+
			"видов ни на что не влияет и вычитание держится не им: %+v", withoutSkip)
	}
}

// TestStdlibContextIsNotADeferral — ЗАКОННЫЙ БЛИЗНЕЦ, живой в обоих деревьях:
// имя функции стандартной библиотеки отсрочкой не является.
//
// Положительный контроль стоит РЯДОМ и отличается ОДНИМ фактом: в той же строке
// добавлена форма обращения к читателю кода. Без него молчание на имени функции
// было бы неотличимо от разбора, разучившегося падать.
func TestStdlibContextIsNotADeferral(t *testing.T) {
	t.Parallel()
	root := synthTree(t, map[string]string{
		"internal/a/lawful.go": "package a\n\nimport \"context\"\n\n" +
			"func F() { _ = context." + "TODO" + "() }\n",
		"internal/b/defect.go": "package b\n\nimport \"context\"\n\n" +
			"func F() { _ = context." + "TODO" + "() } // " + "TODO" + ": убрать\n",
	})
	findings, _ := audit(t, root)
	if len(findings) != 1 || !strings.Contains(findings[0].Where, "internal/b/defect.go") {
		t.Fatalf("вычет имени функции стандартной библиотеки сработал не по существу: %+v",
			findings)
	}
}

// TestQuotedMatchIsAMentionNotAPromise — ЗАКОННЫЙ БЛИЗНЕЦ русской формы:
// совпадение целиком внутри кавычек есть разговор о маркере.
//
// У русских форм нет знака препинания, по которому обращение отличалось бы от
// пересказа, поэтому граница проводится кавычками. Пара отличается ОДНИМ
// фактом: наличием «ёлочек» вокруг той же фразы.
func TestQuotedMatchIsAMentionNotAPromise(t *testing.T) {
	t.Parallel()
	const phrase = "потом " + "доделаем"

	lawful := synthTree(t, map[string]string{
		"internal/a/a.go": "package a\n\n// запрещено обещание «" + phrase + "»\n",
	})
	found, census := audit(t, lawful)
	if len(found) != 0 {
		t.Fatalf("цитата принята за обещание: %+v", found)
	}
	if census.Mentions != 1 {
		t.Fatalf("отсечённых упоминаний %d, ожидалось 1 — молчание объясняется не "+
			"границей употребления, а тем, что образец не сработал вовсе", census.Mentions)
	}

	defect := synthTree(t, map[string]string{
		"internal/a/a.go": "package a\n\n// запрещено обещание " + phrase + "\n",
	})
	promise, promiseCensus := audit(t, defect)
	if len(promise) != 1 {
		t.Fatalf("та же фраза без кавычек обязана находиться: %+v", promise)
	}
	if promiseCensus.Mentions != 0 {
		t.Errorf("отсечено упоминаний %d при отсутствии кавычек", promiseCensus.Mentions)
	}
}

// TestTwoPromisesInOneLineAreOneFinding — дедупликация по строке, и она не
// съедает обещание, стоящее рядом с цитатой.
//
// Отсев упоминаний идёт ДО дедупликации намеренно: строка, несущая и цитату, и
// обещание, при обратном порядке потеряла бы второе вместе с первым — то есть
// кавычки вокруг ЧУЖОЙ фразы глушили бы СВОЮ.
func TestTwoPromisesInOneLineAreOneFinding(t *testing.T) {
	t.Parallel()
	twoInOne := synthTree(t, map[string]string{
		"internal/a/a.go": "package a\n\n// " + "TODO" + ": раз; " + "FIXME" + ": два\n",
	})
	found, _ := audit(t, twoInOne)
	if len(found) != 1 {
		t.Fatalf("два обещания в одной строке обязаны дать одну находку: %+v", found)
	}

	quoteAndPromise := synthTree(t, map[string]string{
		"internal/a/a.go": "package a\n\n// не «" + "потом " + "доделаем" + "», а " +
			"позже " + "допилим" + "\n",
	})
	mixed, census := audit(t, quoteAndPromise)
	if len(mixed) != 1 {
		t.Fatalf("обещание рядом с цитатой потеряно вместе с ней: %+v", mixed)
	}
	if census.Mentions != 1 {
		t.Errorf("цитата в той же строке не отсечена: упоминаний %d", census.Mentions)
	}
}
