// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/retention"
)

// Константы уборщика истечения (З10). Объявлены однажды, ручек нет.
const (
	// SweepInterval — период прохода истечения.
	SweepInterval = time.Minute
	// SweepBatch — пачка оператора истечения. Своя величина, с пачкой петли
	// corelib/retention не связана.
	SweepBatch = 1000
)

// sweepCallTimeout — свой срок одного оператора уборки (arch-per-call-deadline):
// пачка истечения (SweepBatch строк и замки их окон) либо партия уборки. Меньше
// SweepInterval: зависший оператор не съедает следующий проход.
const sweepCallTimeout = 30 * time.Second

// expireSQL — один оператор решает причину истечения и возврат вклада
// (З10, CX1-53 (б), CX1-16):
//
//   - x — перевод в EXPIRED пачки строк, взятых под замок с SKIP LOCKED;
//     условие аренды стоит в том же операторе, что перевод (NTF1-B25);
//     причина — последняя отсрочка, иначе unclaimed или no_ack по монотонному
//     признаку выдачи;
//   - d — вклад переведённых строк с причиной возврата, сведённый по ключу
//     окна до обновления (CX1-62): UPDATE … FROM меняет строку окна один раз;
//   - l — замки строк окна в полном порядке ключа окна, текстовые части
//     побайтно (CX1-63, М26); MATERIALIZED не даёт встроить шаг в соединение;
//   - u — возврат вклада по замкнутым строкам.
//
// Итог — счёт переведённых строк по классу и причине (для метрик исходов).
// Возврат идёт только по строкам, которые перевёл ЭТОТ оператор, — два
// уборщика возвращают вклад ровно один раз (CX1-55 (б)).
func expireSQL(svc string) string {
	return fmt.Sprintf(`WITH x AS (
  UPDATE %[1]s SET state = 'expired',
    outcome_reason = coalesce(last_defer_reason,
                              CASE WHEN first_claimed_at IS NULL THEN 'unclaimed' ELSE 'no_ack' END),
    outcome_token = NULL, recorded_kind = NULL, recorded_reason = NULL,
    secret_attrs = NULL, outcome_at = now(), lease_token = NULL, lease_until = NULL
  WHERE id IN (SELECT id FROM %[1]s
                WHERE state = 'pending' AND expires_at <= now()
                  AND (lease_until IS NULL OR lease_until <= now())
                LIMIT $1 FOR UPDATE SKIP LOCKED)
  RETURNING id, class, outcome_reason),
d AS (
  SELECT c.template, c.scope, c.window_seconds, c.key, c.window_start, count(*) AS n
    FROM %[2]s c JOIN x ON c.notification_id = x.id
   WHERE x.outcome_reason = ANY($2)
   GROUP BY c.template, c.scope, c.window_seconds, c.key, c.window_start),
l AS MATERIALIZED (
  SELECT w.template, w.scope, w.window_seconds, w.key, w.window_start, d.n
    FROM %[3]s w JOIN d
      ON (w.template, w.scope, w.window_seconds, w.key, w.window_start)
       = (d.template, d.scope, d.window_seconds, d.key, d.window_start)
   ORDER BY w.template COLLATE "C", w.scope COLLATE "C", w.window_seconds,
            w.key COLLATE "C", w.window_start
     FOR UPDATE OF w),
u AS (
  UPDATE %[3]s w SET count = w.count - l.n
    FROM l
   WHERE (w.template, w.scope, w.window_seconds, w.key, w.window_start)
       = (l.template, l.scope, l.window_seconds, l.key, l.window_start)
  RETURNING 1)
SELECT x.class, x.outcome_reason, count(*)::int, (SELECT count(*) FROM u)::int
  FROM x GROUP BY x.class, x.outcome_reason`,
		tablename.Of(svc, tablename.Outbox), tablename.Of(svc, tablename.Contrib), tablename.Of(svc, tablename.Window))
}

// pendingSQL — наблюдение ленты (NTF1-B20): возраст старейшей строки pending
// и число строк, чья отсрочка ещё не прошла, по классу. Строка без отсрочки
// (not_before IS NULL) в отсроченные не входит (УК75).
func pendingSQL(svc string) string {
	return fmt.Sprintf(`SELECT class,
       coalesce(extract(epoch FROM now() - min(enqueued_at)), 0)::float8,
       (count(*) FILTER (WHERE not_before IS NOT NULL AND not_before > now()))::int
  FROM %s WHERE state = 'pending' GROUP BY class`, tablename.Of(svc, tablename.Outbox))
}

// SweeperConfig — то, что корень источника передаёт уборщику. Флага здесь нет
// намеренно: уборщик работает при любом его значении (CX1-21, NTF1-B31 (в)).
type SweeperConfig struct {
	// Module — имя модуля, метка метрик.
	Module string
	// Service — префикс таблиц ленты.
	Service string
	// DB — пул источника.
	DB DB
	// Metrics — регистратор метрик ленты.
	Metrics prometheus.Registerer
	// Log — журнал; nil — молчащий.
	Log *slog.Logger
}

// Sweeper — уборщик ленты: истечение с возвратом вклада и наблюдение ленты
// каждые SweepInterval, уборка закрытых строк и прошедших окон — петлёй
// corelib/retention (З10).
type Sweeper struct {
	module   string
	service  string
	db       DB
	metrics  *metrics
	log      *slog.Logger
	refunds  []string
	retainer *retention.Sweeper

	stopped chan struct{}
}

