// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"sync"
	"time"
)

// rateLimiter — token-bucket per-Principal на storm-protection. При flooding
// `GET /vpc/v1/networks/*` от unauthorized user'а positive cache не помогает
// (негативы не кэшируются) → каждый запрос идёт в `kaname.Check` →
// потенциальный DoS на kaname.
//
// Что именно он ограничивает: темп проверок, чей исход кэш НЕ поглощает, — их
// список задаёт вызывающий (Interceptor.authorize), а не этот тип. Разрешение
// кэшируется и потому самоограничено, поэтому верхняя граница на ОБЩИЙ rate
// Check'ов одного subject'а здесь НЕ обещается: её даёт кэш, а бюджет добирает
// ровно то, что кэш добрать не может. По истечении баланса → `ResourceExhausted`
// без обращения в FGA.
//
// Гарантия точная: УСТОЙЧИВЫЙ темп не выше ratePerSec. Мгновенный всплеск может
// превысить burst на число одновременно летящих проверок — см. HasBudget; долг
// при этом НЕ списывается (баланс уходит в минус и отрабатывается пополнением),
// поэтому перерасход возвращается, а не накапливается безнаказанно.
//
// Тhread-safe; eviction inactive subjects через periodic sweep.
type rateLimiter struct {
	mu sync.Mutex

	// ratePerSec — токенов в секунду per subject (например 100).
	// 0 / negative → rate-limit disabled.
	ratePerSec float64
	// burst — burst-size bucket'а (по умолчанию 2x ratePerSec).
	burst float64

	// buckets: subjectID → bucket-state.
	buckets map[string]*bucket

	// maxBuckets — жёсткий внутренний потолок числа bucket'ов (CWE-770): память
	// ограничена даже если composition root не расписал периодический
	// EvictInactive-sweep или под churn'ом уникальных principal-id (id
	// пере-трогаются быстрее maxAge). Зеркалит Cache.maxEntries.
	maxBuckets int

	now func() time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// defaultMaxBuckets — потолок числа rate-limiter bucket'ов по умолчанию
// (CWE-770). Один bucket ≈ несколько десятков байт → 100k ≈ единицы МБ.
const defaultMaxBuckets = 100_000

// newRateLimiter создает лимитер. ratePerSec ≤ 0 → disabled (бюджет всегда есть).
func newRateLimiter(ratePerSec float64) *rateLimiter {
	return newRateLimiterWithLimit(ratePerSec, defaultMaxBuckets)
}

// newRateLimiterWithLimit — как newRateLimiter, но с явным потолком bucket'ов.
// maxBuckets ≤ 0 → defaultMaxBuckets.
func newRateLimiterWithLimit(ratePerSec float64, maxBuckets int) *rateLimiter {
	if ratePerSec < 0 {
		ratePerSec = 0
	}
	if maxBuckets <= 0 {
		maxBuckets = defaultMaxBuckets
	}
	return &rateLimiter{
		ratePerSec: ratePerSec,
		burst:      ratePerSec * 2,
		buckets:    make(map[string]*bucket, 64),
		maxBuckets: maxBuckets,
		now:        time.Now,
	}
}

// HasBudget сообщает, осталось ли у subjectID право вызвать ещё одну Check'у,
// НЕ расходуя бюджет. Тратит его Charge — и только на исход, который кэш не
// поглотит. Если rate-limit disabled (ratePerSec ≤ 0) — всегда true.
//
// Почему проверка и трата разнесены: спросить бюджет нужно ДО обращения к
// модели прав (отсечка, срабатывающая после вопроса, ничего не защищает), а
// списать — только ПОСЛЕ ответа (аутентифицированный вызывающий не должен
// платить за то, что ему разрешено). Два шага не атомарны, поэтому под
// конкуренцией одновременно «в полёте» может оказаться чуть больше `burst`
// вопросов — величина ограничена параллелизмом сервера, а не вызывающим. На
// УСТОЙЧИВЫЙ темп это не влияет ровно потому, что Charge не обрезает долг: весь
// перерасход списывается и отрабатывается пополнением (см. Charge).
//
// Реализация — стандартный token-bucket: refill по elapsed-time × rate.
func (rl *rateLimiter) HasBudget(subjectID string) bool {
	if rl.ratePerSec <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.refillLocked(subjectID).tokens >= 1.0
}

// Charge списывает один токен с бюджета subjectID. Вызывается ТОЛЬКО для
// проверки, чей исход кэш не поглощает (см. Interceptor.authorize): повторяемые
// отказы идут в модель каждый раз, и именно их темп бюджет ограничивает.
// Разрешение кэшируется и потому самоограничено — оно не платит.
func (rl *rateLimiter) Charge(subjectID string) {
	if rl.ratePerSec <= 0 {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	// Баланс НАМЕРЕННО уходит в минус. Обрезать его нулём нельзя: HasBudget и
	// Charge разнесены во времени, поэтому K одновременных проверок одного
	// принципала все увидят положительный баланс и все пройдут. Если долг
	// обрезать, K списаний схлопнутся в одно, и на каждый пополненный токен
	// проходило бы K проверок — устойчивый темп стал бы ratePerSec × параллелизм
	// вместо ratePerSec. С отрицательным балансом перерасход отрабатывается
	// пополнением, и превышение остаётся РАЗОВЫМ всплеском, а не множителем.
	//
	// Минус ограничен снизу теми же K (больше списаний, чем было одновременных
	// проверок, произойти не может), поэтому «долговая яма» не растёт без предела.
	rl.refillLocked(subjectID).tokens -= 1.0
}

// refillLocked возвращает bucket subject'а, пополнив его по прошедшему времени
// (создав при первом обращении). Вызывается под rl.mu.
func (rl *rateLimiter) refillLocked(subjectID string) *bucket {
	now := rl.now()
	b, exists := rl.buckets[subjectID]
	if !exists {
		// Новый subject — начинаем с full burst. Перед вставкой держим потолок:
		// при достижении maxBuckets освобождаем место (CWE-770).
		if len(rl.buckets) >= rl.maxBuckets {
			rl.evictForInsertLocked()
		}
		b = &bucket{tokens: rl.burst, lastSeen: now}
		rl.buckets[subjectID] = b
		return b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * rl.ratePerSec
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastSeen = now
	return b
}

// evictForInsertLocked освобождает место в buckets при достижении потолка
// maxBuckets. Вызывается под rl.mu. Стратегия (зеркалит Cache.evictLocked):
// сначала выбрасывает полностью пополненные (idle) bucket'ы — их удаление
// поведенчески нейтрально (следующее обращение создаёт такой же full-burst bucket);
// если и после этого полно — выбрасывает произвольные до low-water (7/8).
// Худший эффект произвольной эвикции — сброс частично израсходованного bucket'а
// в full burst (кратковременно ослабляет лимит для ОДНОГО subject'а под
// экстремальным churn'ом), что приемлемо ради жёсткого потолка памяти.
func (rl *rateLimiter) evictForInsertLocked() {
	for s, b := range rl.buckets {
		if b.tokens >= rl.burst {
			delete(rl.buckets, s)
		}
	}
	if len(rl.buckets) < rl.maxBuckets {
		return
	}
	target := rl.maxBuckets - rl.maxBuckets/8
	if target < 0 {
		target = 0
	}
	for s := range rl.buckets {
		if len(rl.buckets) <= target {
			break
		}
		delete(rl.buckets, s)
	}
}

// EvictInactive удаляет subject-bucket'ы, у которых lastSeen старше maxAge.
// Вызывается из background-loop'а раз в minуту, чтобы избежать unbounded
// memory-growth при большом subject-vocabulary.
func (rl *rateLimiter) EvictInactive(maxAge time.Duration) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := rl.now().Add(-maxAge)
	removed := 0
	for s, b := range rl.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(rl.buckets, s)
			removed++
		}
	}
	return removed
}
