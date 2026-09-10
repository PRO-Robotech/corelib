// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listnarrow_test

// page_bench_test.go — СТОИМОСТЬ СТРАНИЦЫ: сколько стоит сузить выдачу списка
// по правам, и из чего эта стоимость складывается.
//
// # Почему именно этот путь
//
// Сужение страницы — единственная работа, которую платит КАЖДЫЙ списочный вызов
// КАЖДОГО ресурса платформы, и единственная, чья стоимость растёт с `page_size`
// (по контракту — до 1000). Изменение, удваивающее её, удваивает стоимость
// всякого списка сразу; ниже по стеку такого рычага нет.
//
// # Что здесь измеряется, а что НЕТ — сказано прямо
//
// Сосед подставной и отвечает мгновенно, поэтому числа НЕ говорят ничего о
// задержке живого пути: там царит сеть. Измеряется РАБОТА САМОГО СУЖАТЕЛЯ —
// нарезка партий, веер, сборка запросов, окно вердиктов, сборка ответа. Именно
// она и есть то, что правится в этом дереве и что регрессирует незаметно.
//
// # Воспроизводимость: три вещи, без которых числа несравнимы
//
//  1. ЧАСЫ фиксированы (`WithClock`). Окно вердиктов истекает по времени, и на
//     живых часах длинный прогон получил бы истечения в середине — то есть
//     мерил бы разное на разных итерациях;
//  2. ВЕЕР задан явно (`Parallelism`), а не взят умолчанием: он определяет число
//     горутин на страницу, и умолчание, сменившись, молча сдвинуло бы все числа;
//  3. СОСЕД без общего изменяемого состояния — иначе при вееере больше единицы
//     мерились бы блокировки дублёра, а не работа сужателя.
//
// # Как прогнать
//
//	go test ./pkg/listnarrow/ -run '^$' -bench BenchmarkPageNarrow -benchtime 200x
//
// # Замер 2026-08-22 — ОРИЕНТИР, а не порог
//
// Снят названной выше командой на машине разработчика. Числа зависят от машины
// и порогом быть не могут: на общем ранере они плавают вдвое сами по себе, и
// первый же ложный срабат такой порог отключит. Порогом здесь служит
// [TestPageCostShapeIsLinearInBatchesNotInRows] — он утверждает то, что от
// машины не зависит.
//
//	страница | окно вердиктов | нс/строку | байт/страницу | аллокаций
//	---------|----------------|-----------|---------------|----------
//	       1 | холодное       |     21443 |          1769 |        32
//	      50 | холодное       |      2505 |         38657 |       385
//	     100 | холодное       |      3145 |         75777 |       735
//	    1000 | холодное       |      4001 |        803519 |      7145
//	       1 | тёплое         |      1539 |           384 |         9
//	      50 | тёплое         |      1045 |         11768 |        68
//	     100 | тёплое         |       979 |         22744 |       118
//	    1000 | тёплое         |      1883 |        277648 |      1024
//
// Что из этого видно и стоило того, чтобы записать: окно вердиктов снимает
// примерно половину внутрипроцессной стоимости предельной страницы (4001 против
// 1883 нс на строку) и три четверти её аллокаций (7145 против 1024). То есть
// правка, ломающая попадание в окно, дорога — и при этом не меняет НИ ОДНОГО
// исхода, поэтому не роняет ни одной пробы, кроме той, что стоит ниже.
//
// # Чего этот замер НЕ закрывает
//
// В конвейере этого дерева `-bench` не встречается ни разу (предикат:
// `grep -rn -- '-bench' .github scripts Makefile`), поэтому ни этот замер, ни
// два прежних не исполняются нигде и ни с чем не сравниваются. Сам по себе он
// отвечает на «сколько стоит» и не отвечает на «не стало ли вдвое дороже» — на
// второй вопрос отвечает проба ниже, и только она.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/listnarrow/narrowtest"
)

// benchPeer — сосед без изменяемого состояния.
//
// Дублёр из `narrowtest` для замера не годится: он ведёт перепись вызовов в
// общих полях, поэтому при вееере больше единицы мерились бы гонки за его
// поля, а не работа сужателя. Здесь ответ строится только из запроса.
type benchPeer struct{ allow bool }

