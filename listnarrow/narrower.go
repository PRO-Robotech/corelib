// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listnarrow

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/validate"
)

// MaxBatchSize — контрактный предел `AuthorizeService.BatchCheck` приёмной стороны
// (>100 → InvalidArgument). Партии режутся по нему.
const MaxBatchSize = 100

// DefaultParallelism — сколько партий ОДНОГО отношения летят в kaname
// одновременно (ограниченный пул исполнителей, не «горутина на партию»).
//
// 5 выбрано из арифметики контракта, а не на глаз: оно РОВНО делит максимум партий
// на предельной странице (validate.MaxPageSize / MaxBatchSize = 10), поэтому каждое
// отношение — ровно две полные волны без рваного хвоста. Глубина worst-case падает
// с 10 последовательных до 2, и это ОСВОБОЖДАЕТ бюджет под реалистичный срок одного
// вызова: пока партии обходились по одной, срок приходилось пилить под число
// последовательных хопов, и первый же перешагнувший его хоп ронял весь
// ПОЛОЖИТЕЛЬНЫЙ список в отказ доступности.
//
// Всплеск на приёмной стороне ограничен 5 одновременными запросами (≤500 вопросов
// в полёте) на запрос — предсказуемый малый множитель.
const DefaultParallelism = 5

// budgetHeadroom{Num,Den} — множитель бюджета операции над worst-case стеной
// (×3/2 ⇒ запас 50%). Бюджет обязан быть ПОТОЛКОМ, который в здоровом режиме не
// срабатывает: срабатывать должны сроки отдельных вызовов.
const (
	budgetHeadroomNum = 3
	budgetHeadroomDen = 2
)

// defaultRelationsKey — ключ записи «все прочие типы» в предикате страницы.
// Объявление с такой записью ТОТАЛЬНО: у типа, не названного поимённо, предикат всё
// равно объявлен, а не спрятан в теле функции.
const defaultRelationsKey = ""

// Check — ОДИН вопрос о доступе, объявленный типами ФУНДАМЕНТА.
//
// Прежде порт говорил сгенерированным типом владельца модели, и сигнатура
// совпадала с его клиентом — «боевая реализация тонкий проброс». Цена этого
// удобства названа приёмкой K3-1 §7.2: фундамент импортировал контракт службы
// доступа, то есть после разъезда `corelib` потребовал бы `kaname`, а `kaname`
// уже требует `corelib`. Цикл на уровне модулей, который Go не собирает.
//
// Поэтому шов проходит СКВОЗЬ ТИП: вопрос и вердикт объявлены здесь, а перевод
// в контракт владельца живёт у того, кто этот контракт реализует
// (`pkg/listnarrow/narrowiam`, класс `kaname`).
type Check struct {
	// Subject — принципал в форме владельца модели (`<тип>:<id>`).
	Subject string
	// ResourceType — тип объекта в словаре модели прав.
	ResourceType string
	// ResourceID — идентификатор объекта.
	ResourceID string
	// Action — метод, ради которого задан вопрос.
	Action string
	// RequiredRelation — отношение, которого метод требует.
	RequiredRelation string
}

// AuthorizeClient — узкий порт к владельцу модели.
//
// Контракт ответа: вердикты В ПОРЯДКЕ ВОПРОСОВ и ТОЙ ЖЕ ДЛИНЫ. Иная длина —
// не «считаем отказом», а fail-closed ошибка у вызывающего: молчаливое смещение
// индексов выдало бы вердикт одного объекта за другой, и страница была бы
// неверна так, что вызывающий этого не обнаружит.
type AuthorizeClient interface {
	BatchCheck(ctx context.Context, checks []Check) ([]bool, error)
}

