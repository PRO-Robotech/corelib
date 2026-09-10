// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package metrics exposes the outbox-delivery observability surface: backlog
// depth, oldest-pending age and poison count per outbox table/channel. It makes
// a stuck/lost owner-tuple delivery observable (alertable) instead of a silent
// Warn-log.
//
// Dependency boundary (Clean Architecture): corelib stays dependency-light — the
// concrete Prometheus client is NOT imported here. Instead the package defines a
// small Recorder interface; the service wires a Prometheus-backed Recorder at
// its composition root (mirroring kaname internal/observability/metrics), and
// tests use the in-memory MemRecorder. The Collector periodically scans an
// outbox table (DB-side) and feeds the gauges into whatever Recorder it is given.
//
// Three series, all labelled by the outbox table name (so a service running two
// outbox families — audit `_outbox` vs register `_fga_register_outbox` — keeps
// them separate and never conflates poison/backlog):
//
//	outbox_backlog_depth{table}              gauge  — pending (sent_at IS NULL) rows
//	outbox_oldest_pending_age_seconds{table} gauge  — age of the oldest pending row
//	outbox_poisoned_total{table}             counter — monotonic poison events
//	(the Collector also reports the current poisoned-row gauge via PoisonedCount)
package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/corelib/outbox"
)

// Recorder is the metrics sink the outbox layer writes to. Implement it with a
// Prometheus registry at the service composition root; corelib provides the
// in-memory MemRecorder for tests and a no-op via a nil-safe wrapper.
//
//   - SetBacklogDepth / SetOldestPendingAgeSeconds / SetPoisonedCount — gauges
//     set by the Collector on each scan.
//   - IncPoisoned — the monotonic counter, incremented by the drainer's poison
//     observer (see drainer.WithPoisonObserver).
type Recorder interface {
	SetBacklogDepth(table string, depth float64)
	SetOldestPendingAgeSeconds(table string, age float64)
	SetPoisonedCount(table string, count float64)
	IncPoisoned(table string)
}

// DirectionRecorder is the OPTIONAL half of the sink: the same three questions asked per
// DIRECTION of the queue, plus the one the table-wide series cannot express at all — how
// many rows of a direction have EVER been delivered.
//
// ПЕРВЫЕ ДВЕ ВЕЛИЧИНЫ СТАВИТ СКАН, ТРЕТЬЮ — ДРЕНАЖ, и это не асимметрия ради
// удобства. Первые две суть вопросы о том, что ЛЕЖИТ в очереди сейчас, и скан
// отвечает на них по определению. Третья — вопрос о том, что ПРОИЗОШЛО, и скан
// живых строк отвечает на него лишь пока строки не убираются (#1714).
//
// WHY IT IS A SEPARATE INTERFACE. A recorder that predates the split still satisfies
// Recorder, and the Collector asserts this capability at run time — so wiring that has
// not adopted the split keeps working and simply publishes no per-direction series.
// Absence of the series is the honest signal there; a zero would read as "no withdrawals
// happened", which is exactly the state the split exists to distinguish from "withdrawals
// do not arrive".
//
// WHY THE SPLIT IS NEEDED AT ALL. One queue carries both directions and all three
// table-wide series aggregate them. Grants flow continuously — resources are created all
// the time — so the aggregate looks healthy no matter whether a single withdrawal ever
// lands: the depth is small because grants drain, the head is young for the same reason,
// and nothing is poisoned because nothing refused. "It works" and "it was never revoked"
// therefore produce an IDENTICAL picture. Measured 2026-08-04: 479 repository
// registrations against 60 withdrawals, and no aggregate series said so.
type DirectionRecorder interface {
	// SetBacklogDepthByDirection — pending rows of one direction.
	SetBacklogDepthByDirection(table, direction string, depth float64)
	// SetOldestPendingAgeByDirection — age of the OLDEST pending row of one direction:
	// the value that answers "how long ago did this direction stop arriving".
	SetOldestPendingAgeByDirection(table, direction string, age float64)
	// IncDeliveredByDirection — ОДНА доставленная строка направления.
	//
	// СЧЁТЧИК, А НЕ ИЗМЕРИТЕЛЬ, и это несущее различие (#1714). Величина
	// объявлена «за всё время» и служит единственным способом отличить «отзывов
	// не было» от «отзывы не проходят». Прежде она ставилась сканом как
	// `count(*)` по ЖИВЫМ строкам — совпадая с объявленным ровно до тех пор, пока
	// строки не убираются. Уборка доставленных (#1361) обнулила бы её на
	// ИСПРАВНОЙ очереди, где отзыв редок, и ноль прочитался бы по контракту как
	// «не доставлено ни одного отзыва».
	//
	// Инкрементирует НАБЛЮДАТЕЛЬ ДРЕНАЖА (`drainer.WithDeliveryObserver`) — как
	// это уже сделано у отравления (`IncPoisoned`), — поэтому величина не
	// зависит от числа живых строк by construction.
	IncDeliveredByDirection(table, direction string)
	// InitDeliveredByDirection — ЗАВЕСТИ серию направления со значением ноль, не
	// увеличивая её.
	//
	// БЕЗ ЭТОГО МЕТОДА СЧЁТЧИК ТЕРЯЕТ СВОЙ ПРЕДМЕТ, и это не украшение. Дочерняя
	// серия счётчика появляется в выдаче лишь ПОСЛЕ первого инкремента — значит
	// «ни одного отзыва не доставлено» выражалось бы ОТСУТСТВИЕМ ряда, тогда как
	// весь смысл разбивки в том, чтобы это состояние было видно ЧИСЛОМ. Прежняя
	// величина-измеритель заводилась сканом и потому всегда существовала; счётчик
	// обязан получить ту же видимость явно.
	//
	// Зовётся ОДИН раз на направление при сборке наблюдателя ([DeliveryObserver]),
	// то есть при старте процесса: ряд существует с нуля, ещё до первой доставки.
	InitDeliveredByDirection(table, direction string)
}