func (p benchPeer) BatchCheck(_ context.Context, checks []listnarrow.Check) ([]bool, error) {
	out := make([]bool, 0, len(checks))
	for range checks {
		out = append(out, p.allow)
	}
	return out, nil
}

// countingPeer — сосед, считающий ОБРАЩЕНИЯ и ВОПРОСЫ.
//
// Отдельный тип, а не поле в `benchPeer`: счётчик в замеряемом дублёре был бы
// той самой общей ячейкой, которой замер не должен касаться.
// Счётчики АТОМАРНЫ: сужение зовёт соседа из нескольких горутин — это его
// смысл, батчи идут параллельно. Обычные поля здесь дают гонку, и поймал её
// не замер, а прогон с детектором: локальная короткая группа бенчмарки не
// гоняет, поэтому «локально зелено» относилось к меньшему, чем казалось.
type countingPeer struct {
	allow  bool
	calls  atomic.Int64
	checks atomic.Int64
}

func (p *countingPeer) BatchCheck(_ context.Context, checks []listnarrow.Check) ([]bool, error) {
	p.calls.Add(1)
	p.checks.Add(int64(len(checks)))
	out := make([]bool, 0, len(checks))
	for range checks {
		out = append(out, p.allow)
	}
	return out, nil
}

// benchClock — управляемые часы: замер обязан быть детерминирован, а окно
// вердиктов истекает по времени.
type benchClock struct{ at time.Time }

func newBenchClock() *benchClock {
	return &benchClock{at: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)}
}

func (c *benchClock) now() time.Time          { return c.at }
func (c *benchClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// benchNarrower — сужатель с фиксированными часами и явным веером.
func benchNarrower(peer listnarrow.AuthorizeClient, cacheTTL time.Duration) *listnarrow.Narrower {
	n, _ := benchNarrowerWithClock(peer, cacheTTL)
	return n
}

// benchNarrowerWithClock отдаёт сужатель ВМЕСТЕ с его часами — там, где пробе
// нужно перевести время вперёд.
func benchNarrowerWithClock(peer listnarrow.AuthorizeClient,
	cacheTTL time.Duration) (*listnarrow.Narrower, *benchClock) {
	clock := newBenchClock()
	return listnarrow.New(peer, listnarrow.Config{
		Relations:       map[string][]string{"": {"v_get"}},
		Timeout:         5 * time.Second,
		Parallelism:     listnarrow.DefaultParallelism,
		CacheTTL:        cacheTTL,
		CacheMaxEntries: 10000,
	}).WithClock(clock.now), clock
}

// row — запись страницы: сужатель работает с идентификаторами, поэтому тело
// записи намеренно пустое — иначе замер включал бы стоимость чужой структуры.
type row struct{ id string }

func page(n int) []row {
	out := make([]row, n)
	for i := range out {
		out[i] = row{id: fmt.Sprintf("net%017d", i)}
	}
	return out
}

// benchSizes — размеры страницы по КОНТРАКТУ, а не по вкусу: умолчание 50,
// потолок 1000, плюс граница партии 100 и единица как нижний край.
var benchSizes = []int{1, 50, 100, 1000}

// BenchmarkPageNarrow_ColdVerdictWindow — окно вердиктов пусто: каждый
// идентификатор стоит обращения к соседу.
//
// Это ВЕРХНЯЯ граница стоимости страницы и та, что действует после каждого
// перезапуска и на всякой странице, которую этот процесс видит впервые.
func BenchmarkPageNarrow_ColdVerdictWindow(b *testing.B) {
	for _, size := range benchSizes {
		b.Run(fmt.Sprintf("page_size=%d", size), func(b *testing.B) {
			rows := page(size)
			ctx := narrowtest.Caller()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Сужатель пересобирается на каждой итерации ИМЕННО ЗАТЕМ, чтобы
				// окно вердиктов оставалось холодным: переиспользуй мы один,
				// вторая итерация мерила бы попадания и число поехало бы вниз,
				// выдавая тёплый путь за холодный.
				b.StopTimer()
				n := benchNarrower(benchPeer{allow: true}, 5*time.Second)
				b.StartTimer()
				out, err := listnarrow.Page(ctx, n, "vpc_network", "list", rows,
					func(r row) string { return r.id })
				if err != nil {
					b.Fatalf("сужение отказало: %v", err)
				}
				if len(out) != size {
					b.Fatalf("сужено до %d строк из %d — замер идёт по неверному пути", len(out), size)
				}
			}
			// Стоимость ОДНОЙ строки — единственное число, сравнимое между
			// размерами страницы: без него рост с 1 до 1000 читается как
			// ухудшение, хотя работы стало ровно в тысячу раз больше.
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/id")
		})
	}
}