// Config — посадка сужателя.
type Config struct {
	// Relations — предикат членства страницы ПО ТИПУ объекта. Обязан быть ТОТАЛЬНЫМ:
	// запись под пустым ключом — умолчание для типа, не названного поимённо.
	//
	// Это ПОЛЕ, а не константа пакета, намеренно (решение владельца 2026-08-09):
	// сведение предиката к одному значению меняло бы ВИДИМОСТЬ, а это продуктовое
	// решение по каждому ресурсу. Тотальность позволяет гейту паритета прочитать
	// объявление вызывающего и сверить его с каталогом прав потипно.
	Relations map[string][]string

	// Timeout — срок ОДНОГО запроса к приёмной стороне. НЕ бюджет операции.
	Timeout time.Duration
	// Parallelism — сколько партий одного отношения летят одновременно.
	// 0 → DefaultParallelism.
	Parallelism int
	// OverallTimeout — бюджет ВСЕЙ операции. 0 → выводится из Timeout и Parallelism
	// так, чтобы worst-case стена на предельной странице помещалась внутрь с
	// запасом. Потолок нужен, потому что срок одного вызова ограничивает ОДИН хоп, а
	// не их последовательность.
	OverallTimeout time.Duration

	// CacheTTL — окно жизни ОДНОГО положительного вердикта. Это ОКНО ОТЗЫВА:
	// столько субъект, у которого право уже отобрали, продолжает видеть строку.
	//
	// НОЛЬ ЗДЕСЬ НЕ ОЗНАЧАЕТ «БЕЗ КЕША». Ноль и всякая неположительная величина
	// означают умолчание `pkg/authz.RevocationPolicy.Default`; выключить окно
	// этим полем НЕЛЬЗЯ, такого состояния у сужателя нет. Семантика названа
	// здесь, потому что читается она ровно наоборот, и на ней уже ошиблись:
	// проба, собранная с `CacheTTL: 0` ради «холодного» пути, мерила тёплый и
	// зеленела бы, утверждая не то. Единственный рычаг, которым окно
	// заканчивается, — время (см. Narrower.WithClock).
	CacheTTL time.Duration
	// CacheMaxEntries — предел размера окна. Неположительная величина означает
	// умолчание `pkg/authz` (не «без предела»).
	CacheMaxEntries int

	// SoftPassOnPeerFailure — задокументированный мягкий проход: на отказе приёмной
	// стороны страница уходит НЕсуженной. Защитим ровно пока отказ действительно
	// временный, поэтому здесь он классифицируется и считается (см. Counts).
	SoftPassOnPeerFailure bool

	// Breakglass — аварийный режим: страница отдаётся несуженной, потому что модели
	// на этой посадке нет вовсе. Остаётся явным исключением (решение владельца
	// 2026-08-09), но КАЖДОЕ срабатывание считается и называется — иначе аварийный
	// режим становится тихим штатным.
	//
	// Личности он НЕ отменяет: безымянный вызывающий отвергается и здесь.
	Breakglass bool
}

// Counts — прочитанные величины сужателя. Именно ПРОЧИТАННЫЕ: по отсутствию строк в
// журнале «прохода не было» и «счётчика нет» неразличимы, а прочитанный ноль их
// различает.
//
// Полос ЧЕТЫРЕ, и первая из них положительная. Трёх полос несуженного прохода
// недостаточно: три нуля не отличают «аварийных проходов не было» от «сужателя не
// звали ни разу», а это разные состояния — второе означает, что защита не
// исполняется вовсе. Единица счёта у всех четырёх одна: ОДНА публичная операция
// сужения страницы.
type Counts struct {
	// Narrowed — сколько раз страница сужена ШТАТНО: личность установлена,
	// аварийного режима нет, сосед ответил, мягкий проход не понадобился.
	//
	// Пустая страница сюда НЕ попадает: соседа она не спрашивает и сужать в ней
	// нечего, поэтому засчитывать её значило бы объявлять работу там, где её не
	// делали.
	Narrowed uint64
	// Breakglass — сколько раз страница ушла несуженной по аварийному режиму.
	Breakglass uint64
	// SoftPassMisconfigured — сколько раз мягкий проход сработал на ответе,
	// ДОКАЗЫВАЮЩЕМ неверную настройку (сам не пройдёт никогда).
	SoftPassMisconfigured uint64
	// SoftPassTransient — сколько раз мягкий проход сработал на отказе, который
	// может пройти сам.
	SoftPassTransient uint64
}

