// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// watermark_concurrency_internal_test.go — наблюдатель стал ОБЩЕЙ ИЗМЕНЯЕМОЙ
// величиной, и это надо держать пробой (kacho#1374).
//
// # Почему проба здесь, а не у первого потребителя
//
// У механизма подписки наблюдатель свой на каждый поток, и состязания нет. У
// возобновимого чтения, отвечающего на запрос
// (`InternalIAMService.PollSubjectChanges`), экземпляр ОДИН на процесс, а
// проходов столько, сколько реплик потребителя поллит эту реплику владельца.
//
// Поставить эту пробу у потребителя нельзя — ИЗМЕРЕНО, а не предположено: там
// наблюдение идёт через пул соединений, собственный замок пула создаёт связь
// «раньше-позже» между проходами, и детектор состязаний молчит ДАЖЕ ПРИ СНЯТОМ
// замке наблюдателя. То есть интеграционная проба этого свойства не держит: она
// зелена в обоих состояниях. Здесь источник ответов состязаний не заводит, и
// снятие замка находится немедленно.
//
// # Что подаётся вместо базы
//
// Каждый проход отвечает СВОИМ номером — общей величины у источника нет вовсе,
// иначе синхронизация в нём самом закрыла бы ровно тот доступ, ради которого
// проба написана.

// probeRow — ответ наблюдению без единого разделяемого состояния.
type probeRow struct {
	maxSeq  int64
	minSeq  int64
	writers []string
}

func (r probeRow) Scan(dest ...any) error {
	if len(dest) != 3 {
		return fmt.Errorf("наблюдение спросило %d значений, а не 3", len(dest))
	}
	*(dest[0].(*int64)) = r.maxSeq
	*(dest[1].(*int64)) = r.minSeq
	*(dest[2].(*[]string)) = r.writers
	return nil
}

// probeQuerier отвечает величиной, названной ЕГО собственным проходом.
type probeQuerier struct{ row probeRow }

func (q probeQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return q.row }

// TestWatermarkSurvivesConcurrentPasses — проходы, идущие параллельно через один
// наблюдатель, не портят его состояние.
//
// Гоняется под детектором состязаний; её способность падать доказана снятием
// замка из [Watermark.Advance] — тогда детектор находит запись и чтение
// `settled` из разных горутин немедленно.
func TestWatermarkSurvivesConcurrentPasses(t *testing.T) {
	h := newWatermark("probe_journal", "id", slog.Default(), time.Now)

	const passes = 8
	var wg sync.WaitGroup
	for i := 1; i <= passes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := probeQuerier{row: probeRow{maxSeq: int64(i), minSeq: 1}}
			if err := h.Advance(context.Background(), q); err != nil {
				t.Errorf("проход %d: %v", i, err)
			}
			// Читатели идут ТЕМ ЖЕ забегом: состязание бывает не только между
			// двумя записями, но и между записью и чтением, а вызывающий именно
			// читает границу сразу после того, как её двинул.
			_ = h.Settled()
			_ = h.Established()
		}(i)
	}
	wg.Wait()

	// Писателей не было ни в одном наблюдении, значит граница обязана стоять на
	// наибольшем виденном номере, а наблюдение — состояться.
	if got := h.Settled(); got != passes {
		t.Errorf("граница %d, ожидалась %d — параллельные проходы потеряли наблюдение", got, passes)
	}
	if !h.Established() {
		t.Error("наблюдение не состоялось, хотя писателей не было ни в одном проходе")
	}
}
