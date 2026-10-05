// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"sync"
	"time"
)

// Ограничение частоты строки «неразмеченный RPC» (corelib#91).
//
// Источник таких вызовов почти всегда периодический — проба, опрос, повтор
// клиента, — и без ограничения журнал сервиса заполняется одинаковыми
// строками (на стенде 4320 строк за 6 ч на сервис от одной пробы), в которых
// тонет всё остальное. Ограничивается СТРОКА, а не решение и не учёт: каждый
// вызов по-прежнему отвергается и попадает в `Metrics.Unmapped`.
const (
	// unmappedLogWindow — окно, в котором на один ключ пишется одна строка.
	// Минута: строк на ключ в час не больше 60, а новая находка видна в
	// журнале не позже чем через минуту после того, как появилась.
	unmappedLogWindow = time.Minute

	// unmappedLogMaxKeys — потолок числа ключей (CWE-770). Ключ — полное имя
	// метода, а методы, до которых доходит звено, — это методы, которые
	// сервер служит (неизвестный метод grpc-go отвергает до цепочки звеньев),
	// то есть их единицы-десятки. Потолок держит память и тогда, когда это
	// предположение однажды перестанет быть верным.
	unmappedLogMaxKeys = 1024

	// unmappedLogOverflowKey — общее окно для вызовов сверх потолка: строки
	// по-прежнему ограничены, а не пишутся на каждый вызов.
	unmappedLogOverflowKey = "\x00overflow"

	// unmappedReason — причина строки. Сейчас она у неразмеченного вызова
	// одна, но входит в ключ: другая причина того же метода обязана получить
	// свою первую строку, а не утонуть в чужом окне.
	unmappedReason = "rpc_not_mapped"
)

// logWindow — «первая строка в окне и счётчик подавленных» на ключ.
//
// Подавленное не исчезает молча: первая строка СЛЕДУЮЩЕГО окна того же ключа
// несёт число строк, не записанных в предыдущем. Окно, в котором ничего не
// подавлено, счётчика не печатает — ноль не притворяется «не считали».
type logWindow struct {
	mu      sync.Mutex
	window  time.Duration
	maxKeys int
	slots   map[string]*logSlot
}

type logSlot struct {
	opened     time.Time
	suppressed uint64
}

func newLogWindow(window time.Duration, maxKeys int) *logWindow {
	return &logWindow{
		window:  window,
		maxKeys: maxKeys,
		slots:   make(map[string]*logSlot),
	}
}

// admit решает, писать ли строку для key в момент now. Если писать — второе
// значение равно числу строк, подавленных в предыдущем окне этого ключа.
func (w *logWindow) admit(key string, now time.Time) (bool, uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	slot, ok := w.slots[key]
	if !ok {
		if len(w.slots) >= w.maxKeys {
			w.sweepLocked(now)
		}
		if len(w.slots) >= w.maxKeys {
			key = unmappedLogOverflowKey
			slot = w.slots[key]
		}
		if slot == nil {
			w.slots[key] = &logSlot{opened: now}
			return true, 0
		}
	}
	if now.Sub(slot.opened) < w.window {
		slot.suppressed++
		return false, 0
	}
	suppressed := slot.suppressed
	slot.opened = now
	slot.suppressed = 0
	return true, suppressed
}

// sweepLocked снимает ключи, чьё окно истекло и в котором ничего не
// подавлено: их снятие поведенчески нейтрально — следующий вызов откроет такое
// же окно. Ключ с подавленными остаётся, иначе его счётчик пропал бы молча.
func (w *logWindow) sweepLocked(now time.Time) {
	for k, s := range w.slots {
		if k != unmappedLogOverflowKey && s.suppressed == 0 && now.Sub(s.opened) >= w.window {
			delete(w.slots, k)
		}
	}
}

func (w *logWindow) size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.slots)
}
