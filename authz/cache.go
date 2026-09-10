// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"sync"
	"sync/atomic"
	"time"
)

// Cache хранит positive Check-results; срок жизни записи объявлен политикой
// (RevocationPolicy, revocation_policy.go), умолчание — 5s.
//
// Семантика:
//   - Кешируются ТОЛЬКО `allowed=true` (positive results).
//   - Negative (deny) НЕ кешируются — иначе grant binding'а не проявится до
//     истечения TTL → расходится с UX «дал права — почему не работает?».
//   - Отсюда асимметрия: ВЫДАЧА видна сразу, ОТЗЫВ ждёт истечения записи.
//     Срок жизни записи и есть окно отзыва — см. RevocationPolicy, где оно
//     объявлено числом с обоснованием, и гейт
//     `internal/repohygiene.TestRevocationWindowIsDeclaredPolicy`, который
//     держит объявление и дерево в согласии.
//   - Проактивного снятия записи у backend-сервиса НЕТ. Здесь стояло
//     утверждение, что отзыв прилетает по `pg_notify('kacho_iam_subjects')` в
//     `InvalidateBySubject`; в этом репозитории у канала нет НИ ОДНОГО
//     отправителя (при database-per-service его и не может быть — сигнал шёл бы
//     из БД iam, к которой у backend-сервиса нет доступа). Механизм
//     `ListenInvalidator` остаётся пригодным, но пока по каналу никто не пишет,
//     единственный путь снятия записи — истечение срока.
//
// Thread-safe: используется из нескольких gRPC-handler goroutines одновременно.
type Cache struct {
	mu  sync.RWMutex
	ttl time.Duration

	// store: ключ = subjectID, значение = map[entryKey]entry.
	// Двухуровневый dict позволяет O(1) invalidateBySubject(subjectID) —
	// просто `delete(c.store, subjectID)`.
	store map[string]map[entryKey]entry

	// count — точное текущее число entry во всех subject-bucket'ах. Поддерживается
	// инкрементально на insert/delete, чтобы решение об эвикции по потолку было O(1).
	count int

	// maxEntries — жёсткий потолок числа entry (CWE-770 защита от unbounded roста).
	maxEntries int

	// now — функция текущего времени, переопределяема в тестах.
	now func() time.Time

	// Величины окна вердиктов. Атомарные и вне `mu` намеренно: `Get` читает под
	// RLock, и поднимать замок до write ради счётчика значило бы сериализовать
	// самый горячий путь процесса ради наблюдения за ним.
	//
	// Учёт ЗДЕСЬ, а не у звена, потому что учитывать одну величину в двух местах
	// значит завести два числа, которые разъедутся молча. Кеш — единственный, кто
	// видит все свои события: у звена нет ни истечения записи, ни давления
	// потолка, ни снятия.
	hits            atomic.Uint64
	misses          atomic.Uint64
	evictedExpired  atomic.Uint64
	evictedCapacity atomic.Uint64
	invalidated     atomic.Uint64
}

// CacheStats — ПРОЧИТАННЫЕ величины окна вердиктов.
//
// Именно прочитанные: по отсутствию строки на поверхности «события не было» и
// «счётчика нет» неразличимы, а прочитанный ноль их различает.
//
// # Зачем каждая, и почему трёх из пяти не хватает
//
// `Hits` без `Misses` не даёт доли: у неё нет знаменателя, и «попаданий много»
// одинаково верно при кеше, поглощающем весь поток, и при кеше, мимо которого
// идёт вдесятеро больше. `Entries` объясняет, ПОЧЕМУ доля такая, а три причины
// вытеснения объясняют, почему она упала, — и сводить их в одну нельзя:
// истечение окна есть штатная работа, давление потолка есть сигнал, что кеша не
// хватает на нагрузку, а снятие есть единственный проактивный путь. Сложенные,
// они объявили бы исчерпание потолка нормой.
type CacheStats struct {
	// Hits / Misses — исходы обращений к окну. Их сумма и есть число заданных
	// окну вопросов; доля попаданий считается ПОТРЕБИТЕЛЕМ, а не здесь: доля,
	// посчитанная в процессе за всё время жизни, не дифференцируется по времени и
	// не складывается по репликам.
	Hits   uint64
	Misses uint64

	// Subjects / Entries — текущий размер: субъектов и записей.
	Subjects int
	Entries  int

	// EvictedExpired — записи, снятые ПО ИСТЕЧЕНИИ окна (лениво на чтении и
	// подметанием перед вставкой). Штатная работа.
	EvictedExpired uint64
	// EvictedCapacity — записи, снятые ДАВЛЕНИЕМ ПОТОЛКА, то есть ещё живые.
	// Каждая такая — попадание, которого не будет: ненулевое значение означает,
	// что потолок мал для нагрузки, и доля попаданий упирается в него, а не в
	// окно.
	EvictedCapacity uint64
	// Invalidated — записи, снятые ЯВНО (по субъекту либо целиком). Единственный
	// проактивный путь снятия; ноль здесь означает, что окно отзыва целиком
	// определяется истечением.
	Invalidated uint64
}