// NewSweeper судит конфигурацию и собирает уборщика без петель: Pass
// исполняет проход истечения и наблюдения сразу (проверяемо без тикера).
func NewSweeper(cfg SweeperConfig) (*Sweeper, error) {
	if !moduleForm.MatchString(cfg.Module) {
		return nil, fmt.Errorf("feed: SweeperConfig.Module %q не DNS-метка", cfg.Module)
	}
	if err := tablename.Valid(cfg.Service); err != nil {
		return nil, fmt.Errorf("feed: SweeperConfig.Service модуля %s: %w", cfg.Module, err)
	}
	if cfg.DB == nil {
		return nil, fmt.Errorf("feed: SweeperConfig.DB модуля %s не задан", cfg.Module)
	}
	if cfg.Metrics == nil {
		return nil, fmt.Errorf("feed: SweeperConfig.Metrics модуля %s не задан", cfg.Module)
	}
	m, err := newMetrics(cfg.Metrics, cfg.Module)
	if err != nil {
		return nil, err
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	retainer, err := retention.New(retention.DefaultConfig(), RetentionSubjects(cfg.DB, cfg.Service), log)
	if err != nil {
		return nil, fmt.Errorf("feed: уборка ленты модуля %s: %w", cfg.Module, err)
	}
	refunds := make([]string, 0, len(RefundReasons()))
	for _, r := range RefundReasons() {
		refunds = append(refunds, string(r))
	}
	return &Sweeper{
		module: cfg.Module, service: cfg.Service, db: cfg.DB, metrics: m, log: log,
		refunds: refunds, retainer: retainer, stopped: make(chan struct{}),
	}, nil
}

// StartSweeper собирает уборщика и поднимает все три прохода — истечение,
// закрытые строки, прошедшие окна (CX1-55 (а)). Корень зовёт его рядом со
// схемой ленты, вне ветки регистрации сервера и вне вопроса о флаге. Первый
// проход каждой петли идёт сразу. Петли живут до отмены ctx; Wait дожидается
// их завершения.
func StartSweeper(ctx context.Context, cfg SweeperConfig) (*Sweeper, error) {
	s, err := NewSweeper(cfg)
	if err != nil {
		return nil, err
	}
	s.retainer.Start(ctx)
	go s.loop(ctx)
	return s, nil
}

func (s *Sweeper) loop(ctx context.Context) {
	defer close(s.stopped)
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for {
		if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "notification feed expiry pass failed",
				slog.String("module", s.module), slog.String("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Wait ждёт завершения петель после отмены контекста StartSweeper и отвечает,
// дождался ли за d.
func (s *Sweeper) Wait(d time.Duration) bool {
	deadline := time.Now().Add(d)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.stopped:
	case <-t.C:
		return false
	}
	return s.retainer.Wait(time.Until(deadline))
}

// Pass — один проход истечения и наблюдения. Истечение повторяется пачками
// SweepBatch до нуля переведённых: за проход закрывается каждая строка,
// подлежащая закрытию (NTF1-B31). Ошибка наблюдения не отменяет истечения.
func (s *Sweeper) Pass(ctx context.Context) error {
	var errs []error
	for ctx.Err() == nil {
		n, err := s.expireBatch(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("feed: истечение модуля %s: %w", s.module, err))
			break
		}
		if n == 0 {
			break
		}
	}
	if err := s.observe(ctx); err != nil {
		errs = append(errs, fmt.Errorf("feed: наблюдение ленты модуля %s: %w", s.module, err))
	}
	return errors.Join(errs...)
}

// expireBatch — один оператор истечения; возвращает число переведённых строк.
func (s *Sweeper) expireBatch(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, sweepCallTimeout)
	defer cancel()
	rows, err := s.db.Query(ctx, expireSQL(s.service), SweepBatch, s.refunds)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type cell struct {
		class  Class
		reason Reason
		n      int
	}
	var (
		got   []cell
		total int
	)
	for rows.Next() {
		var c cell
		var class, reason string
		var refunded int
		if err := rows.Scan(&class, &reason, &c.n, &refunded); err != nil {
			return 0, err
		}
		c.class, c.reason = Class(class), Reason(reason)
		got = append(got, c)
		total += c.n
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Метрики — после завершения оператора: строки переведены и закоммичены.
	for _, c := range got {
		for range c.n {
			s.metrics.observeOutcome(c.class, Outcome{Kind: KindExpired, Reason: c.reason})
		}
	}
	return total, nil
}

// observe — наблюдение ленты: возраст старейшей строки pending и отсроченные.
func (s *Sweeper) observe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, storeCallTimeout)
	defer cancel()
	rows, err := s.db.Query(ctx, pendingSQL(s.service))
	if err != nil {
		return err
	}
	defer rows.Close()
	oldest := map[Class]float64{}
	deferred := map[Class]int{}
	for rows.Next() {
		var class string
		var age float64
		var n int
		if err := rows.Scan(&class, &age, &n); err != nil {
			return err
		}
		oldest[Class(class)] = age
		deferred[Class(class)] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.metrics.observePending(oldest, deferred)
	return nil
}