// Narrower — сужатель списочной страницы поверх `AuthorizeService.BatchCheck` с
// окном ПОЛОЖИТЕЛЬНЫХ вердиктов (TTL + вытеснение давно не использованных).
type Narrower struct {
	cli AuthorizeClient
	cfg Config

	logger *slog.Logger
	now    func() time.Time

	narrowed      atomic.Uint64
	breakglass    atomic.Uint64
	misconfigured atomic.Uint64
	transient     atomic.Uint64

	// Величины окна вердиктов. Атомарные, а не под общим mutex: попадание
	// считается на горячем пути по КАЖДОМУ элементу страницы, а страница
	// контрактно бывает до тысячи.
	cacheHits       atomic.Uint64
	cacheMisses     atomic.Uint64
	evictedExpired  atomic.Uint64
	evictedCapacity atomic.Uint64

	mu     sync.Mutex
	cache  map[string]*list.Element
	lruLst *list.List
}

type cacheEntry struct {
	key     string
	expires time.Time
}

// New собирает сужатель. cli == nil означает, что спросить негде: без аварийного
// режима такой сужатель ОТКАЗЫВАЕТ, а не пропускает.
//
// Нормализует веер и ВЫВОДИТ бюджет операции, если тот не задан явно: бюджет — не
// независимая ручка, а функция контракта (максимум страницы, предел партии) и срока
// одного вызова. Так арифметика «сумма worst-case ожиданий помещается в бюджет»
// держится ПО ПОСТРОЕНИЮ, а не по надежде на когерентную настройку.
func New(cli AuthorizeClient, cfg Config) *Narrower {
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Second
	}
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = DefaultParallelism
	}
	if cfg.OverallTimeout <= 0 {
		cfg.OverallTimeout = deriveOverallTimeout(cfg.Timeout, cfg.Parallelism, cfg.Relations)
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Second
	}
	if cfg.CacheMaxEntries <= 0 {
		cfg.CacheMaxEntries = 10000
	}
	return &Narrower{
		cli:    cli,
		cfg:    cfg,
		logger: slog.Default(),
		now:    time.Now,
		cache:  make(map[string]*list.Element, cfg.CacheMaxEntries),
		lruLst: list.New(),
	}
}

// WithLogger подменяет приёмник записей о проходах (композиционный корень / тесты).
func (n *Narrower) WithLogger(l *slog.Logger) *Narrower {
	if n != nil && l != nil {
		n.logger = l
	}
	return n
}

// WithClock подменяет часы окна вердиктов. Нужен там, где проба утверждает ИСТЕЧЕНИЕ:
// ожидание настоящего срока сделало бы её медленной и недетерминированной.
func (n *Narrower) WithClock(clock func() time.Time) *Narrower {
	if n != nil && clock != nil {
		n.now = clock
	}
	return n
}

// Counts — прочитанные величины (см. Counts).
func (n *Narrower) Counts() Counts {
	if n == nil {
		return Counts{}
	}
	return Counts{
		Narrowed:              n.narrowed.Load(),
		Breakglass:            n.breakglass.Load(),
		SoftPassMisconfigured: n.misconfigured.Load(),
		SoftPassTransient:     n.transient.Load(),
	}
}

// Narrows сообщает, СУЖАЕТ ли сужатель на самом деле: собеседник есть, аварийный
// режим не взведён, мягкий проход не превращает отказ в сквозной пропуск. Читается
// загрузочным стражем — «поле есть» и «значение меняет исход» разные вещи.
func (n *Narrower) Narrows() bool {
	return n != nil && n.cli != nil && !n.cfg.Breakglass && !n.cfg.SoftPassOnPeerFailure
}

// Budget — фактический бюджет одной операции.
func (n *Narrower) Budget() time.Duration { return n.cfg.OverallTimeout }

