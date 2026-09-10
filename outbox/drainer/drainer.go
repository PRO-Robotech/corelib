// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config — параметры конкретного экземпляра drainer-а.
type Config struct {
	// Table — полное имя outbox-таблицы (`<schema>.<table>`), e.g. "kaname.fga_outbox".
	Table string
	// Channel — имя LISTEN-канала, e.g. "kacho_iam_fga_outbox".
	Channel string
	// BatchSize — сколько rows клейм'ить за один catch-up SELECT (default 32).
	BatchSize int
	// PollFallback — интервал poll'а на случай missed NOTIFY (default 30s).
	PollFallback time.Duration
	// MaxAttempts — отметка «poisoned», после которой drainer перестает ретраить
	//   (default 10). Permanent-error → force attempt_count = MaxAttempts, drainer пропускает.
	MaxAttempts int
	// BackoffMin/BackoffMax — exp-backoff bounds (default 1s..30s).
	BackoffMin time.Duration
	BackoffMax time.Duration
	// ApplyTimeout — таймаут на один Apply-вызов (default 5s). Используется
	//   также как graceful-grace при shutdown: in-flight Apply имеет
	//   non-cancellable inner ctx с этим deadline, чтобы row не осталась
	//   half-applied при ctx.Cancel parent-loop'а.
	ApplyTimeout time.Duration
	// ApplyConcurrency — сколько строк одного claim-батча применять ПАРАЛЛЕЛЬНО
	// (default 1 = последовательно, историческое поведение). >1 разворачивает
	// внешние Apply-вызовы батча по N горутинам, скрывая per-call latency пира:
	// одиночный drainer с последовательными ApplyTimeout-bounded apply'ями
	// упирается в ~1/apply_latency (при таймаутящем пире — ~1/ApplyTimeout, что
	// катастрофически мало под write-burst). Claim-батч при ApplyConcurrency>1
	// сайзится ровно в ApplyConcurrency, поэтому вся волна применяется за один
	// проход параллельно; mark'и остаются ПОСЛЕДОВАТЕЛЬНЫМИ на единственной
	// claim-транзакции. Exactly-once НЕ меняется: та же claim-tx держит
	// FOR UPDATE SKIP LOCKED lock КАЖДОЙ заклейменной строки до commit'а, а
	// Apply не трогает DB-состояние (только внешний вызов) → параллельные apply
	// не создают ни второй tx, ни лишних conn'ов пула. Требование: Applier
	// БЕЗОПАСЕН для конкурентного вызова (см. Applier godoc).
	//
	// ORDERING (важно): drainer НЕ гарантирует apply в id-порядке — ни при
	// ApplyConcurrency>1 (строки батча применяются конкурентно), ни при
	// ApplyConcurrency=1: claim сортирует `ORDER BY attempt_count, id`, поэтому
	// transiently-bumped предшественник (attempt≥1) уступает свежему преемнику
	// (attempt=0) и применяется ПОСЛЕ него — внутри одного батча и тем более между
	// батчами. Порядок ВНУТРИ ключа даёт ТОЛЬКО PartitionColumn (см. ниже);
	// ApplyConcurrency — это throughput-ручка, а НЕ источник (и не причина)
	// reorder'а.
	//
	// Держать PartitionColumn пустым допустимо ТОЛЬКО когда финальное состояние
	// target'а СХОДИТСЯ при любом порядке применения строк ОДНОГО ключа:
	//   (а) поток по ключу append-only/коммутативен — нет события, отменяющего
	//       предыдущее (напр. только register/label-update, без unregister); либо
	//   (б) target версионирует ОБЕ ветки — и upsert, и удаление (versioned
	//       tombstone), так что stale-apply любого вида no-op'ится.
	//
	// Канонические register-outbox'ы kacho (`<svc>.fga_register_outbox`) под (а) и
	// (б) НЕ подпадают и ОБЯЗАНЫ задавать PartitionColumn (ключ = колонка ресурса,
	// напр. `resource_id`): они несут register И unregister ОДНОГО объекта, а
	// материализация в iam версионирована лишь ЧАСТИЧНО. source_version-LWW
	// (`resource_mirror` UPSERT `WHERE source_version < EXCLUDED.source_version`,
	// services/iam/.../resource_mirror/emitter.go) гейтит ТОЛЬКО ветку
	// ON CONFLICT DO UPDATE, т.е. спасает лишь register↔register. Пара
	// register(t1)→unregister(t2>t1) НЕ коммутативна и НЕ защищена: unregister
	// делает ЖЁСТКИЙ DELETE без tombstone, поэтому переставленный stale register
	// попадает в ветку INSERT (сравнивать не с чем) и ВОСКРЕШАЕТ mirror-строку
	// удалённого ресурса; level-triggered reconciler читает mirror как источник
	// истины и вечно ре-материализует owner-tuple, самоисцеления нет. Поведение
	// закреплено Test_1_4_45_RegisterOutbox_UnregisterThenStaleRegister
	// (register-outbox без PartitionColumn → resurrect; с PartitionColumn →
	// корректное ABSENT).
	//
	// Тот же класс разбирался на журнале намерений iam `fga_outbox`: он несёт
	// СЫРОЙ, вообще не версионированный owner-hierarchy tuple, а WRITE(grant) и
	// DELETE(revoke) одного (user,relation,object) не коммутируют, поэтому там
	// стоял PartitionColumn (`tuple_key` — материализованный триггером триплет)
	// вместе с ApplyConcurrency>1. Дренажа у этого журнала больше нет — он снят
	// вместе со своим адресатом, — но пример остаётся: правило про ширину ключа
	// от него не зависит.
	ApplyConcurrency int

	// PartitionColumn — SQL-выражение над строкой outbox-таблицы, дающее ключ
	// ПАРТИЦИИ порядка (обычная колонка: `resource_id` для register-outbox'ов).
	// Пусто (default) = фича выключена,
	// claim-запрос БАЙТ-в-БАЙТ прежний (нулевое изменение поведения для всех
	// consumer'ов, которые её не задают).
	//
	// # Ширина ключа: САМЫЙ УЗКИЙ ключ, на котором события НЕ коммутируют
	//
	// Ключ выбирается не «по ресурсу», а по НЕКОММУТАТИВНОСТИ: партиция обязана
	// совпадать с той единицей состояния target'а, которую события перезаписывают
	// друг у друга. Всё, что шире, — чистая ПЕРЕсериализация: она ничего не
	// добавляет к корректности, но ставит каждое событие в очередь за событиями, до
	// которых ему нет дела (радиус wedge тоже растёт до всей широкой группы).
	//
	// Канонический промах — iam `fga_outbox` до миграции 0067: партиция была
	// `payload->>'object'`, тогда как состояние адресата было МНОЖЕСТВОМ кортежей
	// с ключом (user, relation, object). WRITE(user:a,v_get,P) и DELETE(user:b,v_get,P)
	// трогают РАЗНЫЕ элементы и коммутируют; общий у них только object. Замер на
	// живом стенде под пакетным e2e: 8 643 pending-строки, 1 439 объектов, но 8 641
	// РАЗЛИЧНЫХ tuple'ов — то есть широкий ключ давал 1 439 claimable-голов вместо
	// 8 641, один revoke ждал до 632 предшественников по объекту при ≤3 по своему
	// tuple'у, и claim отбрасывал ~3 из 4 просмотренных строк как «не голова»
	// (68 кандидатов на 16 заклеймленных против ровно 16 на узком ключе). Под
	// нагрузкой это выносило видимость отзыва прав на ~30 c при бюджете пробы 15 c.
	// Регрессию (обе стороны — и порядок, и отсутствие лишней сериализации) локают
	// Test_1_4_46_PartitionKey_SameObject_DistinctSubjects_StayConcurrent и
	// Test_1_4_47_NarrowKey_PreservesOrder_UnderSameObjectNoise.
	//
	// Сужение ключа безопасно ровно до этой границы и НЕ создаёт окна: предикат не
	// меняется, меняется только его equi-ключ, поэтому пара «WRITE→DELETE одного
	// ключа» по-прежнему упорядочена cross-batch и cross-replica
	// (Test_1_4_48_NarrowKey_OrderHolds_AcrossReplicas). Ошибка в другую сторону —
	// ключ УЖЕ реального (расщепить одну единицу состояния на две партиции) — вот
	// это и есть потеря порядка; поэтому при сомнении в разделителе/рендеринге ключа
	// выбирай тот, который может только СКЛЕИТЬ две единицы (пере-упорядочить), а не
	// расщепить одну.
	//
	// # Зачем (order-preserving drain без cross-batch reorder-leak)
	//
	// Порядок apply НЕ сохраняется и БЕЗ конкурентности: claim
	// `ORDER BY (attempt_count,id)` разносит bumped-предшественника (attempt≥1 после
	// transient) и fresh-преемника (attempt=0) — преемник обгоняет предшественника
	// уже внутри одного батча при ApplyConcurrency=1 и тем более попадает в БОЛЕЕ
	// РАННИЙ батч. ApplyConcurrency>1 добавляет сверху ещё и intra-batch reorder
	// (горутины конкурентны), но не является причиной проблемы. Для order-sensitive
	// target'а это ломается двумя способами: iam raw-tuple (WRITE потом DELETE того
	// же tuple) → delete-before-write → tuple ВЫЖИВАЕТ → authz over-grant /
	// cross-account LEAK; register-outbox (register потом unregister того же
	// ресурса) → unregister-before-register → воскрешённая mirror-строка удалённого
	// ресурса (см. ORDERING в ApplyConcurrency).
	//
	// PartitionColumn закрывает это на CLAIM-уровне (не на apply-re-sort, который
	// бессилен, если предшественник в другом батче): claim НЕ берёт строку t, если
	// в её партиции существует ДОСТАВЛЯЕМЫЙ (sent_at IS NULL AND
	// attempt_count < MaxAttempts) предшественник с меньшим id. Тогда:
	//   - преемник НИКОГДА не заклеймлен впереди unsent-предшественника (cross-batch
	//     И cross-replica: незакоммиченный claim соседа виден как sent_at IS NULL в
	//     снапшоте → его преемник остаётся заблокирован до commit'а) →
	//     per-partition FIFO держится by construction;
	//   - в любом снапшоте claimable ровно ОДНА строка партиции (её head), поэтому
	//     один claim-батч НИКОГДА не содержит две строки одной партиции → intra-batch
	//     конкурентный reorder двух строк одной партиции НЕВОЗМОЖЕН by construction
	//     (apply-level partition-grouping не нужен — было бы vestigial).
	// Строки РАЗНЫХ партиций по-прежнему клеймятся и применяются конкурентно
	// (пропускная способность ApplyConcurrency сохранена — предикат сериализует
	// только внутри одной партиции).
	//
	// # Head-of-line wedge (осознанный trade-off leak-safety > per-partition liveness)
	//
	// Пока head партиции доставляем-но-ещё-не-доставлен (transient-stuck: peer down,
	// attempt капнут на MaxAttempts-1, ретраится вечно с backoff) — его преемники
	// ЖДУТ. Это ВРЕМЕННЫЙ wedge: transient-строка НИКОГДА не отравляется (cap ниже
	// poison-gate) → применится, как только peer оживёт → wedge рассосётся. Границей
	// wedge является восстановление peer'а, не бесконечность. ОТРАВЛЕННЫЙ (poisoned,
	// attempt_count=MaxAttempts) предшественник из блокирующего набора ИСКЛЮЧЁН
	// (предикат `attempt_count < MaxAttempts`), т.к. он НИКОГДА не применится —
	// вечно wedge'ить партицию им нельзя; преемник «перепрыгивает» мёртвого
	// предшественника. Leak это НЕ реинтродуцирует: отравленный WRITE не создаёт
	// tuple → последующий DELETE (или отсутствие эффекта) не оставляет над-гранта;
	// порядок между ДОСТАВЛЯЕМЫМИ строками партиции по-прежнему строгий. Wedge
	// наблюдаем: (а) table-wide `outbox_oldest_pending_age_seconds{table}` (metrics
	// Collector) растёт, пока любой head застрял → alertable; (б) per-partition
	// атрибуция — опциональный WithWedgeObserver + WedgeWarnAfter (см. ниже).
	//
	// # Контракт выражения
	//
	// PartitionColumn — выражение над НЕквалифицированными колонками строки,
	// начинающееся с идентификатора колонки, так что префикс `<alias>.` даёт
	// валидный квалифицированный SQL. ПРЕДПОЧТИТЕЛЬНА обычная колонка (`tuple_key`,
	// `resource_id`): claim'у нужен partial-индекс ровно по этому ключу, и колонка
	// делает его простым btree, который планировщик не может «не сматчить»;
	// составной ключ материализуй колонкой (миграция + BEFORE INSERT триггер как
	// единственный источник истины для ВСЕХ writer'ов), а не встраивай выражение —
	// разъехавшийся рендеринг ключа у одного из writer'ов расщепляет одну единицу
	// состояния на две партиции и молча теряет порядок. Значение используется в
	// equi-сравнении партиций в claim-предикате; в claim-пути оно не логируется, но
	// per-partition wedge-observer (WithWedgeObserver) НАМЕРЕННО surfaces ключ в WARN
	// для атрибуции застрявшей партиции — это id-based handle (напр.
	// `user:<id> v_get vpc_network:<id>`), не PII/инфра-чувствительное. Rows с
	// NULL-ключом партиции трактуются как независимые (NULL=NULL не true) — поэтому
	// таблица обязана гарантировать непустой ключ (у iam — CHECK ... NOT VALID).
	//
	// # Обязательные индексы — РОВНО ДВА, и «ровно» здесь так же важно, как «два»
	//
	// Миграция сервиса ОБЯЗАНА нести ДВА partial-индекса (оба `WHERE sent_at IS
	// NULL`, чтобы размер тянулся за PENDING-backlog'ом, а не за append-mostly
	// таблицей) — и НИ ОДНОГО ТРЕТЬЕГО:
	//
	//	1. `(<PartitionColumn>, id) WHERE sent_at IS NULL` — коррелированный
	//	   NOT EXISTS (поиск предшественника партиции);
	//	2. `(attempt_count, id) WHERE sent_at IS NULL` — ВНЕШНИЙ упорядоченный
	//	   скан `ORDER BY (attempt_count, id)`.
	//
	// (1) БЕЗ (2) — НЕДОСТАТОЧНО, и это не «чуть медленнее», а throughput-инверсия.
	// Без упорядоченного access-path планировщик вообще не доходит до
	// nested-loop anti-join, ради которого построен (1): он seq-скан'ит ВЕСЬ
	// pending-backlog, сортирует его и hash-anti-join'ит со ВТОРЫМ полным
	// seq-скан'ом того же backlog'а — (1) при этом не используется, claim
	// становится O(backlog). Дальше включается положительная обратная связь: чем
	// глубже очередь, тем медленнее claim → тем медленнее дренаж → тем глубже
	// очередь; producer, обгоняющий consumer хотя бы на проценты, расходится
	// неограниченно вместо небольшого постоянного лага. Замер на живом стенде
	// (Postgres 16, kaname.fga_outbox), один claim: backlog 5k — 11.7ms без (2)
	// против 0.81ms с (2); 20k — 61.6ms против 0.72ms; 80k — 327ms против 0.82ms
	// (с (2) время от глубины не зависит). Регрессию локает
	// Test_ClaimPlan_DoesNotScaleWithBacklogDepth.
	//
	// ЛЮБОЙ ТРЕТИЙ partial-индекс по `sent_at IS NULL` в ДРУГОМ порядке — decoy, и
	// он возвращает ровно ту же инверсию, даже когда (1) и (2) на месте. Очередь
	// почти всегда пуста, поэтому последний ANALYZE почти всегда снят на ПУСТОМ
	// backlog'е, и в burst планировщик входит с оценкой `rows=1`; на ней Sort
	// «бесплатен», и любой индекс, дающий дешёвый скан pending-строк в НЕ том
	// порядке, выигрывает по стоимости. Итоговый план — «Index Scan(decoy) → Sort →
	// Limit»: Sort обязан материализовать ВЕСЬ pending-набор прежде, чем сработает
	// LIMIT, поэтому anti-join прогоняется по разу на КАЖДУЮ pending-строку, а (1)
	// и (2) просто не используются. Замер на живом стенде, тот же claim, 8 600
	// pending, разница только в наличии лишнего `(created_at) WHERE sent_at IS NULL`:
	// 6 990 ms против 29 ms (в 240 раз). В логах drainer'а это выглядело как ровно
	// один 16-строчный батч раз в ~11.5 c в течение 46 c (1.4 строки/с) с прыжком до
	// ~600 строк/с в момент, когда autovacuum наконец переанализировал таблицу.
	// Держи индексный набор МИНИМАЛЬНЫМ: не «добавь индекс — хуже не будет», а
	// «каждый лишний порядок над pending-строками — это план, который планировщик
	// выберет именно тогда, когда очередь глубокая». Per-table autovacuum-настройки
	// (у iam — миграция 0064) сокращают ОКНО плохих статистик, но не устраняют
	// плохой ПЛАН: autovacuum_naptime кластерный (1 мин), поэтому окно всё равно
	// есть. Регрессию локает Test_ClaimPlan_StaleStatistics_DoesNotCollapse.
	PartitionColumn string

	// WedgeWarnAfter — если >0 И задан PartitionColumn И зарегистрирован
	// WithWedgeObserver, drainer периодически (rate-limited) сообщает партиции, чей
	// самый старый unsent-row старше этого порога (head-of-line wedge, см. выше).
	// Сообщаются ТОЛЬКО N самых старых (wedgeReportLimit): задача observer'а —
	// АТРИБУЦИЯ («какая партиция стоит»), а «сколько стоит» без кардинальности уже
	// отвечают table-wide backlog-depth / oldest-pending-age метрики. Ключ узкий, и
	// при выпадении peer'а под порог попадают ВСЕ партиции сразу — без cap'а это
	// тысячи WARN-строк за скан, которые хоронят собственно атрибуцию.
	// 0 (default) = per-partition wedge-репорт выключен (нулевая стоимость; table-wide
	// oldest-pending-age метрика всё равно покрывает «доставка застряла»). Опрос
	// использует тот же partial-index, что и claim.
	WedgeWarnAfter time.Duration

	// PermanentPolicy — что делать с ПОСТОЯННЫМ отказом ПРИМЕНЕНИЯ. Умолчание
	// (нулевое значение) — травить, то есть сегодняшнее поведение.
	//
	// RetryPermanent законен ТОЛЬКО без PartitionColumn: обоснование политики
	// целиком опирается на отсутствие партиции. Пара отвергается Validate, а не
	// оговаривается комментарием. Полностью — у типа PermanentPolicy.
	PermanentPolicy PermanentPolicy
}

