// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package authzmetrics — ЕДИНСТВЕННЫЙ коллектор величин кеша положительных
// вердиктов.
//
// # Почему одна реализация на всех
//
// Кеш вердиктов в этом дереве один (`pkg/authz.Cache`), и строит его один
// носитель контура на все сервисы. Ровно та же причина держит единым и его
// наблюдение: шесть коллекторов с одинаковыми на вид именами серий — это шесть
// мест, где полосу можно забыть, переименовать или сложить не с той. Здесь имя
// семейства собирается из имени сервиса по одному правилу, а словари полос и
// причин вытеснения закрыты константами.
//
// # Имена ОДНОРОДНЫ краю, а не изобретены заново
//
// Край уже выставляет `kacho_api_gateway_authz_cache_total{result="hit"|"miss"}`
// (`gateway/internal/observability/metrics`). Здесь то же семейство и та же
// метка исхода, только имя сервиса другое: собиратель, у которого уже есть
// правило на край, читает сервисы тем же выражением. Расходиться в написании
// значило бы завести второй контракт с панелями и правилами тревог.
//
// # Полоса — потому что кешей в процессе бывает больше одного
//
// У registry их два: окно звена решения (вопрос на вызов) и окно прямого
// пообъектного опроса страницы. Сложить их в одну серию значило бы сделать
// невидимым тот из них, который не попадает, — а это ровно тот, ради которого
// величину и смотрят. Полоса объявляется ВЫЗЫВАЮЩИМ: процесс без второго кеша
// вторую полосу не объявляет и нулями её не рисует, иначе экспозиция утверждала
// бы существование окна, которого в этом процессе нет.
//
// # Доли попаданий здесь НЕТ
//
// Доля, посчитанная в процессе за всё время жизни, на поверхности сбора
// бесполезна: её нельзя ни продифференцировать по времени, ни сложить по
// репликам. Потребитель считает её из попаданий и промахов сам, а второе место
// об одном предмете разъезжается молча.
//
// # Величины читаются ИЗ ПРОЦЕССА
//
// Коллектор не делает ни одного внешнего вызова в момент сбора: он зовёт
// функцию, которую принёс композиционный корень, а та читает счётчики кеша.
// Диагностика, которая ходит наружу за своими числами, гаснет ровно тогда, когда
// нужна.
package authzmetrics

import (
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/PRO-Robotech/corelib/authz"
)

// Полосы окна вердиктов — ЗАКРЫТЫЙ словарь. Ни одно значение метки не берётся из
// данных запроса: число различных серий не растёт с числом обслуженных
// арендаторов (потолок кардинальности и запрет `security.md` §«Инфра-чувствительные
// данные» суть один запрет с двух сторон).
const (
	// LaneRPC — окно звена решения о доступе: один вопрос на вызов, оба
	// слушателя.
	LaneRPC = "rpc"
	// LaneList — окно, которое держит САМ СЕРВИС перед сужателем на пути страницы.
	// Сегодня такое есть у registry (`handler.cachedAuthorizer`): страница
	// контрактно бывает до тысячи элементов, и каждый её элемент — отдельный
	// вопрос.
	LaneList = "list"
	// LaneNarrow — окно ВНУТРИ общего сужателя (`pkg/listnarrow`).
	//
	// Третья полоса, а не переиспользованная `LaneList`, и причина не
	// стилистическая: у registry эти два окна стоят ДРУГ ЗА ДРУГОМ на одном пути
	// (свой кеш впереди, окно сужателя позади него), поэтому под одной меткой они
	// сложились бы — а сложить их значит сделать невидимым то из них, которое не
	// попадает, ровно как сказано абзацем выше про два кеша процесса. Полоса
	// называет МЕХАНИЗМ, чьё окно она описывает, а не форму вопроса: форма у них
	// одна, а механизма два.
	LaneNarrow = "narrow"
)

// Значения метки исхода — те же, что у края.
const (
	resultHit  = "hit"
	resultMiss = "miss"
)

// Значения метки решения — ЗАКРЫТЫЙ словарь.
//
// Шесть, а не четыре как у края: у края «проверка не состоялась» разводится на
// отклонён/пропущен, а здесь исходов больше и они РАЗНЫЕ по смыслу — недоступность
// владельца модели, аварийный режим, метод без строки каталога и срабатывание
// отсечки шторма. Сводить их к чужой четвёрке значило бы потерять ровно то, ради
// чего они заведены: аварийный режим и метод без каталога на боевой посадке
// обязаны быть нулями, и их ноль обязан быть отличим от «звено не спрашивали».
const (
	decisionAllowed     = "allowed"
	decisionDenied      = "denied"
	decisionUnavailable = "unavailable"
	decisionBreakglass  = "breakglass"
	decisionUnmapped    = "unmapped"
	decisionRateLimited = "rate_limited"
)