// Stats — снимок величин окна вердиктов.
func (c *Cache) Stats() CacheStats {
	subjects, entries := c.Size()
	return CacheStats{
		Hits:            c.hits.Load(),
		Misses:          c.misses.Load(),
		Subjects:        subjects,
		Entries:         entries,
		EvictedExpired:  c.evictedExpired.Load(),
		EvictedCapacity: c.evictedCapacity.Load(),
		Invalidated:     c.invalidated.Load(),
	}
}

// entryKey — composite-ключ (relation, object_type, object_id).
// subjectID — внешний уровень map.
type entryKey struct {
	relation   string
	objectType string
	objectID   string
}

// entry — кешируемое значение. Кешируются только positive-результаты (negative
// не кешируется, см. package-doc), поэтому «разрешено» — структурный инвариант:
// сам факт живой entry означает allowed=true. Отдельного allowed-поля нет —
// иначе SetDenied-путь мог бы записать allowed=false и молча вернуть negative-
// кеширование, запрещённое контрактом пакета.
type entry struct {
	expiresAt time.Time // unix-time истечения
}

// defaultMaxEntries — верхняя граница числа кешируемых entry по умолчанию
// (защита от неограниченного роста map при enumeration-нагрузке, CWE-770).
// Один entry ≈ несколько десятков байт → 100k ≈ единицы МБ, потолок памяти жёсткий.
const defaultMaxEntries = 100_000

// NewCache создает кеш с указанным TTL. ttl ≤ 0 → defaults to 5*time.Second.
// Число entry ограничено defaultMaxEntries (см. NewCacheWithLimit).
func NewCache(ttl time.Duration) *Cache {
	return NewCacheWithLimit(ttl, defaultMaxEntries)
}

// NewCacheWithLimit создает кеш с указанным TTL и жёстким потолком числа entry.
// ttl ≤ 0 → 5s; maxEntries ≤ 0 → defaultMaxEntries. При достижении потолка insert
// нового ключа сперва вычищает просроченные записи, а если и после этого кеш полон —
// эвиктит произвольные entry до low-water (см. evictLocked). Cache-miss всегда
// безопасен (fallback на авторитетный Check), поэтому произвольная эвикция не влияет
// на корректность авторизации — только на hit-rate.
func NewCacheWithLimit(ttl time.Duration, maxEntries int) *Cache {
	if ttl <= 0 {
		// Умолчание берётся из ОБЪЯВЛЕННОЙ политики, а не из литерала здесь:
		// три сервиса (compute, geo, storage) строят кеш как NewCache(0) и
		// своего числа не имеют вовсе, поэтому именно это значение и есть их
		// окно отзыва. Пока оно было безымянной константой в этой строке, окно
		// трёх сервисов не было записано нигде.
		ttl = RevocationPolicy.Default
	}
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	return &Cache{
		ttl:        ttl,
		store:      make(map[string]map[entryKey]entry, 64),
		now:        time.Now,
		maxEntries: maxEntries,
	}
}

// Get возвращает (true, true) если есть валидная positive-запись.
// Возвращает (false, false) в остальных случаях (miss / expired).
//
// На expiry — синхронно удаляет stale-entry (lazy eviction).
func (c *Cache) Get(subjectID, relation, objectType, objectID string) (allowed bool, ok bool) {
	c.mu.RLock()
	subMap, exists := c.store[subjectID]
	if !exists {
		c.mu.RUnlock()
		c.misses.Add(1)
		return false, false
	}
	e, exists := subMap[entryKey{relation, objectType, objectID}]
	c.mu.RUnlock()

	if !exists {
		c.misses.Add(1)
		return false, false
	}
	if c.now().After(e.expiresAt) {
		// Lazy delete — guarded против clobber конкурентно записанного свежего
		// entry (см. evictIfStale).
		c.evictIfStale(subjectID, entryKey{relation, objectType, objectID}, e.expiresAt)
		// Истёкшая запись — ПРОМАХ: вызывающий уйдёт к авторитетному Check ровно
		// так же, как если бы записи не было вовсе. Считать её попаданием значило
		// бы объявить долю тем выше, чем короче окно.
		c.misses.Add(1)
		return false, false
	}
	// Живая entry ⇒ positive-результат (negative не кешируется).
	c.hits.Add(1)
	return true, true
}