// Validate отвергает настройку, которая внутренне противоречива.
//
// Отдельный экспортируемый метод, а не ветка внутри New: правило проверяемо без
// пула и без базы, и проверять его хочется там же, где оно записано.
func (c Config) Validate() error {
	if c.PermanentPolicy == RetryPermanent && c.PartitionColumn != "" {
		return fmt.Errorf(
			"drainer.Config: PermanentPolicy=RetryPermanent несовместим с "+
				"PartitionColumn=%q — политика повтора обоснована ОТСУТСТВИЕМ партиции "+
				"(коммутативному потоку нечего заклинивать), а с ключом порядка "+
				"постоянный отказ заклинил бы свою партицию навсегда: ни одна строка "+
				"за ним не будет взята. Либо сними ключ порядка, либо оставь травление "+
				"и подними возврат отравленных строк",
			c.PartitionColumn)
	}
	return nil
}

// withDefaults заполняет нулевые поля конфигом по умолчанию.
func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 32
	}
	if c.PollFallback <= 0 {
		c.PollFallback = 30 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = 1 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.ApplyTimeout <= 0 {
		c.ApplyTimeout = 5 * time.Second
	}
	if c.ApplyConcurrency < 1 {
		c.ApplyConcurrency = 1
	}
	return c
}

// Decoder[T] — превращает payload JSONB в типизированный T.
// Ошибка decoder-а трактуется как permanent (poisoned row), drainer не вызывает
// applier и помечает row attempt_count = MaxAttempts + last_error = err.
type Decoder[T any] func(payload []byte) (T, error)