// DeliveryObserver — переходник «дренаж → счётчик направления».
//
// Живёт ЗДЕСЬ, потому что словарь направлений принадлежит наблюдению, а не
// дренажу: дренаж отдаёт ТИП СОБЫТИЯ, и второе написание словаря у него
// разошлось бы с этим молча.
//
// Событие вне словаря СЧИТАЕТСЯ ОТДЕЛЬНО и роняет не прогон, а величину: у
// очереди бывают типы, которых разбивка не покрывает, и молча приписать их
// чужому направлению значило бы солгать именно той величине, ради точности
// которой всё это заведено. Возвращает nil, когда считать некому либо нечем, —
// тогда дренаж не получает наблюдателя вовсе (nil игнорируется опцией), а не
// зовёт пустышку.
// Принимает [Recorder] и сам приводит его к [DirectionRecorder] — ТОЙ ЖЕ
// проверкой в рантайме, что и `Collector.scanDirections`. Приёмник, заведённый
// до разбивки, по-прежнему удовлетворяет [Recorder]: он просто не получает
// наблюдателя, и серии не публикуются вовсе. Отсутствие серии — честный сигнал;
// ноль прочитался бы как «отзывов не было», а это ровно то состояние, ради
// отличения которого разбивка и заведена.
func DeliveryObserver(table string, dirs map[string][]string, rec Recorder) func(string) {
	dirRec, ok := rec.(DirectionRecorder)
	if !ok || len(dirs) == 0 {
		return nil
	}
	// Обратный словарь строится ОДИН раз: наблюдатель зовётся на каждой
	// доставленной строке, то есть на горячем пути дренажа.
	byEvent := make(map[string]string, len(dirs))
	for dir, events := range dirs {
		for _, ev := range events {
			byEvent[ev] = dir
		}
		// Ряд заводится СРАЗУ и с нуля: «ни одного отзыва не доставлено» обязано
		// читаться числом, а не отсутствием ряда (см. InitDeliveredByDirection).
		dirRec.InitDeliveredByDirection(table, dir)
	}
	return func(eventType string) {
		if dir, ok := byEvent[eventType]; ok {
			dirRec.IncDeliveredByDirection(table, dir)
		}
	}
}

// MemRecorder is an in-memory Recorder for tests and as a safe default. It is
// concurrency-safe.
type MemRecorder struct {
	mu            sync.Mutex
	backlog       map[string]float64
	oldest        map[string]float64
	poisonedCount map[string]float64 // current count gauge (Collector)
	poisonedTotal map[string]float64 // monotonic counter (drainer)
	// Per-direction series, keyed by table then direction. Absent until a Collector
	// configured with Directions records them — absence is the signal, not a zero.
	dirBacklog   map[string]map[string]float64
	dirOldest    map[string]map[string]float64
	dirDelivered map[string]map[string]float64
}