// Причины вытеснения — ЗАКРЫТЫЙ словарь. Три, а не одна: истечение окна есть
// штатная работа, давление потолка есть сигнал, что кеша не хватает на нагрузку,
// снятие есть единственный проактивный путь. Сложенные, они объявили бы
// исчерпание потолка нормой.
const (
	reasonExpired     = "expired"
	reasonCapacity    = "capacity"
	reasonInvalidated = "invalidated"
)

// Reader — читатель величин ОДНОГО окна вердиктов.
type Reader func() authz.CacheStats

// DecisionReader — читатель величин ЗВЕНА решения. Одно на процесс: звено у
// сервиса ровно одно, и оба слушателя проходят через него.
type DecisionReader func() authz.Metrics

// MetricName / EntriesMetricName / EvictionsMetricName / DecisionsMetricName —
// имена семейств для названного сервиса.
//
// Отдельные функции, а не литералы по месту: имя серии — контракт с панелями и
// правилами тревог, и собирать его в шести местах значило бы завести шесть
// контрактов, которые разъедутся на первом же переименовании.
func MetricName(service string) string        { return "kacho_" + service + "_authz_cache_total" }
func EntriesMetricName(service string) string { return "kacho_" + service + "_authz_cache_entries" }
func EvictionsMetricName(service string) string {
	return "kacho_" + service + "_authz_cache_evictions_total"
}

// DecisionsMetricName — семейство решений звена. Имя однородно краю
// (`kacho_api_gateway_authz_check_decisions_total`).
func DecisionsMetricName(service string) string {
	return "kacho_" + service + "_authz_check_decisions_total"
}

// Source — приёмник читателя величин, установимый ПОЗЖЕ регистрации.
//
// Нужен потому, что порядок сборки обратен порядку наблюдения: композиционный
// корень регистрирует коллектор на диагностической поверхности ДО того, как
// носитель контура соберёт звено решения со своим кешем. Неустановленный
// источник отвечает нулями, а не исчезает: исчезновение серий на это окно
// сообщило бы собирателю не «попаданий не было», а ничего.
type Source struct {
	mu   sync.RWMutex
	read DecisionReader
}

// Install ставит читателя. Идемпотентен по последнему вызову.
//
// Тип параметра НЕ именованный намеренно: значение этого метода передаётся как
// `servicecontract.Spec.AuthzObserve`, а типы функций в Go тождественны только
// при тождественных параметрах — с `DecisionReader` в подписи присваивание
// потребовало бы обёртки в каждом из шести корней.
func (s *Source) Install(read func() authz.Metrics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = read
}

// Read отдаёт величины звена; до установки — нули.
func (s *Source) Read() authz.Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.read == nil {
		return authz.Metrics{}
	}
	return s.read()
}

// Cache — величины окна вердиктов этого звена, читатель полосы.
func (s *Source) Cache() authz.CacheStats { return s.Read().Cache }

// Collector — коллектор величин окон вердиктов одного процесса.
type Collector struct {
	read          map[string]Reader
	decisions     DecisionReader
	cacheDesc     *prometheus.Desc
	entriesDesc   *prometheus.Desc
	evictionsDesc *prometheus.Desc
	decisionsDesc *prometheus.Desc
}

