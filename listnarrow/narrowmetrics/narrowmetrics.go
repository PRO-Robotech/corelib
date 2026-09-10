// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package narrowmetrics — ЕДИНСТВЕННЫЙ коллектор величин сужателя списков.
//
// # Почему одна реализация на пятерых
//
// Сужатель в этом дереве один (`pkg/listnarrow`), потому что пять копий одного
// решения о видимости разъезжаются молча. Ровно та же причина держит единым и
// его наблюдение: пять коллекторов с одинаковыми на вид именами серий — это
// пять мест, где полосу можно забыть, переименовать или сложить не с тем. Здесь
// имя семейства собирается из имени сервиса по одному правилу, а словарь
// исходов закрыт константами.
//
// # Что различают четыре полосы
//
//   - `narrowed` — страница сужена ШТАТНО;
//   - `breakglass` — ушла несуженной по аварийному режиму;
//   - `softpass_misconfigured` — ушла несуженной на ответе, ДОКАЗЫВАЮЩЕМ неверную
//     настройку (сам такой отказ не пройдёт никогда);
//   - `softpass_transient` — ушла несуженной на отказе, который может пройти сам.
//
// Три последние означают страницу, ушедшую без пообъектной проверки, и на боевой
// посадке обязаны быть нулями: аварийный режим и мягкий проход там запрещены
// отказом старта. Первая нужна, чтобы эти нули отличались от «сужателя не звали
// ни разу» — без неё три нуля утверждали бы благополучие там, где защита просто
// не исполняется.
//
// # Величины читаются ИЗ ПРОЦЕССА
//
// Коллектор не делает внешних вызовов в момент сбора: он зовёт функцию, которую
// принёс композиционный корень, а та читает атомарные счётчики сужателя.
package narrowmetrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/PRO-Robotech/corelib/listnarrow"
)

// Значения метки исхода — ЗАКРЫТЫЙ словарь. Ни одно не берётся из данных
// запроса: число различных серий не растёт с числом обслуженных арендаторов.
const (
	outcomeNarrowed              = "narrowed"
	outcomeBreakglass            = "breakglass"
	outcomeSoftPassMisconfigured = "softpass_misconfigured"
	outcomeSoftPassTransient     = "softpass_transient"
)

// MetricName — имя семейства для названного сервиса.
//
// Отдельная функция, а не литерал по месту: имя серии — контракт с панелями и
// правилами тревог, и собирать его в пяти местах значило бы завести пять
// контрактов, которые разъедутся на первом же переименовании.
func MetricName(service string) string {
	return "kacho_" + service + "_list_narrow_pages_total"
}

// Collector — коллектор величин сужателя одного сервиса.
type Collector struct {
	desc *prometheus.Desc
	read func() listnarrow.Counts
}

// New собирает коллектор для сервиса `service` (`vpc`, `compute`, `nlb`,
// `storage`, `registry`) поверх читателя величин.
//
// `read == nil` означает, что сужателя на этой посадке нет вовсе; коллектор
// всё равно отдаёт ВСЕ четыре полосы нулями. Исчезновение серий сообщило бы
// собирателю не «сужений не было», а ничего.
func New(service string, read func() listnarrow.Counts) *Collector {
	if read == nil {
		read = func() listnarrow.Counts { return listnarrow.Counts{} }
	}
	return &Collector{
		desc: prometheus.NewDesc(
			MetricName(service),
			"List pages that went through the per-object visibility filter, by outcome. "+
				"Everything except outcome=narrowed is a page returned WITHOUT per-object "+
				"authorization; on a production posture those three are refused at startup and "+
				"must therefore read zero. outcome=narrowed is what tells a zero apart from a "+
				"filter that was never called.",
			[]string{"outcome"}, nil),
		read: read,
	}
}

// Describe объявляет семейство до первого сбора — серии присутствуют нулями с
// первой секунды жизни процесса.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

// Collect отдаёт все четыре полосы, включая нулевые.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	counts := c.read()
	// Обход ЛИТЕРАЛЬНОГО набора, ключи которого — константы этого файла: словарь
	// меток закрыт по построению, и это же читает гейт дерева
	// `TestDiagnosticCollectorLabelsAreAClosedVocabulary`.
	for outcome, value := range map[string]uint64{
		outcomeNarrowed:              counts.Narrowed,
		outcomeBreakglass:            counts.Breakglass,
		outcomeSoftPassMisconfigured: counts.SoftPassMisconfigured,
		outcomeSoftPassTransient:     counts.SoftPassTransient,
	} {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.CounterValue,
			float64(value), outcome)
	}
}

// Compile-time: реализуем контракт коллектора. Потеря метода в рефакторинге не
// сломала бы сборку у вызывающего, который регистрирует нас интерфейсом, —
// она тихо перестала бы публиковать единственные серии, говорящие, сужаются ли
// страницы вообще.
var _ prometheus.Collector = (*Collector)(nil)