// NewMemRecorder constructs an empty in-memory recorder.
func NewMemRecorder() *MemRecorder {
	return &MemRecorder{
		backlog:       map[string]float64{},
		oldest:        map[string]float64{},
		poisonedCount: map[string]float64{},
		poisonedTotal: map[string]float64{},
		dirBacklog:    map[string]map[string]float64{},
		dirOldest:     map[string]map[string]float64{},
		dirDelivered:  map[string]map[string]float64{},
	}
}

// SetBacklogDepth records the pending-row gauge for a table.
func (m *MemRecorder) SetBacklogDepth(table string, depth float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backlog[table] = depth
}

// SetOldestPendingAgeSeconds records the oldest-pending-age gauge for a table.
func (m *MemRecorder) SetOldestPendingAgeSeconds(table string, age float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.oldest[table] = age
}

// SetPoisonedCount records the current poisoned-row gauge for a table.
func (m *MemRecorder) SetPoisonedCount(table string, count float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poisonedCount[table] = count
}

// IncPoisoned increments the monotonic poison counter for a table.
func (m *MemRecorder) IncPoisoned(table string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poisonedTotal[table]++
}

// BacklogDepth returns the last-recorded backlog gauge (test accessor).
func (m *MemRecorder) BacklogDepth(table string) float64 { return m.read(m.backlog, table) }

// OldestPendingAgeSeconds returns the last-recorded oldest-age gauge (test accessor).
func (m *MemRecorder) OldestPendingAgeSeconds(table string) float64 { return m.read(m.oldest, table) }

// PoisonedCount returns the last-recorded poisoned-row gauge (test accessor).
func (m *MemRecorder) PoisonedCount(table string) float64 { return m.read(m.poisonedCount, table) }

// PoisonedTotal returns the monotonic poison counter (test accessor).
func (m *MemRecorder) PoisonedTotal(table string) float64 { return m.read(m.poisonedTotal, table) }

func (m *MemRecorder) read(src map[string]float64, table string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return src[table]
}

// SetBacklogDepthByDirection / SetOldestPendingAgeByDirection / IncDeliveredByDirection —
// DirectionRecorder for tests.
func (m *MemRecorder) SetBacklogDepthByDirection(table, direction string, depth float64) {
	m.write(m.dirBacklog, table, direction, depth)
}

func (m *MemRecorder) SetOldestPendingAgeByDirection(table, direction string, age float64) {
	m.write(m.dirOldest, table, direction, age)
}

// IncDeliveredByDirection — монотонный счётчик доставленных строк направления.
func (m *MemRecorder) IncDeliveredByDirection(table, direction string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirDelivered[table] == nil {
		m.dirDelivered[table] = map[string]float64{}
	}
	m.dirDelivered[table][direction]++
}

// InitDeliveredByDirection заводит серию направления с нулём, не увеличивая её.
func (m *MemRecorder) InitDeliveredByDirection(table, direction string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirDelivered[table] == nil {
		m.dirDelivered[table] = map[string]float64{}
	}
	if _, ok := m.dirDelivered[table][direction]; !ok {
		m.dirDelivered[table][direction] = 0
	}
}

// BacklogDepthByDirection / OldestPendingAgeByDirection / DeliveredTotal — test accessors.
func (m *MemRecorder) BacklogDepthByDirection(table, direction string) float64 {
	return m.readDir(m.dirBacklog, table, direction)
}

func (m *MemRecorder) OldestPendingAgeByDirection(table, direction string) float64 {
	return m.readDir(m.dirOldest, table, direction)
}

func (m *MemRecorder) DeliveredTotal(table, direction string) float64 {
	return m.readDir(m.dirDelivered, table, direction)
}