// evictIfStale удаляет entry (subjectID, key) под write lock, но ТОЛЬКО если
// сохранённый expiresAt всё ещё равен observedExpiresAt — тому stale-значению,
// которое Get наблюдал под RLock перед тем, как отпустить его.
//
// Зачем: между RUnlock и Lock в Get конкурентный SetAllowed мог записать свежий
// entry (новый expiresAt в будущем). Безусловный delete выкинул бы этот валидный
// positive-результат (потеря → лишний Check round-trip в kaname). Сравнение
// expiresAt гарантирует, что мы удаляем именно ту stale-запись, а не свежую.
func (c *Cache) evictIfStale(subjectID string, key entryKey, observedExpiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	subMap, ok := c.store[subjectID]
	if !ok {
		return
	}
	cur, ok := subMap[key]
	if !ok {
		return
	}
	if !cur.expiresAt.Equal(observedExpiresAt) {
		// Свежий entry записан конкурентно — не трогаем.
		return
	}
	delete(subMap, key)
	c.count--
	c.evictedExpired.Add(1)
	if len(subMap) == 0 {
		delete(c.store, subjectID)
	}
}

// evictLocked приводит размер кеша под потолок. Вызывается под write lock ПЕРЕД
// вставкой нового ключа, когда count достиг maxEntries. Фаза 1 — удалить все
// просроченные entry (дешевле и точнее). Фаза 2 (если и после этого полно) —
// удалить произвольные entry до low-water (maxEntries*7/8), чтобы вставка нового
// ключа гарантированно осталась под потолком. Произвольная эвикция безопасна:
// cache-miss всегда откатывается на авторитетный Check → корректность не страдает.
func (c *Cache) evictLocked() {
	now := c.now()
	var expired, capacity uint64
	for sid, sm := range c.store {
		for k, e := range sm {
			if now.After(e.expiresAt) {
				delete(sm, k)
				c.count--
				expired++
			}
		}
		if len(sm) == 0 {
			delete(c.store, sid)
		}
	}
	c.evictedExpired.Add(expired)
	if c.count < c.maxEntries {
		return
	}
	// Всё ещё полно — эвиктим произвольные entry до low-water. Эти записи ЖИВЫ:
	// каждая из них — попадание, которого не будет, и потому считается отдельной
	// причиной. Слитая с истечением, она объявила бы исчерпание потолка нормой.
	target := c.maxEntries - c.maxEntries/8
	if target < 0 {
		target = 0
	}
	for sid, sm := range c.store {
		for k := range sm {
			if c.count <= target {
				break
			}
			delete(sm, k)
			c.count--
			capacity++
		}
		if len(sm) == 0 {
			delete(c.store, sid)
		}
		if c.count <= target {
			break
		}
	}
	c.evictedCapacity.Add(capacity)
}

// SetAllowed — кеширует positive result (TTL).
//
// Set negative — не делается; если allowed=false, вызывающий не должен
// звать SetAllowed.
func (c *Cache) SetAllowed(subjectID, relation, objectType, objectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := entryKey{relation, objectType, objectID}
	// Проверяем, новый ли это ключ (эвикция по потолку нужна только для новых —
	// перезапись существующего размер не меняет).
	if sm, ok := c.store[subjectID]; ok {
		if _, keyExists := sm[key]; keyExists {
			sm[key] = entry{expiresAt: c.now().Add(c.ttl)}
			return
		}
	}
	// Новый ключ. Держим потолок ДО вставки (evictLocked может удалить subject-bucket).
	if c.count >= c.maxEntries {
		c.evictLocked()
	}
	subMap, exists := c.store[subjectID]
	if !exists {
		subMap = make(map[entryKey]entry, 8)
		c.store[subjectID] = subMap
	}
	subMap[key] = entry{expiresAt: c.now().Add(c.ttl)}
	c.count++
}

// InvalidateBySubject удаляет ВСЕ записи для subjectID.
//
// Вызывается:
//   - из listen_invalidate.go при NOTIFY `kacho_iam_subjects` (push-invalidate).
//   - может вызываться вручную (например в тесте).
//
// Idempotent.
func (c *Cache) InvalidateBySubject(subjectID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sm, ok := c.store[subjectID]; ok {
		c.count -= len(sm)
		c.invalidated.Add(uint64(len(sm)))
		delete(c.store, subjectID)
	}
}

// InvalidateAll удаляет весь кеш. Используется:
//   - в periodic full-cache-clear (см. KACHO_<SVC>_AUTHZ__FULL_CACHE_CLEAR_INTERVAL).
//   - в LISTEN-loop reconnect (conservative — иначе риск пропустить NOTIFY
//     во время disconnect).
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count > 0 {
		c.invalidated.Add(uint64(c.count))
	}
	c.store = make(map[string]map[entryKey]entry, 64)
	c.count = 0
}

// Size возвращает (subjectsCount, entriesCount). Используется в метриках.
func (c *Cache) Size() (subjects int, entries int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.store), c.count
}

// SetNowFunc — для тестов: подмена time.Now.
func (c *Cache) SetNowFunc(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// TTL — срок жизни положительной записи, то есть окно отзыва этого кеша.
//
// Экспортирован ради гейта политики окна отзыва: без него «какое окно у кеша,
// построенного вот так» нельзя ни спросить, ни утверждать — можно только
// пересказать литерал из конструктора, а пересказ переживает правку.
func (c *Cache) TTL() time.Duration { return c.ttl }