// WorstCaseDepth — worst-case число ПОСЛЕДОВАТЕЛЬНЫХ волн на предельной странице при
// текущем веере.
func (n *Narrower) WorstCaseDepth() int {
	return worstCaseDepth(n.cfg.Parallelism, n.cfg.Relations)
}

// Relations — предикат страницы для одного типа объекта. Второе значение false
// означает, что предикат для типа НЕ ОБЪЯВЛЕН: карта не тотальна.
func (n *Narrower) Relations(resourceType string) ([]string, bool) {
	if rels, ok := n.cfg.Relations[resourceType]; ok && len(rels) > 0 {
		return rels, true
	}
	rels, ok := n.cfg.Relations[defaultRelationsKey]
	return rels, ok && len(rels) > 0
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// maxRelationsDepth — сколько отношений глубже всего спрашивается по одному объекту.
// Отношения последовательны по построению (следующее спрашивается только у отказанных
// предыдущим), поэтому в глубину волн они входят множителем.
func maxRelationsDepth(relations map[string][]string) int {
	depth := 1
	for _, rels := range relations {
		if len(rels) > depth {
			depth = len(rels)
		}
	}
	return depth
}

func worstCaseDepth(parallelism int, relations map[string][]string) int {
	if parallelism <= 0 {
		parallelism = DefaultParallelism
	}
	batches := ceilDiv(int(validate.MaxPageSize), MaxBatchSize)
	return maxRelationsDepth(relations) * ceilDiv(batches, parallelism)
}

func deriveOverallTimeout(perCall time.Duration, parallelism int, relations map[string][]string) time.Duration {
	return time.Duration(worstCaseDepth(parallelism, relations)) * perCall * budgetHeadroomNum / budgetHeadroomDen
}

// visibleIDs — сужение страницы для УЖЕ НАЗВАННОГО субъекта. Публичный вход — IDs /
// Page: именно они держат безусловный порядок «личность → посадка → страница».
func (n *Narrower) visibleIDs(ctx context.Context, subject, resourceType, action string, ids []string) ([]string, error) {
	relations, ok := n.Relations(resourceType)
	if !ok {
		// Тип без предиката прошёл бы БЕЗ вопроса. Это не настройка, а дыра, и она
		// обязана быть отказом, а не молчаливым пропуском.
		return nil, status.Errorf(codes.Internal,
			"list filter: no page predicate declared for resource type %q and no default entry", resourceType)
	}

	// Бюджет ВСЕЙ операции. Сроки отдельных вызовов остаются, но выводятся уже из
	// этого контекста — раньший срок выигрывает автоматически, поэтому
	// последовательность волн не может суммарно пережить бюджет.
	if n.cfg.OverallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, n.cfg.OverallTimeout)
		defer cancel()
	}

	visible := make(map[string]struct{}, len(ids))
	pending := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue // одна страница не платит дважды за повторяющийся идентификатор
		}
		seen[id] = struct{}{}
		if n.getCache(subject, resourceType, id) {
			visible[id] = struct{}{}
			continue
		}
		pending = append(pending, id)
	}

	for _, relation := range relations {
		if len(pending) == 0 {
			break
		}
		allowed, denied, err := n.askRelation(ctx, subject, resourceType, action, relation, pending)
		if err != nil {
			return n.handleErr(ids, err)
		}
		for _, id := range allowed {
			visible[id] = struct{}{}
			n.putCache(subject, resourceType, id)
		}
		pending = denied
	}

	// Штатное сужение состоялось. Считается ЗДЕСЬ, а не у вызывающего: отсюда
	// видно, что ни один из трёх несуженных проходов не сработал (каждый из них
	// возвращается раньше через handleErr), — а у вызывающего мягкий проход
	// неотличим от успеха, потому что тоже приходит без ошибки.
	n.narrowed.Add(1)

	// Порядок входа сохраняется — страница уже упорядочена курсором,
	// переупорядочивание сломало бы пагинацию.
	out := make([]string, 0, len(visible))
	for _, id := range ids {
		if _, ok := visible[id]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// batchVerdicts — вердикты ОДНОЙ партии. Каждая горутина пишет в СВОЙ элемент среза, а
// склейка идёт по индексу партии, а не по порядку завершения: разбиение обязано быть
// детерминированным независимо от того, кто ответил первым.
type batchVerdicts struct {
	allowed []string
	denied  []string
}

func splitBatches(ids []string) [][]string {
	out := make([][]string, 0, ceilDiv(len(ids), MaxBatchSize))
	for start := 0; start < len(ids); start += MaxBatchSize {
		end := start + MaxBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[start:end])
	}
	return out
}