// Applier[T] — применяет T к target-системе.
// Возвращает nil → success (drainer mark'ит sent_at).
// Возвращает ErrAlreadyApplied → idempotent success (drainer mark'ит sent_at).
// Возвращает любую другую error → transient (retry с exp backoff)
//
//	ИЛИ permanent (если errors.Is(err, ErrPermanent)).
//
// CONCURRENCY: при Config.ApplyConcurrency>1 drainer вызывает Applier из
// НЕСКОЛЬКИХ горутин одновременно (разные строки одного claim-батча) — Applier
// ОБЯЗАН быть безопасен для конкурентного вызова. Канонические appliers это
// gRPC-клиенты поверх *grpc.ClientConn (конкурентно-безопасен) + чистое
// построение запроса — они уже удовлетворяют требованию. Applier, шарящий
// mutable-состояние без синхронизации, обязан либо синхронизироваться, либо
// оставить ApplyConcurrency=1.
type Applier[T any] func(ctx context.Context, eventType string, payload T) error

// ErrAlreadyApplied — applier возвращает, когда адресат сообщил «уже есть»:
// повторная запись того, что у него уже лежит, либо снятие того, чего у него уже
// нет. Drainer трактует как success.
//
// ВНИМАНИЕ: отказ, при котором адресат НЕ ЗАПИСАЛ НИЧЕГО (транзакционный обрыв,
// конфликт с параллельным писателем), сюда НЕ относится, как бы он ни назывался на
// проводе. Пометив такую строку sent_at, дренаж молча потерял бы намерение —
// перманентная дыра в правах; такой отказ обязан остаться transient, его и лечит
// повтор. Различить их может только applier: словарь адресата знает он один.
var ErrAlreadyApplied = errors.New("drainer: target reports already-applied (idempotent)")

