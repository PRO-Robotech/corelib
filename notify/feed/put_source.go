// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/subscription"
)

// Signal — писатель строки журнала подписки владельца по объекту
// notification_feed:<модуль> (З7, шаг 6; NTF1-B01). Пишет тем же писателем
// журнала, что прочие строки подписки владельца, в транзакции постановки.
type Signal interface {
	SignalFeed(ctx context.Context, tx pgx.Tx) error
}

// Sealer — запечатывание секретных атрибутов строки ключом кольца процесса
// (З11). AAD — таблица, id строки и шаблон: шифротекст, перенесённый в чужую
// строку или под чужой шаблон, не открывается. Кольцо и его ключи — полоса
// сервера ленты; Put зовёт его через этот порт.
type Sealer interface {
	Seal(table, id, template string, plaintext []byte) ([]byte, error)
}

// JournalKey — ключ журнала подписки владельца для ленты; на проводе — вид
// notification_feed.
const JournalKey = "notification"

// journalSignal пишет строку журнала функцией фундамента Journal.Emit.
type journalSignal struct {
	journal subscription.Journal
	entry   subscription.Entry
}

// JournalSignal — Signal над журналом подписки владельца: строка вида
// JournalKey, объект — модуль, род изменения change словом владельца.
// Объявление судится при сборке корня: вид без NameFormNone и ScopeCluster
// либо род вне словаря — отказ старта, а не отказ первой постановки.
func JournalSignal(j subscription.Journal, module, change string) (Signal, error) {
	if err := j.Validate(); err != nil {
		return nil, fmt.Errorf("feed: журнал модуля %s: %w", module, err)
	}
	kind, ok := j.Mapping.Kinds[JournalKey]
	if !ok {
		return nil, fmt.Errorf("feed: журнал модуля %s не объявил ключ %q", module, JournalKey)
	}
	if kind.Scope != subscription.ScopeCluster || kind.NameForm != subscription.NameFormNone {
		return nil, fmt.Errorf("feed: ключ %q журнала модуля %s обязан быть уровня кластера без имени", JournalKey, module)
	}
	if _, ok := j.Mapping.Changes[change]; !ok {
		return nil, fmt.Errorf("feed: род изменения %q вне словаря журнала модуля %s", change, module)
	}
	if !moduleForm.MatchString(module) {
		return nil, fmt.Errorf("feed: имя модуля %q не DNS-метка", module)
	}
	return journalSignal{journal: j, entry: subscription.Entry{Kind: JournalKey, ID: module, Change: change}}, nil
}

func (s journalSignal) SignalFeed(ctx context.Context, tx pgx.Tx) error {
	jt, ok := tx.(*journaltx.Tx)
	if !ok {
		return subscription.ErrNotHelperTx
	}
	return s.journal.Emit(ctx, jt, s.entry)
}

// moduleForm — имя модуля: DNS-метка (объект notification_feed:<модуль>,
// метка метрик).
var moduleForm = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Config — то, что корень источника передаёт ленте, разобрав ручки один раз.
type Config struct {
	// Module — имя модуля: объект notification_feed:<Module>, метка метрик.
	Module string
	// Service — префикс таблиц ленты (<Service>_notification_outbox).
	Service string
	// Enabled — флаг источника; нулевое значение не принимается.
	Enabled Enabled
	// Signal — писатель строки журнала подписки (JournalSignal).
	Signal Signal
	// Sealer — запечатывание секретных атрибутов; обязателен при включённом
	// флаге.
	Sealer Sealer
	// Metrics — регистратор метрик источника.
	Metrics prometheus.Registerer
}

// Source — лента источника: флаг, писатель журнала, запечатывание, метрики.
// Строит только NewSource.
type Source struct {
	module  string
	service string
	enabled bool
	signal  Signal
	sealer  Sealer
	gauge   prometheus.Gauge
	defects *prometheus.CounterVec
}

