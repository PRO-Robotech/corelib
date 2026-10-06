// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"fmt"
	"sort"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// metrics — метрики ленты источника (З27). Значения меток — закрытые перечни
// Classes, Kinds, Reasons, PutDefects; свободной строки в метке нет: серию
// увеличивают только типизированные функции observe*.
//
// Серии заводятся на каждую законную клетку при сборке: «ноль за всю жизнь»
// отличим от «серии нет» (NTF1-B20), и тревога видит клетку до первого исхода.
type metrics struct {
	module    string
	outcomes  *prometheus.CounterVec
	delivered prometheus.Counter
	defers    *prometheus.CounterVec
	oldest    *prometheus.GaugeVec
	deferred  *prometheus.GaugeVec
	defects   *prometheus.CounterVec
	// suppressed — notify_suppressed_total{ns,template,reason,scope} (NTF-3
	// Р14): строки, не поставленные лимитом шаблона, после коммита
	// транзакции вызывающего. Серия шаблона заводится первым подавлением:
	// перечня шаблонов источник при сборке не знает.
	suppressed *prometheus.CounterVec
}

// SuppressReasonLimit — единственная причина подавления постановки у
// источника: окно лимита шаблона исчерпано.
const SuppressReasonLimit = "limit"

// newMetrics регистрирует метрики модуля в reg; повторная регистрация того же
// семейства (сервер, уборщик и источник одного процесса) берёт существующее.
func newMetrics(reg prometheus.Registerer, module string) (*metrics, error) {
	outcomes, err := registerOrReuse(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kacho_notification_feed_outcomes_total",
		Help: "Исходы строк ленты по клеткам Р11: класс, вид, причина (пустая — причины нет).",
	}, []string{"module", "class", "kind", "reason"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика исходов модуля %s: %w", module, err)
	}
	delivered, err := registerOrReuse(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kacho_notification_feed_delivered_total",
		Help: "Строки ленты, принятые ретранслятором (Ack SENT), за жизнь процесса.",
	}, []string{"module"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика доставленных модуля %s: %w", module, err)
	}
	defers, err := registerOrReuse(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kacho_notification_feed_defers_total",
		Help: "Отсрочки строк ленты (Ack DEFER) по причине.",
	}, []string{"module", "reason"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика отсрочек модуля %s: %w", module, err)
	}
	oldest, err := registerOrReuse(reg, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kacho_notification_feed_oldest_pending_seconds",
		Help: "Возраст старейшей строки ленты в состоянии pending по классу; 0 — таких строк нет.",
	}, []string{"module", "class"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика возраста модуля %s: %w", module, err)
	}
	deferred, err := registerOrReuse(reg, prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kacho_notification_feed_deferred",
		Help: "Строки ленты в состоянии pending, чья отсрочка ещё не прошла, по классу.",
	}, []string{"module", "class"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика отсроченных модуля %s: %w", module, err)
	}
	defects, err := registerOrReuse(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kacho_notification_feed_put_defects_total",
		Help: "Дефекты программы вызывающего, отвергнутые Put; ноль за всю жизнь — норма.",
	}, []string{"module", "cause"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика дефектов модуля %s: %w", module, err)
	}

	suppressed, err := registerOrReuse(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "notify_suppressed_total",
		Help: "Строки ленты, не поставленные лимитом шаблона, — после коммита транзакции вызывающего; scope — исчерпанное окно (оба — recipient).",
	}, []string{"ns", "template", "reason", "scope"}))
	if err != nil {
		return nil, fmt.Errorf("feed: метрика подавлений модуля %s: %w", module, err)
	}

	m := &metrics{
		module: module, outcomes: outcomes, delivered: delivered.WithLabelValues(module),
		defers: defers, oldest: oldest, deferred: deferred, defects: defects, suppressed: suppressed,
	}
	for _, c := range Classes() {
		for _, o := range cells() {
			m.outcomes.WithLabelValues(module, string(c), string(o.Kind), string(o.Reason))
		}
		m.oldest.WithLabelValues(module, string(c))
		m.deferred.WithLabelValues(module, string(c))
	}
	for _, r := range ackReasons()[KindDefer] {
		m.defers.WithLabelValues(module, string(r))
	}
	for _, d := range PutDefects() {
		m.defects.WithLabelValues(module, string(d))
	}
	return m, nil
}

// cells — законные клетки исхода: каждое терминальное состояние таблицы схемы
// со своими причинами (без причин — одна клетка с ReasonNone), кроме
// LocalOnlyOutcomes, и DEFER с причинами, которые принимает Ack.
func cells() []Outcome {
	pairs := schema.OutcomePairs()
	var out []Outcome
	for state, rs := range pairs {
		if state == "pending" {
			continue
		}
		if len(rs) == 0 {
			out = append(out, Outcome{Kind: Kind(state)})
			continue
		}
		for _, r := range rs {
			if o := (Outcome{Kind: Kind(state), Reason: Reason(r)}); !localOnly(o) {
				out = append(out, o)
			}
		}
	}
	for _, r := range ackReasons()[KindDefer] {
		out = append(out, Outcome{Kind: KindDefer, Reason: r})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// observeOutcome — исход одной строки, изменённой оператором записи. На
// успешном повторе Ack не зовётся (CX1-26 (д)).
func (m *metrics) observeOutcome(c Class, o Outcome) {
	m.outcomes.WithLabelValues(m.module, string(c), string(o.Kind), string(o.Reason)).Inc()
	switch o.Kind {
	case KindSent:
		m.delivered.Inc()
	case KindDefer:
		m.defers.WithLabelValues(m.module, string(o.Reason)).Inc()
	case KindRecipientRejected, KindDenied, KindInvalid, KindDropped, KindExpired, KindSuppressed, KindSuperseded:
		// Своего счётчика сверх клетки исхода у этих видов нет.
	}
}

// observePutDefect — дефект вызывающего, отвергнутый Put (CX1-67).
func (m *metrics) observePutDefect(d PutDefect) {
	m.defects.WithLabelValues(m.module, string(d)).Inc()
}

// observeSuppressed — одно подавление лимитом шаблона template в области
// scope; зовёт только хук после коммита (suppressedAfterCommit).
func (m *metrics) observeSuppressed(template string, scope Scope) {
	m.suppressed.WithLabelValues(m.module, template, SuppressReasonLimit, string(scope)).Inc()
}

// observePending — возраст старейшей строки pending и число отсроченных по
// классу. Класс без строк — 0.
func (m *metrics) observePending(oldest map[Class]float64, deferred map[Class]int) {
	for _, c := range Classes() {
		m.oldest.WithLabelValues(m.module, string(c)).Set(oldest[c])
		m.deferred.WithLabelValues(m.module, string(c)).Set(float64(deferred[c]))
	}
}