// ErrPermanent — applier wrap'ит в это, если retry бессмыслен (HTTP 4xx кроме
// conflict-класса, malformed payload, etc). Drainer mark'ит last_error и пропускает
// row через force attempt_count = MaxAttempts.
var ErrPermanent = errors.New("drainer: permanent error, no retry")

// Drainer[T] — экземпляр drainer-а для одного outbox-table + один applier.
//
// Drainer работает по схеме:
//  1. Слушает LISTEN-канал `cfg.Channel` через dedicated pgx.Conn (hijacked
//     из pool).
//  2. На старте — catch-up: SELECT pending rows (sent_at IS NULL, attempt_count < MaxAttempts)
//     ORDER BY attempt_count, id LIMIT BatchSize → applies each (attempt_count-first
//     ordering предотвращает starvation свежего intent транзитно-залипшим backlog'ом).
//  3. Main loop: wake-up по NOTIFY (payload = row id) ИЛИ tick(PollFallback)
//     → claim → apply → mark.
//  4. Exactly-once: pre-claim атомарный UPDATE … RETURNING с CAS (sent_at IS NULL
//     AND attempt_count < MaxAttempts) — две реплики не возьмут одну row.
//  5. Graceful shutdown: при ctx.Done() — дозавершает текущий in-flight apply
//     (отдельный inner ctx с ApplyTimeout grace), exit.
type Drainer[T any] struct {
	cfg     Config
	pool    *pgxpool.Pool
	decoder Decoder[T]
	applier Applier[T]
	logger  *slog.Logger

	// onPoison, if set, is invoked once each time a row is poisoned (permanent
	// error / decode-fail). Used to drive the outbox_poisoned_total metric
	// without coupling the drainer to the metrics package.
	onPoison func()

	// onDeliver, if set, is invoked once each time a row is DELIVERED, with that
	// row's event type. Drives the per-direction delivered counter without
	// coupling the drainer to the metrics package — the same shape as onPoison.
	//
	// Тип события, а не направление: направление есть словарь НАБЛЮДАТЕЛЯ
	// (`metrics.RegisterOutboxDirections`), и дренаж его не знает и знать не
	// должен. Отдав ему направление, мы завели бы второе написание того же
	// словаря, а два написания расходятся молча.
	onDeliver func(eventType string)

	// onClaim, if set, is invoked once each time a claim query is issued against
	// the outbox table (every SELECT…FOR UPDATE SKIP LOCKED claim, including the
	// terminal empty claim that ends a drain loop). Enables deterministic,
	// in-process observation of claim frequency — e.g. a test can assert an idle
	// drainer issues ZERO claims across an observation window (busy-poll guard)
	// without depending on asynchronous pg_stat counters.
	onClaim func()

	// onWedge, if set (with Config.PartitionColumn and Config.WedgeWarnAfter>0),
	// is invoked once per partition whose oldest unsent row is older than
	// WedgeWarnAfter — the head-of-line wedge signal for a partition-ordered drain
	// (a stuck/transient partition head blocks its successors by design). Wire it
	// to a WARN log / metric for per-partition attribution beyond the table-wide
	// outbox_oldest_pending_age_seconds gauge. The scan is rate-limited to at most
	// once per WedgeWarnAfter and uses the same partial index as the claim.
	onWedge func(partition string, oldestUnsentAge time.Duration)
	// lastWedgeCheck throttles the wedge scan. Accessed only from the single
	// Run/drainBatch goroutine — no synchronisation needed.
	lastWedgeCheck time.Time
}