// Directions returns the direction names this recorder has ANY series for, so a test can
// assert that an unconfigured Collector publishes none at all rather than zeroes.
func (m *MemRecorder) Directions(table string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]struct{}{}
	for _, src := range []map[string]map[string]float64{m.dirBacklog, m.dirOldest, m.dirDelivered} {
		for dir := range src[table] {
			seen[dir] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for dir := range seen {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func (m *MemRecorder) write(dst map[string]map[string]float64, table, direction string, v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dst[table] == nil {
		dst[table] = map[string]float64{}
	}
	dst[table][direction] = v
}

func (m *MemRecorder) readDir(src map[string]map[string]float64, table, direction string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return src[table][direction]
}

var (
	_ Recorder          = (*MemRecorder)(nil)
	_ DirectionRecorder = (*MemRecorder)(nil)
)

// QueueShape — КАК очередь помечает доставленную строку.
//
// # Зачем это объявляется, а не подразумевается
//
// Пакет заведён над одной формой (`sent_at IS NULL` + `attempt_count`) и читал
// её ЖЁСТКО. Очередь другой формы это делало ненаблюдаемой BY CONSTRUCTION:
// провязать сканер было можно, но первый же скан падал бы на «колонки sent_at
// нет» — то есть отсутствие наблюдаемости выглядело как её ненужность, а не как
// пропуск. Реальный случай — журнал аудита iam: 29 446 строк, ни одной попытки
// доставки за всё время жизни, и ни одной величины, которой это было бы видно.
//
// # Почему ЗАКРЫТЫЙ перечень, а не предикат строкой
//
// Предикат строкой из конфигурации дал бы гибкость ценой поверхности внедрения и
// ценой второго места, где форма очереди «объявлена». Форм в дереве две; каждая
// названа здесь вместе со своими тремя выражениями, и третья добавляется сюда, а
// не в вызывающего.
type QueueShape string

const (
	// ShapeSentAt — доставленность помечена ОТМЕТКОЙ ВРЕМЕНИ.
	// Пустая строка нарочно: конфигурация, написанная до появления этого типа,
	// продолжает означать ровно то, что означала.
	ShapeSentAt QueueShape = ""
	// ShapeStatus — доставленность помечена СОСТОЯНИЕМ (`status = 'sent'`),
	// число попыток лежит в `attempts`.
	ShapeStatus QueueShape = "status"
)

// queueShapeSQL — три выражения формы. Собраны в одном месте, чтобы «что считать
// недоставленным» и «что считать доставленным» не могли разойтись: они обязаны
// быть дополнением друг друга, и это проверяется пробой пакета.
type queueShapeSQL struct {
	pending   string
	delivered string
	attempts  string
}

func (s QueueShape) sql() (queueShapeSQL, error) {
	switch s {
	case ShapeSentAt:
		return queueShapeSQL{
			pending:   "sent_at IS NULL",
			delivered: "sent_at IS NOT NULL",
			attempts:  "attempt_count",
		}, nil
	case ShapeStatus:
		// `failed` — это BACKLOG, а не «обработана»: зачесть её в доставленные
		// значило бы объявить очередь разгруженной ровно тогда, когда доставка
		// перестала получаться.
		return queueShapeSQL{
			pending:   "status <> 'sent'",
			delivered: "status = 'sent'",
			attempts:  "attempts",
		}, nil
	}
	// Неизвестная форма — ЯВНЫЙ отказ, а не молчаливое умолчание: опечатка в
	// композиционном корне иначе публиковала бы числа, снятые не тем предикатом,
	// и они были бы неотличимы от верных.
	return queueShapeSQL{}, fmt.Errorf("metrics: неизвестная форма очереди %q", string(s))
}

// CollectorConfig parameterises a Collector.
type CollectorConfig struct {
	// Table — full outbox table name (`<schema>.<table>`), used both for the
	// scan query and as the metric `table` label.
	Table string
	// MaxAttempts — poison threshold; a pending row with attempt_count >=
	// MaxAttempts is counted as poisoned. Default 10 (matches drainer default).
	MaxAttempts int
	// Interval — how often Run scans (default 15s). Scan can also be called
	// directly (tests / on-demand).
	Interval time.Duration
	// Directions — OPTIONAL per-direction breakdown: direction name → the event_type
	// values that belong to it (e.g. {"grant": {"fga.register"}, "withdrawal":
	// {"fga.unregister"}}). Empty ⇒ no per-direction series at all.
	//
	// The names are supplied by the composition root and never by request data, so the
	// label set stays closed and cardinality cannot grow with traffic. The event_type
	// VALUES are matched as parameters, not interpolated.
	//
	// A direction is published even when it currently has no rows — that is the point:
	// `delivered_total{direction="withdrawal"} == 0` is the statement "not one withdrawal
	// has ever been delivered", and it can only be made by a series that exists.
	Directions map[string][]string
	// Shape — КАК эта очередь помечает доставленную строку. Пусто ⇒ ShapeSentAt,
	// форма, с которой пакет заведён: прежние вызывающие не меняются.
	Shape QueueShape
}

func (c CollectorConfig) withDefaults() CollectorConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	if c.Interval <= 0 {
		c.Interval = 15 * time.Second
	}
	return c
}

// Collector scans one outbox table and feeds the gauges into a Recorder.
type Collector struct {
	pool *pgxpool.Pool
	rec  Recorder
	cfg  CollectorConfig
}

// NewCollector constructs a Collector. pool/rec must be non-nil; Table required.
func NewCollector(pool *pgxpool.Pool, rec Recorder, cfg CollectorConfig) *Collector {
	return &Collector{pool: pool, rec: rec, cfg: cfg.withDefaults()}
}

// Scan runs one observation pass: it reads backlog depth, oldest pending age and
// the current poisoned-row count from the outbox table and records them. It does
// NOT mutate the table. The table name is a trusted literal supplied by the
// composition root (same contract as drainer.Config.Table) — not user input.
func (c *Collector) Scan(ctx context.Context) error {
	if c.pool == nil || c.rec == nil {
		return errors.New("metrics.Collector.Scan: pool and recorder required")
	}
	if c.cfg.Table == "" {
		return errors.New("metrics.Collector.Scan: Table required")
	}

	shape, serr := c.cfg.Shape.sql()
	if serr != nil {
		return fmt.Errorf("metrics.Collector.Scan %s: %w", c.cfg.Table, serr)
	}

	// One round-trip: pending count, oldest-pending age (seconds), poisoned count.
	q := fmt.Sprintf(`
		SELECT
		    count(*) FILTER (WHERE %[2]s)                                              AS backlog,
		    COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at) FILTER (WHERE %[2]s))), 0) AS oldest_age,
		    count(*) FILTER (WHERE %[2]s AND %[3]s >= $1)                              AS poisoned
		FROM %[1]s
	`, outbox.SanitizeTable(c.cfg.Table), shape.pending, shape.attempts)

	var backlog, poisoned int64
	var oldestAge float64
	if err := c.pool.QueryRow(ctx, q, c.cfg.MaxAttempts).Scan(&backlog, &oldestAge, &poisoned); err != nil {
		return fmt.Errorf("metrics.Collector.Scan %s: %w", c.cfg.Table, err)
	}

	c.rec.SetBacklogDepth(c.cfg.Table, float64(backlog))
	c.rec.SetOldestPendingAgeSeconds(c.cfg.Table, oldestAge)
	c.rec.SetPoisonedCount(c.cfg.Table, float64(poisoned))

	return c.scanDirections(ctx)
}

// scanDirections adds the per-direction series when the Collector is configured with a
// breakdown AND the recorder can accept one. Both conditions are checked rather than
// assumed: a recorder written before the split still satisfies Recorder, and the honest
// behaviour then is to publish nothing extra instead of failing the whole scan.
//
// The table-wide series above are untouched, so every threshold already written against
// them keeps meaning what it meant.
func (c *Collector) scanDirections(ctx context.Context) error {
	if len(c.cfg.Directions) == 0 {
		return nil
	}
	dirRec, ok := c.rec.(DirectionRecorder)
	if !ok {
		return nil
	}
	shape, serr := c.cfg.Shape.sql()
	if serr != nil {
		return fmt.Errorf("metrics.Collector.Scan %s directions: %w", c.cfg.Table, serr)
	}
	// ДОСТАВЛЕННЫЕ ЗДЕСЬ НЕ СЧИТАЮТСЯ — намеренно (#1714). Скан отвечает на
	// вопрос «что лежит», а «сколько доставлено за всё время» — вопрос о
	// событии; счёт по живым строкам совпадал с ним ровно до появления уборки.
	// Величину ведёт наблюдатель дренажа (`DeliveryObserver`).
	q := fmt.Sprintf(`
		SELECT
		    count(*) FILTER (WHERE %[2]s)                                              AS backlog,
		    COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at) FILTER (WHERE %[2]s))), 0) AS oldest_age
		FROM %[1]s
		WHERE event_type = ANY($1)
	`, outbox.SanitizeTable(c.cfg.Table), shape.pending)

	// Deterministic order so a scan reports its directions the same way every time.
	names := make([]string, 0, len(c.cfg.Directions))
	for name := range c.cfg.Directions {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		var backlog int64
		var oldestAge float64
		if err := c.pool.QueryRow(ctx, q, c.cfg.Directions[name]).
			Scan(&backlog, &oldestAge); err != nil {
			return fmt.Errorf("metrics.Collector.Scan %s direction %s: %w", c.cfg.Table, name, err)
		}
		dirRec.SetBacklogDepthByDirection(c.cfg.Table, name, float64(backlog))
		dirRec.SetOldestPendingAgeByDirection(c.cfg.Table, name, oldestAge)
	}
	return nil
}

// Run scans on c.cfg.Interval until ctx is cancelled. Scan errors are returned
// to the caller's logger via the optional onErr callback (nil → swallowed; the
// loop never dies on a transient scan error). Run blocks until ctx.Done().
//
// РЕПЛИКИ: на-реплику — проход только ЧИТАЕТ счётчики очереди и публикует их как показания
// своего процесса. Реплики публикуют одну и ту же величину под своими
// метками — это и есть штатная форма показания, а не дубль работы.
func (c *Collector) Run(ctx context.Context, onErr func(error)) {
	tick := time.NewTicker(c.cfg.Interval)
	defer tick.Stop()
	// Immediate first scan so metrics are populated before the first tick.
	if err := c.Scan(ctx); err != nil && onErr != nil {
		onErr(err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := c.Scan(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}

// Direction names published by RegisterOutboxDirections. Constants so a dashboard, an
// alert and the wiring all spell them the same way.
const (
	DirectionGrant      = "grant"
	DirectionWithdrawal = "withdrawal"
)

// Event types of the owner-registration outboxes. Every module that registers its
// resources through iam writes these two, so the breakdown below is one artefact rather
// than four hand-written copies — a list kept in four places has already been observed in
// this tree to diverge from itself.
const (
	EventFGARegister   = "fga.register"
	EventFGAUnregister = "fga.unregister"
)

// Event types of the iam tuple outbox. Same two directions, different vocabulary: the
// modules ask iam to REGISTER a resource, iam itself writes and deletes the individual
// tuples. The direction NAMES are deliberately the same in both, so one dashboard panel
// and one alert cover every queue on the platform.
const (
	EventFGATupleWrite  = "fga.tuple.write"
	EventFGATupleDelete = "fga.tuple.delete"
)

// RegisterOutboxDirections is the CollectorConfig.Directions value for an owner-
// registration outbox: grants and withdrawals reported apart.
//
// Withdrawal is the half that goes wrong quietly. A grant that fails to arrive is
// reported by the resource itself — the creator cannot see what they just made, and
// somebody opens a ticket within the hour. A withdrawal that fails to arrive produces no
// symptom at all: the access simply stays, and "it works" is indistinguishable from "it
// was never revoked" until someone asks the store directly.
//
// Returns a fresh map per call so a caller cannot mutate the shared answer.
func RegisterOutboxDirections() map[string][]string {
	return map[string][]string{
		DirectionGrant:      {EventFGARegister},
		DirectionWithdrawal: {EventFGAUnregister},
	}
}

// TupleOutboxDirections is the CollectorConfig.Directions value for the iam tuple outbox
// (`kaname.fga_outbox`): tuple writes and tuple deletes reported apart.
//
// This queue is the one where the asymmetry bites hardest, because it is where revocation
// physically happens: every AccessBinding removal, every group-member removal and every
// delete-stale of the reconciler leaves through it. Its own drainer configuration already
// spells out that writes and deletes of the SAME tuple are not commutative and that a
// delete overtaking its predecessor write makes the tuple survive the revoke. That is a
// statement about ORDER; this is the statement about ARRIVAL, and no aggregate series can
// make it: grants flow continuously, so depth stays small and the head stays young no
// matter whether a single delete ever lands.
//
// Returns a fresh map per call so a caller cannot mutate the shared answer.
func TupleOutboxDirections() map[string][]string {
	return map[string][]string{
		DirectionGrant:      {EventFGATupleWrite},
		DirectionWithdrawal: {EventFGATupleDelete},
	}
}
