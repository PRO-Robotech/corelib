// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// poolclose_test.go — предмет: закрытие пула, которому некого дождаться, обязано
// СДАТЬСЯ и НАЗВАТЬ причину, а не висеть до предела прогона.
//
// # Почему это проверяется поведением, а не чтением кода
//
// Отказ базы внутри открытой транзакции завершает горутину пробы (`FailNow` →
// `runtime.Goexit`). Транзакция остаётся открытой, её соединение — не возвращённым
// в пул, а `pgxpool.Pool.Close` ждёт возврата ВСЕХ выданных соединений. Ждать ему
// нечего: писателя уже нет. Пакет упирается в `-timeout` и печатает `FAIL` — то
// есть «не выполнилось» приходит к читателю под видом красного вердикта.
//
// Проба сама держит свой предел ожидания, поэтому на неисправной реализации она
// ПАДАЕТ, а не виснет: иначе она воспроизводила бы ровно тот дефект, который ловит.
//
// Пакет внутренний (`package pgtest`) намеренно: предмет — `closePoolWithin`, а
// `testing.TB` снаружи не подделать (у интерфейса есть неэкспортируемый метод),
// поэтому утверждать про ТЕКСТ отказа можно только здесь.
package pgtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// probeBound — предел, который проба даёт закрытию. Мал намеренно: проба обязана
// быть быстрой и на исправной реализации, и на неисправной.
const probeBound = 2 * time.Second

// probeMargin — сколько проба ждёт СВЕРХ предела, прежде чем объявить, что
// закрытие не сдалось вовсе. Без запаса красное на медленной машине было бы
// неотличимо от красного на дефекте.
const probeMargin = 8 * time.Second

// openPool — пул на собственной базе этой пробы.
func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), NewDB(t))
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	return pool
}

// closeWithinProbeBudget зовёт closePoolWithin и НЕ даёт себе повиснуть.
// Второе значение — успело ли закрытие вернуть управление вообще.
func closeWithinProbeBudget(t *testing.T, pool *pgxpool.Pool) (string, bool) {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- closePoolWithin(pool, probeBound) }()
	select {
	case msg := <-done:
		return msg, true
	case <-time.After(probeBound + probeMargin):
		return "", false
	}
}

// TestPoolCloseGivesUpAndNamesTheCause — предмет и законный близнец рядом.
//
// Близнец обязателен: без него проба зеленела бы на реализации, которая ВСЕГДА
// докладывает о зависании, — то есть на проверке, отвергающей и исправный случай.
func TestPoolCloseGivesUpAndNamesTheCause(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	t.Run("утёкшее соединение: закрытие сдаётся и называет причину", func(t *testing.T) {
		pool := openPool(t)

		// Ровно то, что оставляет за собой проба, упавшая внутри открытой
		// транзакции: соединение выдано и никогда не будет возвращено.
		leaked, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer leaked.Release() // после утверждений — иначе база не дропнется

		started := time.Now()
		msg, returned := closeWithinProbeBudget(t, pool)
		if !returned {
			t.Fatalf("closePoolWithin не вернуло управление за %s.\n"+
				"Это и есть предмет: закрытие пула ждёт соединение, которое никто не вернёт,\n"+
				"и уносит с собой весь пакет — читатель получает предел прогона вместо причины.",
				probeBound+probeMargin)
		}
		if elapsed := time.Since(started); elapsed > probeBound+probeMargin {
			t.Errorf("закрытие сдалось за %s при пределе %s", elapsed, probeBound)
		}
		if msg == "" {
			t.Fatalf("закрытие вернулось молча — читатель не узнает, ЧТО именно осталось открытым")
		}
		// Текст обязан вести к действию: назвать предмет и назвать снятие.
		for _, want := range []string{"Abort", "Writer", "acquired"} {
			if !strings.Contains(msg, want) {
				t.Errorf("в тексте отказа нет %q — по нему не починить:\n%s", want, msg)
			}
		}
	})

	t.Run("законный пул: закрытие проходит и МОЛЧИТ", func(t *testing.T) {
		pool := openPool(t)
		// Соединение берётся и ВОЗВРАЩАЕТСЯ — та же форма, законный исход.
		c, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		c.Release()

		msg, returned := closeWithinProbeBudget(t, pool)
		if !returned {
			t.Fatalf("исправное закрытие не вернуло управление за %s", probeBound+probeMargin)
		}
		if msg != "" {
			t.Errorf("закрытие исправного пула доложило о зависании — проверка отвергает законный случай:\n%s", msg)
		}
	})
}