// Option customises a Drainer at construction (functional-options pattern).
type Option[T any] func(*Drainer[T])

// WithPoisonObserver registers a callback invoked once per poisoned row. Wire it
// to a metrics recorder's IncPoisoned to make poison events observable
// (outbox_poisoned_total). nil is ignored.
func WithPoisonObserver[T any](fn func()) Option[T] {
	return func(d *Drainer[T]) {
		if fn != nil {
			d.onPoison = fn
		}
	}
}

// WithDeliveryObserver registers a callback invoked once per DELIVERED row, with
// that row's event type. Wire it to a metrics recorder to make
// «доставлено по направлению» a MONOTONIC counter. nil is ignored.
//
// # Почему счётчик здесь, а не скан живых строк
//
// Величина объявлена «за всё время» и служит ЕДИНСТВЕННЫМ способом отличить
// «отзывов не было» от «отзывы не проходят». Считать её `count(*)` по живым
// строкам можно ровно до тех пор, пока строки не убираются: уборка доставленных
// (#1361) обнулила бы её на исправной очереди, где отзыв редок, — то есть
// уничтожила бы именно тот сигнал, ради которого разбивка по направлениям
// заведена. Наблюдатель считает СОБЫТИЕ доставки, и уборка на него не влияет
// by construction (#1714).
//
// Зовётся ПОСЛЕ того, как исход строки признан доставкой, и до возврата из
// пометки; последовательно в пределах партии — как и onPoison.
func WithDeliveryObserver[T any](fn func(eventType string)) Option[T] {
	return func(d *Drainer[T]) {
		if fn != nil {
			d.onDeliver = fn
		}
	}
}

