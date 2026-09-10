// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// spend моделирует один un-absorbed check: спросить бюджет и, если он есть,
// списать. Ровно эту пару делает Interceptor.authorize (HasBudget до обращения к
// модели прав, Charge — после ответа, который кэш не поглотит). Существующие
// утверждения ниже описывают именно эту суммарную семантику.
func spend(rl *rateLimiter, subjectID string) bool {
	if !rl.HasBudget(subjectID) {
		return false
	}
	rl.Charge(subjectID)
	return true
}

// clock — управляемый источник времени для детерминированного теста refill.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

// TestRateLimiter_Disabled — ratePerSec ≤ 0 → бюджет всегда есть (limiter off).
func TestRateLimiter_Disabled(t *testing.T) {
	for _, rate := range []float64{0, -1} {
		rl := newRateLimiter(rate)
		for i := 0; i < 1000; i++ {
			if !spend(rl, "usr_x") {
				t.Fatalf("ratePerSec=%v: budget must always be available when disabled (refused at i=%d)", rate, i)
			}
		}
	}
}

// TestRateLimiter_BurstThenExhaust — свежий subject стартует с full burst
// (2×rate); после исчерпания burst без refill'а → deny.
func TestRateLimiter_BurstThenExhaust(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10) // burst = 20
	rl.now = clk.now

	allowed := 0
	for i := 0; i < 100; i++ {
		if spend(rl, "usr_x") {
			allowed++
		}
	}
	// Без продвижения времени refill не происходит → ровно burst разрешений.
	if allowed != 20 {
		t.Fatalf("expected exactly burst=20 allowed before refill, got %d", allowed)
	}
}

// TestRateLimiter_RefillOverTime — по истечении времени токены пополняются
// elapsed×rate; проверяем что после исчерпания и паузы снова можно.
func TestRateLimiter_RefillOverTime(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10) // rate=10/s, burst=20
	rl.now = clk.now

	// Исчерпываем burst.
	for i := 0; i < 20; i++ {
		if !spend(rl, "usr_x") {
			t.Fatalf("burst not fully consumed at i=%d", i)
		}
	}
	if spend(rl, "usr_x") {
		t.Fatalf("expected deny immediately after burst exhausted")
	}

	// Проходит 0.5s → refill = 0.5×10 = 5 токенов.
	clk.advance(500 * time.Millisecond)
	got := 0
	for i := 0; i < 10; i++ {
		if spend(rl, "usr_x") {
			got++
		}
	}
	if got != 5 {
		t.Fatalf("expected 5 tokens refilled after 0.5s, got %d", got)
	}
}

// TestRateLimiter_RefillCapsAtBurst — длительная пауза не даёт токенам
// превысить burst (иначе DoS-cap обходится).
func TestRateLimiter_RefillCapsAtBurst(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10) // burst = 20
	rl.now = clk.now

	// Один вызов создаёт bucket (tokens=20 → 19).
	spend(rl, "usr_x")
	// Огромная пауза: refill = 3600×10 = 36000, но cap = burst = 20.
	clk.advance(time.Hour)
	got := 0
	for i := 0; i < 100; i++ {
		if spend(rl, "usr_x") {
			got++
		}
	}
	if got != 20 {
		t.Fatalf("expected refill capped at burst=20, got %d", got)
	}
}

// TestRateLimiter_HardCapBoundsBuckets — buckets map имеет собственный жёсткий
// потолок (CWE-770): при churn'е из множества уникальных principal-id map НЕ
// растёт неограниченно даже без внешнего EvictInactive-sweep'а.
func TestRateLimiter_HardCapBoundsBuckets(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiterWithLimit(10, 100) // cap = 100 bucket'ов
	rl.now = clk.now

	// 10_000 уникальных subject'ов — на порядки больше потолка. Без внутренней
	// границы map вырос бы до 10k (OOM-вектор).
	for i := 0; i < 10_000; i++ {
		spend(rl, fmt.Sprintf("usr_%d", i))
		if len(rl.buckets) > 100 {
			t.Fatalf("buckets exceeded hard cap: got %d at i=%d", len(rl.buckets), i)
		}
	}
	if len(rl.buckets) == 0 {
		t.Fatalf("expected some live buckets, got 0")
	}
}

// TestRateLimiter_DefaultCapPresent — конструктор по умолчанию ставит непустой
// внутренний потолок (не 0 = unbounded).
func TestRateLimiter_DefaultCapPresent(t *testing.T) {
	rl := newRateLimiter(10)
	if rl.maxBuckets <= 0 {
		t.Fatalf("default rate limiter must carry a positive maxBuckets, got %d", rl.maxBuckets)
	}
}

