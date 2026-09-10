// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"strings"
	"testing"
	"time"
)

// RET-PLAT-10 — нижняя граница порога ДОКАЗУЕМА и равна бюджету полла клиента.
//
// Приёмка RET-PLAT-1 §2.3(в): клиент, начавший ждать сразу после мутации, вправе
// опрашивать до `created_at + DefaultAwaitBudget`; так как `modified_at >=
// created_at`, порог, отсчитанный от `modified_at` и не меньший бюджета,
// гарантирует, что опрашивающий в пределах ОБЪЯВЛЕННОГО бюджета клиент не
// встретит снятой строки никогда.
//
// Сравнение НЕСТРОГОЕ (§2.3): нестрогая граница — это то, что доказано; строгая
// требовала бы на единицу больше без основания. Нестрогость держится строгим `<`
// в самом предикате уборки, и обе строгости проверяются здесь вместе — потерять
// связь молча иначе нечем.
//
// Величина бюджета не импортируется из `terraform/`: провайдер — клиент
// платформы, и зависимость `pkg/` от него была бы ребром снизу вверх. Число
// повторено с координатой, а согласие держит TestAwaitBudgetIsStillFiveMinutes.
func TestOperationRetentionCoversTheDeclaredPollBudget(t *testing.T) {
	const declaredAwaitBudget = 5 * time.Minute

	if got := OperationRetention + ClockDrift; got < declaredAwaitBudget {
		t.Fatalf("порог уборки %v МЕНЬШЕ объявленного бюджета полла %v: клиент, "+
			"опрашивающий в пределах обещанного, встретил бы снятую строку",
			got, declaredAwaitBudget)
	}

	// Контроль в обратную сторону: предикат обязан сравнивать СТРОГО, иначе
	// последний миг объявленного бюджета застаёт строку уже снятой.
	if !strings.Contains(sweepTerminalSQL, "modified_at <") ||
		strings.Contains(sweepTerminalSQL, "modified_at <=") {
		t.Fatalf("предикат уборки обязан сравнивать modified_at СТРОГО (`<`): "+
			"на равенстве последний миг бюджета теряется молча. Оператор:\n%s", sweepTerminalSQL)
	}
}

// RET-PLAT-01/02 — уборка трогает ТОЛЬКО терминальные строки.
//
// §2.3(а): читатели нетерминальных строк (воркер, реконсайлер) делаются
// непересекающимися с убираемым множеством BY CONSTRUCTION, а не согласованными
// по величине. Уборка нетерминальной строки отняла бы работу у реконсайлера, а
// работавший воркер получил бы «не найдено» вместо коммита.
func TestSweepTouchesOnlyTerminalRows(t *testing.T) {
	if !strings.Contains(sweepTerminalSQL, "done = true") {
		t.Fatalf("предикат уборки не ограничен терминальными строками (`done = true`): "+
			"нетерминальная строка есть работа реконсайлера. Оператор:\n%s", sweepTerminalSQL)
	}
	// §2.3(б): возраст считается от modified_at, а не от created_at —
	// created_at говорит, когда мутацию приняли, а не когда исход стал
	// окончательным.
	if strings.Contains(sweepTerminalSQL, "created_at <") {
		t.Fatalf("возраст обязан считаться от modified_at, а не от created_at: "+
			"created_at не ограничивает нетерминальную фазу. Оператор:\n%s", sweepTerminalSQL)
	}
}

// RET-PLAT-04 — часы уборки БАЗЫ, и слагаемое запаса объявлено.
//
// §2.5: `modified_at` пишется часами ПРОЦЕССА, уборка судит часами БАЗЫ.
// Источники разные ⇒ разница входит в порог отдельным слагаемым. Момент времени
// в сигнатуру уборщика не приходит: уборщик, принимающий часы входом, судит
// теми же часами, что и писатель, и слагаемое становится необоснованным.
func TestSweepJudgesByDatabaseClock(t *testing.T) {
	if !strings.Contains(sweepTerminalSQL, "now()") {
		t.Fatalf("уборка обязана судить часами БАЗЫ (`now()`): часы процесса дали бы "+
			"столько источников, сколько реплик. Оператор:\n%s", sweepTerminalSQL)
	}
	if ClockDrift <= 0 {
		t.Fatal("ClockDrift обязан быть положительным: он закрывает пару «наш процесс " +
			"против нашей базы», и ноль означал бы, что источники совпадают")
	}
}

// Партия обязана ограничивать оператор и запирать строки: уборка не вправе быть
// причиной отказа на пути запроса, а вторая реплика обязана уносить только
// остаток (запись «РЕПЛИКИ:» формы уборки).
func TestSweepIsBatchedAndClaimsRows(t *testing.T) {
	for _, want := range []string{"LIMIT", "FOR UPDATE SKIP LOCKED"} {
		if !strings.Contains(sweepTerminalSQL, want) {
			t.Fatalf("оператор уборки без %q: без предела партии это длинная транзакция "+
				"на горячей таблице, без клейма — вторая реплика повторяет работу первой.\n%s",
				want, sweepTerminalSQL)
		}
	}
}