// WithClaimObserver registers a callback invoked once per claim query issued
// against the outbox table. Enables deterministic in-process observation of
// claim frequency (busy-poll guard) independent of pg_stat lag. nil is ignored.
func WithClaimObserver[T any](fn func()) Option[T] {
	return func(d *Drainer[T]) {
		if fn != nil {
			d.onClaim = fn
		}
	}
}

// WithWedgeObserver registers a callback invoked (rate-limited) once per
// partition whose oldest unsent row exceeds Config.WedgeWarnAfter — the
// head-of-line wedge signal for a partition-ordered drain. Requires
// Config.PartitionColumn set and Config.WedgeWarnAfter>0 (otherwise the scan is
// skipped and this is a no-op). Wire it to a WARN log / gauge for per-partition
// attribution. nil is ignored.
func WithWedgeObserver[T any](fn func(partition string, oldestUnsentAge time.Duration)) Option[T] {
	return func(d *Drainer[T]) {
		if fn != nil {
			d.onWedge = fn
		}
	}
}

// New создает Drainer; не запускает (вызывайте Run).
//
// pool — *pgxpool.Pool на БД сервиса (тот же pool, что используется для
// бизнес-логики; drainer Acquire().Hijack() один conn для LISTEN, остальные
// операции — через pool как обычно).
//
// decoder/applier — пользовательские функции, см. Decoder[T] / Applier[T].
//
// logger — slog.Logger; nil → slog.Default().
func New[T any](
	pool *pgxpool.Pool,
	cfg Config,
	decoder Decoder[T],
	applier Applier[T],
	logger *slog.Logger,
	opts ...Option[T],
) (*Drainer[T], error) {
	if pool == nil {
		return nil, errors.New("drainer.New: pool is nil")
	}
	if cfg.Table == "" {
		return nil, errors.New("drainer.New: Config.Table required")
	}
	if cfg.Channel == "" {
		return nil, errors.New("drainer.New: Config.Channel required")
	}
	if decoder == nil {
		return nil, errors.New("drainer.New: decoder is nil")
	}
	if applier == nil {
		return nil, errors.New("drainer.New: applier is nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	cfg = cfg.withDefaults()
	logger = logger.With(
		slog.String("component", "outbox_drainer"),
		slog.String("table", cfg.Table),
		slog.String("channel", cfg.Channel),
	)
	d := &Drainer[T]{
		cfg:     cfg,
		pool:    pool,
		decoder: decoder,
		applier: applier,
		logger:  logger,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// Run — основной loop drainer-а. Блокирует до ctx.Done().
//
// Поведение:
//  1. Запускает LISTEN-loop в goroutine (own conn, reconnect on drop).
//  2. Выполняет startup catch-up (drains all pending rows).
//  3. Основной select: NOTIFY-wake-up ИЛИ PollFallback-tick ИЛИ ctx.Done().
//     Каждое wake-up → drainBatch() (claim + apply + mark, в loop пока не пусто).
//  4. На ctx.Done() — дозавершает текущий drainBatch (с inner ApplyTimeout-grace
//     на in-flight apply), exits.
//
// Возвращает nil при clean shutdown.
//
// РЕПЛИКИ: клейм — строки берутся клеймом `FOR UPDATE SKIP LOCKED` по голове партиции,
// поэтому репликам достаются непересекающиеся партии, а порядок внутри
// партиции держится даже между партиями и репликами.
func (d *Drainer[T]) Run(ctx context.Context) error {
	// Wake-up signal channel — listenLoop signals on NOTIFY, processLoop consumes.
	// Buffered: один сигнал «есть работа», даже если processLoop в данный
	// момент busy — он перепроверит после возврата.
	wakeup := make(chan struct{}, 1)

	// LISTEN-goroutine: own its own ctx tied to parent. Errors from LISTEN
	// subscription (conn drop, NOTIFY parse) are caught and re-tried with
	// exp-backoff inside listenLoop. Panics propagate to the runtime — drainer
	// is process-fatal on unhandled panic (correct: such panics indicate
	// programmer error, not transient infra failure).
	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		d.listenLoop(ctx, wakeup)
	}()

	// Стартовая попытка катч-апа — выгребаем все накопленное до начала LISTEN.
	// Если в этот момент LISTEN еще не подключился и кто-то INSERTит — мы либо
	// поймаем через NOTIFY (после connect), либо через PollFallback. Race ok.
	d.drainBatch(ctx)

	poll := time.NewTicker(d.cfg.PollFallback)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			// Wait for listen-goroutine to exit (it will once ctx is done).
			<-listenDone
			return nil
		case <-wakeup:
			d.drainBatch(ctx)
		case <-poll.C:
			d.drainBatch(ctx)
		}
	}
}
