// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"testing"
	"time"
)

// TestBackoffDoublesAndStopsAtTheCap — пауза перед повтором УДВАИВАЕТСЯ и
// упирается в потолок.
//
// # Почему это надо утверждать отдельно
//
// На разрежённых повторах держится вся терпимость замысла к отказавшему
// приёмнику: без роста отказавший приёмник получает шквал ровно тогда, когда ему
// хуже всего, а без потолка строка, которую примут через минуту, ждёт сутки.
// Единственное утверждение, которое было у этой оси, — «время следующей попытки
// в будущем»; оно зелёное и при ПОСТОЯННОЙ паузе, то есть об оси не говорит
// ничего.
//
// Проба закрепляет ПОСЛЕДОВАТЕЛЬНОСТЬ целиком, а не отдельные точки: удвоение
// без потолка и потолок без удвоения одинаково проходят точечную проверку.
func TestBackoffDoublesAndStopsAtTheCap(t *testing.T) {
	sh := &Shipper{cfg: ShipperConfig{
		BackoffMin: time.Second,
		BackoffMax: 8 * time.Second,
	}}

	want := []time.Duration{
		1 * time.Second, // первая попытка
		2 * time.Second,
		4 * time.Second,
		8 * time.Second, // потолок достигнут
		8 * time.Second, // и дальше держится
		8 * time.Second,
		8 * time.Second,
	}
	for i, w := range want {
		attempts := i + 1
		if got := sh.backoff(attempts); got != w {
			t.Errorf("попытка %d: пауза %v, ожидалось %v", attempts, got, w)
		}
	}

	// Нижняя граница оси: нулевая и отрицательная попытка читаются как первая,
	// а не как «паузы нет» — иначе сбитый счётчик снимал бы разрежение вовсе.
	for _, attempts := range []int{0, -1, -100} {
		if got := sh.backoff(attempts); got != time.Second {
			t.Errorf("попытка %d: пауза %v, ожидалась минимальная %v",
				attempts, got, time.Second)
		}
	}

	// Потолок ниже минимума — вырожденная, но выразимая настройка: пауза обязана
	// остаться потолком, а не уехать вниз или в ноль.
	odd := &Shipper{cfg: ShipperConfig{BackoffMin: 10 * time.Second, BackoffMax: time.Second}}
	if got := odd.backoff(1); got != time.Second {
		t.Errorf("потолок ниже минимума: пауза %v, ожидался потолок %v", got, time.Second)
	}
}