// askRelation спрашивает ОДНО отношение про набор идентификаторов партиями ≤
// MaxBatchSize. Партии независимы, поэтому идут ОГРАНИЧЕННЫМ веером: обход по одной
// давал на предельной странице десять запросов подряд и вынуждал держать срок одного
// вызова неестественно узким.
//
// Fail-closed: ПЕРВАЯ ошибка отменяет соседей и возвращается как есть; частично
// разрешённый набор не отдаётся никогда.
func (n *Narrower) askRelation(
	ctx context.Context,
	subject, resourceType, action, relation string,
	ids []string,
) (allowed, denied []string, err error) {
	batches := splitBatches(ids)
	results := make([]batchVerdicts, len(batches))

	// Одиночная партия (страница ≤ MaxBatchSize) не платит за пул — общий случай
	// остаётся ровно тем же одним запросом.
	if len(batches) == 1 {
		if cerr := n.askBatch(ctx, subject, resourceType, action, relation, batches[0], &results[0]); cerr != nil {
			return nil, nil, cerr
		}
		return joinVerdicts(results, len(ids))
	}

	workers := n.cfg.Parallelism
	if workers > len(batches) {
		workers = len(batches)
	}

	// cctx отменяется ПЕРВОЙ ошибкой — соседние партии не дорабатывают страницу,
	// которую всё равно не отдадут.
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	queue := make(chan int)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for b := range queue {
				cerr := n.askBatch(cctx, subject, resourceType, action, relation, batches[b], &results[b])
				if cerr == nil {
					continue
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = cerr
					cancel()
				}
				mu.Unlock()
				return
			}
		}()
	}
feed:
	for b := range batches {
		select {
		case queue <- b:
		case <-cctx.Done():
			break feed // кто-то уже упал fail-closed либо истёк бюджет
		}
	}
	close(queue)
	wg.Wait()

	if firstErr != nil {
		return nil, nil, firstErr
	}
	// cancel() зовётся только после установки firstErr, поэтому завершённый cctx без
	// зафиксированной ошибки означает, что истёк РОДИТЕЛЬСКИЙ контекст (бюджет
	// операции или срок вызывающего) посреди страницы. Сообщаем об этом, а не отдаём
	// недорешённый набор за ответ.
	if cerr := ctx.Err(); cerr != nil {
		return nil, nil, cerr
	}
	return joinVerdicts(results, len(ids))
}

// joinVerdicts склеивает вердикты в порядке ПАРТИЙ (не завершения).
func joinVerdicts(results []batchVerdicts, total int) (allowed, denied []string, err error) {
	allowed = make([]string, 0, total)
	denied = make([]string, 0, total)
	for i := range results {
		allowed = append(allowed, results[i].allowed...)
		denied = append(denied, results[i].denied...)
	}
	return allowed, denied, nil
}

func (n *Narrower) askBatch(
	ctx context.Context,
	subject, resourceType, action, relation string,
	batch []string,
	out *batchVerdicts,
) error {
	checks := make([]Check, 0, len(batch))
	for _, id := range batch {
		checks = append(checks, Check{
			Subject:          subject,
			ResourceType:     resourceType,
			ResourceID:       id,
			Action:           action,
			RequiredRelation: relation,
		})
	}

	verdicts, cerr := n.askOnce(ctx, checks)
	if cerr != nil {
		return cerr
	}
	// Контракт приёмной стороны: вердикты в порядке вопросов и той же длины.
	// Расхождение — не «считаем отказом», а fail-closed ошибка: молчаливое смещение
	// индексов выдало бы вердикт одного объекта за другой, и страница была бы неверна
	// так, что вызывающий этого не обнаружит.
	if len(verdicts) != len(batch) {
		return &misalignedAnswerError{got: len(verdicts), want: len(batch), relation: relation}
	}
	out.allowed = make([]string, 0, len(batch))
	out.denied = make([]string, 0, len(batch))
	for i, allowed := range verdicts {
		if allowed {
			out.allowed = append(out.allowed, batch[i])
		} else {
			out.denied = append(out.denied, batch[i])
		}
	}
	return nil
}