// Имя таблицы обязано стоять в операторе ЛИТЕРАЛОМ.
//
// Не стиль: гейт роста (`internal/repohygiene` TestLiveTablesNameTheirGrowthLimit)
// разрешает имя таблицы РАЗБОРОМ ИСХОДНИКА, и имя, приехавшее подстановкой,
// уходит в «слепую зону, названную числом» — механизм есть, гейт его не видит, а
// запись реестра остаётся. Это ровно те два места об одном предмете, ради
// которых обе задачи и заведены.
//
// Подставляется только СХЕМА: она у каждого владельца своя, а имя таблицы одно
// на все восемь.
func TestSweepNamesItsTableLiterally(t *testing.T) {
	if !strings.Contains(sweepTerminalSQL, "%s.operations") {
		t.Fatalf("имя таблицы обязано стоять литералом (`%%s.operations`), иначе гейт роста "+
			"не разрешит его и механизм останется невидимым. Оператор:\n%s", sweepTerminalSQL)
	}
}

// fakeTerminalSweeper — подставной уборщик: записывает, с каким порогом и какой
// партией его позвали, и отвечает заданной последовательностью исходов.
//
// Подделка НЕ снисходительнее продукта в том, что здесь проверяется: она не
// решает, какие строки снимать, — она свидетельствует о ВЫЗОВЕ. Всё, что она
// умеет соврать, названо в пробе, которая её применяет.
type fakeTerminalSweeper struct {
	graces  []time.Duration
	batches []int
	// alwaysFull — партия всегда уходит полной: так проверяется ПОТОЛОК партий
	// за проход. Конечная последовательность исходов проверяла бы длину этой
	// последовательности, а не потолок, — на ней проба зеленела бы при любом
	// потолке выше её длины.
	alwaysFull bool
	err        error
}

func (f *fakeTerminalSweeper) SweepTerminal(_ context.Context, grace time.Duration, batch int) (int64, bool, error) {
	f.graces = append(f.graces, grace)
	f.batches = append(f.batches, batch)
	if f.err != nil {
		return 0, false, f.err
	}
	if f.alwaysFull {
		return int64(batch), true, nil
	}
	return 1, false, nil
}

// Порог уборки собирается ОДИН раз в этом пакете, а не у каждого из восьми
// владельцев: восемь копий одного порога разошлись бы молча и дали бы разный
// срок жизни операции у разных доменов при одном контракте.
func TestRetentionSubjectCarriesTheDeclaredThreshold(t *testing.T) {
	f := &fakeTerminalSweeper{}
	subj := RetentionSubject(f)

	if subj.Name != SubjectOperations {
		t.Fatalf("предмет назван %q, а таблица зовётся %q: оператор обязан узнавать таблицу в отчёте прохода",
			subj.Name, SubjectOperations)
	}
	want := OperationRetention + ClockDrift
	if subj.Grace != want {
		t.Fatalf("порог предмета %v, объявлено %v (OperationRetention + ClockDrift): "+
			"порог, собранный мимо объявления, невидим оператору", subj.Grace, want)
	}
	if subj.Sweep == nil {
		t.Fatal("запись реестра без уборщика — объявление без предмета")
	}
}

// Проход обязан ДОГОНЯТЬ накопленное: партия, ушедшая полной, повторяется, но
// не более потолка. Без этого уборка со скоростью «одна партия за тик» не
// догоняла бы внешний темп НИКОГДА, оставаясь зелёной по всякой проверке
// «вызвался ли».
func TestRetentionSweepCatchesUpWithinTheCeiling(t *testing.T) {
	f := &fakeTerminalSweeper{alwaysFull: true}
	sw, err := NewRetentionSweeper(f, DefaultRetentionConfig(), nil)
	if err != nil {
		t.Fatalf("сборка уборщика: %v", err)
	}
	res := sw.Pass(context.Background())

	if got := len(f.batches); got != DefaultRetentionConfig().MaxBatchesPerPass {
		t.Fatalf("партий за проход %d, потолок %d: проход обязан упираться в потолок, "+
			"а не идти неограниченно", got, DefaultRetentionConfig().MaxBatchesPerPass)
	}
	for i, g := range f.graces {
		if g != OperationRetention+ClockDrift {
			t.Fatalf("партия %d позвана с порогом %v вместо объявленного %v", i, g, OperationRetention+ClockDrift)
		}
	}
	// Ключ предмета обязан быть в отчёте ВСЕГДА — иначе «нечего убирать»
	// неотличимо от «уборка не доходит до этой записи».
	if _, ok := res.Removed[SubjectOperations]; !ok {
		t.Fatalf("в отчёте прохода нет ключа предмета %q: ноль по предмету обязан быть назван",
			SubjectOperations)
	}
}

// Величины уборки по умолчанию обязаны проходить собственный страж: конфигурация,
// собранная с нулевой партией, исполняется и не убирает ничего — то есть
// выглядит работающей, будучи мёртвой.
func TestDefaultRetentionConfigPassesItsOwnGuard(t *testing.T) {
	if err := DefaultRetentionConfig().Validate(); err != nil {
		t.Fatalf("умолчание величин уборки не проходит собственный страж: %v", err)
	}
}
