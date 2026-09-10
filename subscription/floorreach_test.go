// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// floorreach_test.go — НИЖНЯЯ ВОЗОБНОВИМАЯ ПОЗИЦИЯ СПРАШИВАЕТСЯ ИЗВНЕ ПАКЕТА.
//
// # Зачем проба, утверждающая «метод экспортирован»
//
// Пол — половина ответа на «обнаружил ли читатель пропуск». Вторую половину
// (явный отказ) даёт тот, кто пол спросил; спросить его может только тот, кому
// он виден. Пока метод был неэкспортирован, ЕДИНСТВЕННЫМ владельцем полосы
// обнаружения пропуска оставался механизм подписки — а тот же класс живёт у
// журнала смены субъекта, читаемого унарным глаголом, и там пол был невыразим
// by construction.
//
// Проба живёт в ВНЕШНЕМ пакете (`subscription_test`) намеренно: внутренняя
// зеленела бы и на неэкспортированном имени, то есть утверждала бы о значении, а
// не о досягаемости. Ровно это и есть её предмет.
//
// # Почему обе полосы удержания, а не одна
//
// Односторонняя проба зеленела бы на реализации, отдающей ноль всегда: у
// холодного наблюдателя оба ответа равны нулю, и различить их нечем. Поэтому
// нижняя строка ПОДАЁТСЯ (`RefreshEarliest` через подставной источник), и
// утверждаются ОБА ответа — «удерживаю всё» отдаёт ноль, «чищу с начала» отдаёт
// «самая ранняя минус один».
package subscription_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/subscription"
)

// earliestRow — ответ подставного источника на единственный вопрос
// [subscription.Watermark.RefreshEarliest]: номер самой ранней удержанной строки.
type earliestRow struct{ value int64 }

func (r earliestRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return pgx.ErrNoRows
	}
	target, ok := dest[0].(*int64)
	if !ok {
		return pgx.ErrNoRows
	}
	*target = r.value
	return nil
}

// earliestSource — [subscription.Querier], отвечающий заготовленной нижней
// строкой. Настоящего соединения здесь не нужно: предмет пробы — досягаемость
// пола и его формула, а не разбор SQL.
type earliestSource struct{ value int64 }

func (s earliestSource) QueryRow(context.Context, string, ...any) pgx.Row {
	return earliestRow{value: s.value}
}

// TestFloorIsAskedFromOutsideTheSubscriptionPackage — пол досягаем извне и
// различает ДВЕ объявленные полосы удержания.
func TestFloorIsAskedFromOutsideTheSubscriptionPackage(t *testing.T) {
	ctx := context.Background()
	h := subscription.NewWatermark("kaname.subject_change_outbox", "id", nil)

	// Холодный наблюдатель: нижней строки ещё не спрашивали, обе полосы дают
	// ноль. Утверждается, чтобы следующий шаг был ОТЛИЧИМ от этого состояния.
	if got := h.Floor(subscription.RetainsEverything); got != 0 {
		t.Fatalf("холодный наблюдатель, удержание целиком: пол %d, ожидалось 0", got)
	}
	if got := h.Floor(subscription.RetainsFromEarliestRow); got != 0 {
		t.Fatalf("холодный наблюдатель, чистящий журнал: пол %d, ожидалось 0", got)
	}

	// Нижняя строка ПОДАНА: журнал вычищен по 599-ю включительно.
	if err := h.RefreshEarliest(ctx, earliestSource{value: 600}); err != nil {
		t.Fatalf("перечитать нижнюю удержанную строку: %v", err)
	}

	// Владелец, удерживающий журнал целиком, нижней границы НЕ ИМЕЕТ — и это
	// положительный контроль к строке ниже: без него проба зеленела бы на
	// реализации, отдающей «самая ранняя минус один» независимо от объявления.
	if got := h.Floor(subscription.RetainsEverything); got != 0 {
		t.Fatalf("удержание целиком: пол %d, ожидалось 0 — нижней границы у такого журнала не существует", got)
	}
	// Чистящий владелец: с 599 возобновление ещё не теряет ничего.
	if got := h.Floor(subscription.RetainsFromEarliestRow); got != 599 {
		t.Fatalf("чистящий журнал: пол %d, ожидалось 599 (самая ранняя 600 минус один)", got)
	}
}