// NewSource судит конфигурацию и регистрирует метрики источника:
// kacho_notifications_enabled{module} (NTF1-N09) и
// kacho_notification_feed_put_defects_total{module, cause} (CX1-67).
func NewSource(cfg Config) (*Source, error) {
	if !moduleForm.MatchString(cfg.Module) {
		return nil, fmt.Errorf("feed: Config.Module %q не DNS-метка", cfg.Module)
	}
	if err := tablename.Valid(cfg.Service); err != nil {
		return nil, fmt.Errorf("feed: Config.Service модуля %s: %w", cfg.Module, err)
	}
	if !cfg.Enabled.Set() {
		return nil, fmt.Errorf("feed: Config.Enabled модуля %s не разобран ParseEnabled", cfg.Module)
	}
	if cfg.Signal == nil {
		return nil, fmt.Errorf("feed: Config.Signal модуля %s не задан — строка ленты без сигнала подписки", cfg.Module)
	}
	if cfg.Enabled.On() && cfg.Sealer == nil {
		return nil, fmt.Errorf("feed: Config.Sealer модуля %s не задан при включённом флаге", cfg.Module)
	}
	if cfg.Metrics == nil {
		return nil, fmt.Errorf("feed: Config.Metrics модуля %s не задан", cfg.Module)
	}
	gaugeVec, err := registerOrReuse(cfg.Metrics, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kacho_notifications_enabled",
		Help: "Флаг доставки извещений источника: 1 — включён, 0 — выключен.",
	}, []string{"module"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика флага модуля %s: %w", cfg.Module, err)
	}
	defects, err := registerOrReuse(cfg.Metrics, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kacho_notification_feed_put_defects_total",
		Help: "Дефекты программы вызывающего, отвергнутые Put; ноль за всю жизнь — норма.",
	}, []string{"module", "cause"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика дефектов модуля %s: %w", cfg.Module, err)
	}
	g := gaugeVec.WithLabelValues(cfg.Module)
	if cfg.Enabled.On() {
		g.Set(1)
	} else {
		g.Set(0)
	}
	return &Source{
		module: cfg.Module, service: cfg.Service, enabled: cfg.Enabled.On(),
		signal: cfg.Signal, sealer: cfg.Sealer, gauge: g, defects: defects,
	}, nil
}

func registerOrReuse[C prometheus.Collector](reg prometheus.Registerer, c C) (C, error) {
	if err := reg.Register(c); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			if existing, ok := already.ExistingCollector.(C); ok {
				return existing, nil
			}
		}
		return c, err
	}
	return c, nil
}

// Enabled — значение флага источника.
func (s *Source) Enabled() bool { return s.enabled }

// EnabledGauge — серия kacho_notifications_enabled этого модуля.
func (s *Source) EnabledGauge() prometheus.Gauge { return s.gauge }

// DefectCounter — kacho_notification_feed_put_defects_total.
func (s *Source) DefectCounter() *prometheus.CounterVec { return s.defects }

// DeliveryConfigured — первый вопрос глагола, ставящего письмо (З12,
// NTF1-N06): ErrDeliveryNotConfigured при выключенном флаге. Проверка стоит до
// чтения адреса; Put проверяет флаг и сам — это второй рубеж.
func (s *Source) DeliveryConfigured() error {
	if !s.enabled {
		return ErrDeliveryNotConfigured
	}
	return nil
}

type sourceKey struct{}

// Bind кладёт источник в контекст запроса: Put и порождённые SendX берут его
// оттуда. Корень источника привязывает его на входе каждого пути, ставящего
// письма (перехватчик запроса, фоновый путь).
func (s *Source) Bind(ctx context.Context) context.Context {
	return context.WithValue(ctx, sourceKey{}, s)
}

func sourceFrom(ctx context.Context) (*Source, bool) {
	s, ok := ctx.Value(sourceKey{}).(*Source)
	return s, ok && s != nil
}