// New собирает коллектор для сервиса `service` (`vpc`, `compute`, `nlb`,
// `storage`, `registry`, `geo`) поверх читателей ОБЪЯВЛЕННЫХ полос.
//
// Полоса с `nil`-читателем отвечает нулями: провязка наблюдаемости обязана быть
// nil-безопасной, иначе её заводят «потом». Полоса, которую вызывающий не
// назвал, не рисуется вовсе — см. разбор в шапке пакета.
func New(service string, lanes map[string]Reader, decisions DecisionReader) *Collector {
	if decisions == nil {
		decisions = func() authz.Metrics { return authz.Metrics{} }
	}
	// Полоса вне словаря — ОТКАЗ, а не пропуск. Пропуск был бы «принято и
	// проигнорировано»: корень объявил окно, коллектор его молча выбросил, и
	// величины не выходят наружу ровно так же, как до этой работы. Зовётся из
	// композиционного корня, то есть падает на старте, а не в обслуживании.
	for lane := range lanes {
		if lane != LaneRPC && lane != LaneList && lane != LaneNarrow {
			panic("authzmetrics: неизвестная полоса окна вердиктов " + strconv.Quote(lane) +
				": словарь полос закрыт (" + LaneRPC + ", " + LaneList + ", " + LaneNarrow + "). Метка, взятая из " +
				"данных, превращает счётчик в перечень арендаторов — серий становится столько же, " +
				"сколько обслужено")
		}
	}
	c := &Collector{
		read:      make(map[string]Reader, len(lanes)),
		decisions: decisions,
		cacheDesc: prometheus.NewDesc(
			MetricName(service),
			"Positive-verdict cache lookups, by lane and result. hit+miss is the number of "+
				"questions the window was asked; the hit ratio is computed by the consumer, not here. "+
				"Only ALLOW verdicts are cached, so a miss always reaches the authoritative Check.",
			[]string{"lane", "result"}, nil),
		entriesDesc: prometheus.NewDesc(
			EntriesMetricName(service),
			"Entries currently held in the positive-verdict cache, by lane.",
			[]string{"lane"}, nil),
		evictionsDesc: prometheus.NewDesc(
			EvictionsMetricName(service),
			"Entries dropped from the positive-verdict cache, by lane and reason. "+
				"reason=expired is the window doing its job; reason=capacity drops LIVE entries "+
				"under the hard cap and is therefore a hit that will not happen; "+
				"reason=invalidated is the only proactive removal.",
			[]string{"lane", "reason"}, nil),
		decisionsDesc: prometheus.NewDesc(
			DecisionsMetricName(service),
			"Authorization decisions taken by the service's own interceptor, by outcome. "+
				"breakglass and unmapped MUST read zero on a production posture; their zero is only "+
				"meaningful next to a non-zero allowed, which tells it apart from an interceptor "+
				"nobody asked.",
			[]string{"decision"}, nil),
	}
	for lane, read := range lanes {
		if read == nil {
			read = func() authz.CacheStats { return authz.CacheStats{} }
		}
		c.read[lane] = read
	}
	return c
}

// Describe объявляет ВСЕ семейства до первого сбора — серии присутствуют нулями
// с первой секунды жизни процесса, поэтому «попаданий не было» и «коллектора
// нет» различимы без единого запроса.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.cacheDesc
	ch <- c.entriesDesc
	ch <- c.evictionsDesc
	ch <- c.decisionsDesc
}

// Collect отдаёт все объявленные серии, включая нулевые.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	// Обход ЛИТЕРАЛЬНОГО набора, ключи которого — константы этого файла: значение
	// метки полосы не может прийти из данных даже теоретически, и это же читает
	// гейт дерева `TestDiagnosticCollectorLabelsAreAClosedVocabulary`. Полоса,
	// которую вызывающий не объявил, не рисуется вовсе — иначе экспозиция
	// утверждала бы существование окна, которого в этом процессе нет.
	for lane, read := range map[string]Reader{
		LaneRPC:    c.read[LaneRPC],
		LaneList:   c.read[LaneList],
		LaneNarrow: c.read[LaneNarrow],
	} {
		if read == nil {
			continue
		}
		s := read()
		// Обход ЛИТЕРАЛЬНЫХ наборов, ключи которых — константы этого файла:
		// словари меток закрыты по построению, и это же читает гейт дерева
		// `TestDiagnosticCollectorLabelsAreAClosedVocabulary`.
		for result, value := range map[string]uint64{
			resultHit:  s.Hits,
			resultMiss: s.Misses,
		} {
			ch <- prometheus.MustNewConstMetric(c.cacheDesc, prometheus.CounterValue,
				float64(value), lane, result)
		}
		ch <- prometheus.MustNewConstMetric(c.entriesDesc, prometheus.GaugeValue,
			float64(s.Entries), lane)
		for reason, value := range map[string]uint64{
			reasonExpired:     s.EvictedExpired,
			reasonCapacity:    s.EvictedCapacity,
			reasonInvalidated: s.Invalidated,
		} {
			ch <- prometheus.MustNewConstMetric(c.evictionsDesc, prometheus.CounterValue,
				float64(value), lane, reason)
		}
	}

	m := c.decisions()
	for decision, value := range map[string]uint64{
		decisionAllowed:     m.Allowed,
		decisionDenied:      m.Denied,
		decisionUnavailable: m.Unavailable,
		decisionBreakglass:  m.Breakglass,
		decisionUnmapped:    m.Unmapped,
		decisionRateLimited: m.RateLimited,
	} {
		ch <- prometheus.MustNewConstMetric(c.decisionsDesc, prometheus.CounterValue,
			float64(value), decision)
	}
}

// Compile-time: реализуем контракт коллектора. Потеря метода в рефакторинге не
// сломала бы сборку у вызывающего, который регистрирует нас интерфейсом, — она
// тихо перестала бы публиковать единственные серии, говорящие, работает ли
// кеш вообще.
var _ prometheus.Collector = (*Collector)(nil)