// misalignedAnswerError — ответ, который невозможно выровнять с вопросами. Отдельный
// тип, а не строка: классификатор мягкого прохода обязан узнавать его как
// ДОКАЗАТЕЛЬСТВО неверной настройки (сосед отвечает по другому контракту), а не как
// временный сбой.
type misalignedAnswerError struct {
	got, want int
	relation  string
}

func (e *misalignedAnswerError) Error() string {
	return "list filter: peer answered " + itoa(e.got) + " of " + itoa(e.want) +
		" checks for relation " + e.relation
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// askOnce делает ровно один запрос под собственным сроком. Контекст уже несёт бюджет
// операции, поэтому фактическим сроком становится РАНЬШИЙ из двух.
func (n *Narrower) askOnce(ctx context.Context, checks []Check) ([]bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()
	return n.cli.BatchCheck(callCtx, checks)
}

// handleErr — реакция на отказ приёмной стороны.
func (n *Narrower) handleErr(ids []string, err error) ([]string, error) {
	if n.cfg.SoftPassOnPeerFailure {
		return n.softPass(ids, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// Оба числа названы явно: сработать мог как срок одного вызова, так и бюджет
		// операции, и по ошибке контекста их не различить.
		return nil, status.Errorf(codes.Unavailable,
			"list filter: AuthorizeService.BatchCheck deadline exceeded (per-call %s, operation budget %s)",
			n.cfg.Timeout, n.cfg.OverallTimeout)
	}
	if s, ok := status.FromError(err); ok && s.Code() != codes.OK && s.Code() != codes.Unknown {
		return nil, status.Errorf(codes.Unavailable, "list filter: AuthorizeService.BatchCheck %s: %s", s.Code(), s.Message())
	}
	return nil, status.Errorf(codes.Unavailable, "list filter: AuthorizeService.BatchCheck: %v", err)
}

// softPass — задокументированный мягкий проход: страница уходит вызывающему
// НЕсуженной. Он защитим ровно пока отказ действительно временный, поэтому здесь он
// (а) классифицируется и (б) считается.
//
// Классификация решает, чем это НАЗВАНО. Ответ соседа, доказывающий, что мы
// обращаемся не туда (нет такого метода, учётные данные не приняты, ответ не той
// формы), — это НАСТРОЙКА: она не пройдёт сама, и пока её не поправят, сужатель
// исполняется на каждом запросе и не сужает ни одной страницы. Постоянное состояние
// не может называться предупреждением о временном.
func (n *Narrower) softPass(ids []string, err error) ([]string, error) {
	if provesMisconfiguration(err) {
		total := n.misconfigured.Add(1)
		n.logger.Error("list filter soft pass: iam.BatchCheck answered in a way that proves a MISCONFIGURED edge, "+
			"not an outage — this will not clear on its own; the per-object filter decides nothing "+
			"and the page is returned UNFILTERED",
			"error", err, "misconfigured_soft_passes_total", total)
		return ids, nil
	}
	total := n.transient.Add(1)
	n.logger.Warn("list filter soft pass: iam.BatchCheck unreachable, bypassing per-object authz "+
		"(returning the UNFILTERED page)",
		"error", err, "transient_soft_passes_total", total)
	return ids, nil
}

// provesMisconfiguration — доказывает ли ответ, что дело не в доступности соседа.
//
// Различие проводится по одному вопросу: может ли повтор ТОГО ЖЕ запроса когда-нибудь
// пройти? Истёкший срок и отменённый контекст — может (медленный, но живой сосед), и
// проверяется это ПЕРВЫМ: голый истёкший срок не является статусом, и без этой ветки
// истёкший бюджет читался бы как доказательство неверной настройки — ложная тревога на
// здоровом, но нагруженном стенде.
//
// Остальные коды оставлены временными намеренно: ошибиться в эту сторону значит
// недосказать тревогу, ошибиться в другую — поднимать её на здоровом стенде и приучить
// оператора её игнорировать.
func provesMisconfiguration(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var misaligned *misalignedAnswerError
	if errors.As(err, &misaligned) {
		// До разбора мы дошли, а разобрать не смогли: сосед отвечает по другому
		// контракту — повтор не поможет.
		return true
	}
	s, ok := status.FromError(err)
	if !ok {
		return true
	}
	switch s.Code() {
	case codes.Unimplemented, codes.Unauthenticated, codes.PermissionDenied, codes.InvalidArgument:
		return true
	default:
		return false
	}
}

// cacheKey — ключ ПОЛОЖИТЕЛЬНОГО вердикта. Отношение в ключ не входит: кешируется
// итоговое «видим», а не отдельная ветка предиката.
func cacheKey(subject, resourceType, id string) string {
	return subject + "|" + resourceType + "|" + id
}

func (n *Narrower) getCache(subject, resourceType, id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	el, ok := n.cache[cacheKey(subject, resourceType, id)]
	if !ok {
		n.cacheMisses.Add(1)
		return false
	}
	e, _ := el.Value.(*cacheEntry)
	if n.now().After(e.expires) {
		n.lruLst.Remove(el)
		delete(n.cache, e.key)
		// Истечение — ШТАТНАЯ работа окна, и считается отдельно от давления
		// потолка: сложенные, они объявили бы исчерпание потолка нормой.
		n.evictedExpired.Add(1)
		n.cacheMisses.Add(1)
		return false
	}
	n.lruLst.MoveToFront(el)
	n.cacheHits.Add(1)
	return true
}

// Отзыв доступа виден по ИСТЕЧЕНИИ записи, и другого механизма здесь нет: кешируются
// ТОЛЬКО положительные вердикты и только на CacheTTL, поэтому снятый доступ перестаёт
// действовать самое позднее через окно, а отрицательный вердикт не кешируется вовсе —
// свежая выдача видна сразу.
func (n *Narrower) putCache(subject, resourceType, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	key := cacheKey(subject, resourceType, id)
	exp := n.now().Add(n.cfg.CacheTTL)
	if el, ok := n.cache[key]; ok {
		e, _ := el.Value.(*cacheEntry)
		e.expires = exp
		n.lruLst.MoveToFront(el)
		return
	}
	el := n.lruLst.PushFront(&cacheEntry{key: key, expires: exp})
	n.cache[key] = el
	for n.lruLst.Len() > n.cfg.CacheMaxEntries {
		tail := n.lruLst.Back()
		if tail == nil {
			break
		}
		te, _ := tail.Value.(*cacheEntry)
		n.lruLst.Remove(tail)
		delete(n.cache, te.key)
		// Ещё ЖИВАЯ запись, снятая потолком, — это попадание, которого не будет.
		// Ненулевое значение означает, что потолок мал для нагрузки, и доля
		// попаданий упирается в него, а не в окно.
		n.evictedCapacity.Add(1)
	}
}

// CacheSize — текущий размер окна вердиктов.
func (n *Narrower) CacheSize() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lruLst.Len()
}

// CacheStats — величины окна вердиктов сужателя (#768).
//
// # Зачем они здесь, если размер уже был
//
// Размер объясняет, ПОЧЕМУ доля попаданий такая, но самой доли не даёт. А доля
// — та величина, которая решает, сколько вопросов доезжает до владельца модели
// под списочной нагрузкой: звено решения задаёт ОДИН вопрос на вызов, а сужатель
// — по вопросу на КАЖДЫЙ элемент страницы, а страница контрактно бывает до
// тысячи. То есть здесь через окно проходит больше вопросов, чем через окно
// звена, и до сих пор их не считал никто.
//
// # Форма — ТА ЖЕ, что у окна звена решения
//
// Возвращается `authz.CacheStats`, а не свой тип: коллектор
// (`pkg/authz/authzmetrics`) принимает читателя именно этой формы, и второй тип
// потребовал бы переходника, который разъехался бы с первым молча. Имена серий
// от этого тоже остаются едиными.
//
// # `Invalidated` здесь ВСЕГДА ноль, и это факт, а не пропуск
//
// Проактивного снятия у окна сужателя НЕТ: кешируются только положительные
// вердикты и только на своё время жизни, поэтому окно отзыва целиком
// определяется истечением. Ноль на этой полосе — утверждение об устройстве, и
// оно верно; сделать его отсутствующим значило бы скрыть эту разницу с окном
// звена, у которого снятие по субъекту есть.
func (n *Narrower) CacheStats() authz.CacheStats {
	if n == nil {
		return authz.CacheStats{}
	}
	n.mu.Lock()
	entries := n.lruLst.Len()
	n.mu.Unlock()
	return authz.CacheStats{
		Hits:            n.cacheHits.Load(),
		Misses:          n.cacheMisses.Load(),
		Entries:         entries,
		EvictedExpired:  n.evictedExpired.Load(),
		EvictedCapacity: n.evictedCapacity.Load(),
	}
}

// Visible — сужение набора для УЖЕ УСТАНОВЛЕННОГО субъекта и ЯВНО названного
// отношения.
//
// Нижняя дверь того же механизма, и заведена она ради вызывающего, у которого
// личность устанавливается СВОИМИ правилами с записанной причиной: у registry ответ
// безымянному приведён к тому, что даёт его же интерсептор на всех прочих RPC —
// иначе сам код ответа сообщал бы неаутентифицированному пробующему, какие RPC
// авторизуются на уровне данных, а какие интерсептором. Пропусти его через IDs — и
// это решение подменилось бы общим.
//
// Отношение здесь тоже ЯВНОЕ: предикат страницы registry (`v_list` на каталоге имён)
// намеренно шире отношения чтения, и это записанное отступление — страница каталога
// отдаёт голые ИМЕНА, а не сообщение ресурса, поэтому «видно в перечне без
// содержимого» там реализуемо. Брать предикат из карты значило бы молча сузить
// чужое решение.
//
// Всё остальное — то же: партии ≤ MaxBatchSize, ограниченный веер, бюджет операции,
// окно ПОЛОЖИТЕЛЬНЫХ вердиктов, fail-closed на первой ошибке.
func (n *Narrower) Visible(ctx context.Context, subject, resourceType, action, relation string, ids []string) ([]string, error) {
	if n == nil || n.cli == nil {
		return nil, ErrModelNotWired()
	}
	if subject == "" {
		return nil, ErrUnnamedCaller()
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if n.cfg.OverallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, n.cfg.OverallTimeout)
		defer cancel()
	}

	visible := make(map[string]struct{}, len(ids))
	pending := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if n.getCache(subject, resourceType, id) {
			visible[id] = struct{}{}
			continue
		}
		pending = append(pending, id)
	}
	if len(pending) > 0 {
		allowed, _, err := n.askRelation(ctx, subject, resourceType, action, relation, pending)
		if err != nil {
			return n.handleErr(ids, err)
		}
		for _, id := range allowed {
			visible[id] = struct{}{}
			n.putCache(subject, resourceType, id)
		}
	}
	// Нижняя дверь того же механизма — и та же положительная полоса: иначе
	// вызывающий, ходящий сюда, был бы для наблюдения неотличим от вызывающего,
	// который не ходит никуда.
	n.narrowed.Add(1)
	out := make([]string, 0, len(visible))
	for _, id := range ids {
		if _, ok := visible[id]; ok {
			delete(visible, id)
			out = append(out, id)
		}
	}
	return out, nil
}