// BenchmarkPageNarrow_WarmVerdictWindow — окно вердиктов прогрето: соседа не
// спрашивают вовсе.
//
// Это НИЖНЯЯ граница и то, что видит установившийся поток. Пара с холодным
// замером нужна затем, что правка, ломающая попадание в окно, не меняет ни
// одного исхода и не роняет ни одной пробы: она видна ТОЛЬКО тем, что тёплое
// число становится равно холодному.
func BenchmarkPageNarrow_WarmVerdictWindow(b *testing.B) {
	for _, size := range benchSizes {
		b.Run(fmt.Sprintf("page_size=%d", size), func(b *testing.B) {
			rows := page(size)
			ctx := narrowtest.Caller()
			n := benchNarrower(benchPeer{allow: true}, time.Hour)
			// Прогрев ВНЕ замера: иначе первая итерация оплатила бы обращение к
			// соседу и смешала два разных пути в одно число.
			if _, err := listnarrow.Page(ctx, n, "vpc_network", "list", rows,
				func(r row) string { return r.id }); err != nil {
				b.Fatalf("прогрев отказал: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := listnarrow.Page(ctx, n, "vpc_network", "list", rows,
					func(r row) string { return r.id })
				if err != nil {
					b.Fatalf("сужение отказало: %v", err)
				}
				if len(out) != size {
					b.Fatalf("сужено до %d строк из %d", len(out), size)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/id")
		})
	}
}

// TestPageCostShapeIsLinearInBatchesNotInRows — ФОРМА стоимости страницы,
// утверждённая детерминированно.
//
// # Зачем проба рядом с замером
//
// Замер числа не сторожит: он ничего не роняет, а в конвейере этого дерева
// `-bench` не встречается ни разу (предикат: `grep -rn -- '-bench' .github
// scripts Makefile`). То есть сам по себе он отвечает на вопрос «сколько стоит»
// и не отвечает на «не стало ли вдвое дороже».
//
// Проба утверждает то, что от машины НЕ зависит и потому годится для конвейера:
// число обращений к соседу на страницу. Именно оно и удваивается, когда
// стоимость страницы удваивается по-настоящему — партия нарезана вдвое мельче,
// отношение спрошено дважды, окно вердиктов перестало попадать. Стенные
// миллисекунды порогом здесь быть не могут: на общем ранере они плавают вдвое
// и сами по себе, и первый же ложный срабат такой порог отключит.
func TestPageCostShapeIsLinearInBatchesNotInRows(t *testing.T) {
	t.Parallel()
	ctx := narrowtest.Caller()

	for _, size := range benchSizes {
		t.Run(fmt.Sprintf("page_size=%d", size), func(t *testing.T) {
			t.Parallel()
			peer := &countingPeer{allow: true}
			n := benchNarrower(peer, time.Hour)
			rows := page(size)

			out, err := listnarrow.Page(ctx, n, "vpc_network", "list", rows,
				func(r row) string { return r.id })
			if err != nil {
				t.Fatalf("сужение отказало: %v", err)
			}
			if len(out) != size {
				t.Fatalf("сужено до %d строк из %d", len(out), size)
			}

			// Обращений — по одному на партию, и ни одного сверх: отношение здесь
			// одно, поэтому веер числа обращений не меняет.
			wantCalls := (size + listnarrow.MaxBatchSize - 1) / listnarrow.MaxBatchSize
			if got := int(peer.calls.Load()); got != wantCalls {
				t.Fatalf("обращений к соседу %d, ожидалось %d на %d строк при партии %d: "+
					"стоимость страницы изменила ФОРМУ — партия нарезана иначе либо отношение "+
					"спрошено больше одного раза. Это и есть та регрессия, которую нельзя увидеть "+
					"ни по исходу вызова, ни по стенным миллисекундам на общем ранере",
					got, wantCalls, size, listnarrow.MaxBatchSize)
			}
			// Вопросов — ровно по строке: ни одна не спрошена дважды.
			if got := int(peer.checks.Load()); got != size {
				t.Fatalf("вопросов задано %d на %d строк: строка спрошена больше одного раза, "+
					"то есть стоимость страницы выросла кратно и молча", got, size)
			}

			// Вторая страница тем же сужателем не стоит НИ ОДНОГО обращения:
			// окно вердиктов прогрето. Без этого утверждения правка, ломающая
			// попадание в окно, не роняла бы ничего — все исходы остались бы теми
			// же, изменилась бы только цена.
			before := peer.calls.Load()
			if _, err := listnarrow.Page(ctx, n, "vpc_network", "list", rows,
				func(r row) string { return r.id }); err != nil {
				t.Fatalf("повторное сужение отказало: %v", err)
			}
			if peer.calls.Load() != before {
				t.Fatalf("повторная страница стоила %d обращений вместо нуля: окно вердиктов "+
					"перестало попадать. Ни один исход при этом не меняется — видно только ценой",
					peer.calls.Load()-before)
			}
			t.Logf("перепись: строк %d, обращений к соседу %d, вопросов %d, "+
				"повторная страница обращений 0", size, peer.calls.Load(), peer.checks.Load())
		})
	}
}

// TestPageCostShapeCounterMovesWhenTheVerdictWindowExPIRES — ПОЛОЖИТЕЛЬНЫЙ
// КОНТРОЛЬ к пробе выше.
//
// Её последнее утверждение — «повторная страница стоит ноль обращений» — само по
// себе зеленело бы и на сломанном счётчике, и на соседе, которого не зовут
// вовсе. Здесь та же страница запрашивается ПОСЛЕ истечения окна, и счётчик
// обязан вырасти: значит он считает, а ноль выше означает попадание в окно, а не
// молчание прибора.
//
// # Почему часы, а не «выключить окно»
//
// Выключить его нельзя: `CacheTTL <= 0` означает НЕ «без кеша», а умолчание в
// пять секунд (`pkg/authz.RevocationPolicy.Default`). Первая редакция этого
// контроля исходила из обратного и покраснела — что и есть его работа: она
// опровергла посылку автора, а не свойство кода. Единственный рычаг здесь —
// время, и он же ближе к делу: проба заодно утверждает, что окно ОТЗЫВА
// действительно истекает.
func TestPageCostShapeCounterMovesWhenTheVerdictWindowExpires(t *testing.T) {
	t.Parallel()
	ctx := narrowtest.Caller()
	peer := &countingPeer{allow: true}
	const window = 5 * time.Second
	n, clock := benchNarrowerWithClock(peer, window)
	rows := page(50)

	if _, err := listnarrow.Page(ctx, n, "vpc_network", "list", rows,
		func(r row) string { return r.id }); err != nil {
		t.Fatalf("первая страница: %v", err)
	}
	first := peer.calls.Load()
	if first == 0 {
		t.Fatal("первая страница не стоила ни одного обращения — счётчик не считает")
	}

	clock.advance(window + time.Second)

	if _, err := listnarrow.Page(ctx, n, "vpc_network", "list", rows,
		func(r row) string { return r.id }); err != nil {
		t.Fatalf("вторая страница: %v", err)
	}
	if peer.calls.Load() != 2*first {
		t.Fatalf("после истечения окна вторая страница стоила %d обращений вместо %d: либо "+
			"счётчик не движется — и тогда «ноль обращений» в соседней пробе ничего не утверждает, "+
			"— либо окно вердиктов не истекает, и снятый доступ продолжает проходить дольше "+
			"объявленного", peer.calls.Load()-first, first)
	}
	if peer.checks.Load() != int64(2*len(rows)) {
		t.Fatalf("вопросов задано %d вместо %d", peer.checks.Load(), 2*len(rows))
	}
	t.Logf("перепись контроля: окно %s, страниц 2 по %d строк, обращений %d, вопросов %d",
		window, len(rows), peer.calls.Load(), peer.checks.Load())
}