// TestRateLimiter_EvictInactive — удаляет только bucket'ы старше maxAge и
// возвращает корректный removed-count.
func TestRateLimiter_EvictInactive(t *testing.T) {
	clk := &clock{t: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)}
	rl := newRateLimiter(10)
	rl.now = clk.now

	// Старый subject: lastSeen = t0.
	spend(rl, "usr_old")
	// Проходит 2 минуты.
	clk.advance(2 * time.Minute)
	// Свежий subject: lastSeen = t0+2m.
	spend(rl, "usr_fresh")

	// Evict всё, что старше 1 минуты → должен уйти только usr_old.
	removed := rl.EvictInactive(1 * time.Minute)
	if removed != 1 {
		t.Fatalf("expected 1 bucket evicted, got %d", removed)
	}
	if _, ok := rl.buckets["usr_old"]; ok {
		t.Fatalf("usr_old bucket must be evicted")
	}
	if _, ok := rl.buckets["usr_fresh"]; !ok {
		t.Fatalf("usr_fresh bucket must survive")
	}
}

// TestRateLimiter_Concurrent — data-race guard: rateLimiter documented Thread-safe,
// но до этого теста ни один кейс не гонял HasBudget/Charge/EvictInactive из нескольких
// goroutine (в отличие от Cache, у которого есть TestCache_Concurrent). Спавним N
// goroutine, бьющих бюджет по пересекающимся И уникальным subject-id, пока ещё одна
// goroutine периодически зовёт EvictInactive — весь map/bucket-mutation-путь под
// rl.mu. Прогоняется под -race; падает (concurrent map write / detected race), если
// будущая оптимизация сузит или уберёт lock в HasBudget/Charge или eviction-sweep.
func TestRateLimiter_Concurrent(t *testing.T) {
	rl := newRateLimiter(1000) // положительный rate → бюджет идёт по locked-пути
	const goroutines = 32
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	// Writer-heavy: расход по overlapping (usr_shared_%2) и уникальным (usr_g%d_i%d)
	// subject'ам — вставка новых bucket'ов конкурирует с eviction-sweep'ом.
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if i%2 == 0 {
					spend(rl, fmt.Sprintf("usr_shared_%d", i%2))
				} else {
					spend(rl, fmt.Sprintf("usr_g%d_i%d", g, i))
				}
			}
		}(g)
	}

	// Конкурентный eviction-sweep на том же mutation-пути.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			rl.EvictInactive(time.Nanosecond)
		}
	}()

	wg.Wait()

	// Sanity: лимитер работоспособен после конкурентной нагрузки.
	if !spend(rl, "usr_after_load") {
		t.Fatalf("rate limiter must admit a fresh subject after concurrent load")
	}
}

// TestRateLimiter_ConcurrentOverdraftIsRepaidNotForgiven — the budget is asked
// before the permission model answers and spent after, so the two steps cannot be
// atomic: every check already in flight has seen a positive balance. That is
// allowed to inflate ONE burst. It must not multiply the SUSTAINED rate, and the
// only thing standing between the two is whether the overdraft is remembered.
//
// If Charge clamped the balance at zero, K concurrent charges would collapse into
// a single token, so each refilled token would admit K more checks — the ceiling
// would become ratePerSec × concurrency instead of ratePerSec. Carrying the debt
// makes refill repay it first.
//
// The interleaving is modelled by ORDER rather than goroutines (every in-flight
// check asks, then every one of them charges) because the difference is only
// observable across refill, and refill needs the injectable clock.
func TestRateLimiter_ConcurrentOverdraftIsRepaidNotForgiven(t *testing.T) {
	const (
		rate     = 10 // burst = 20
		inFlight = 30 // concurrency above the burst — the overdraft is 10
	)
	rl := newRateLimiter(rate)
	c := &clock{t: time.Now()}
	rl.now = c.now

	for i := 0; i < inFlight; i++ {
		if !rl.HasBudget("usr_x") {
			t.Fatalf("check %d of %d: all in-flight checks observe the balance before any is spent", i+1, inFlight)
		}
	}
	for i := 0; i < inFlight; i++ {
		rl.Charge("usr_x")
	}

	// One second refills exactly `rate` tokens — precisely the size of the debt.
	// It must go to repaying it, not to admitting new checks.
	c.advance(time.Second)
	if rl.HasBudget("usr_x") {
		t.Fatalf("refill admitted a check while the overdraft of %d was still outstanding: "+
			"the debt was discarded, so concurrency multiplies the sustained rate", inFlight-2*rate)
	}

	// A second refill clears the debt and the limiter resumes at its normal rate.
	c.advance(time.Second)
	if !rl.HasBudget("usr_x") {
		t.Fatalf("limiter must resume once the overdraft has been repaid")
	}
}
