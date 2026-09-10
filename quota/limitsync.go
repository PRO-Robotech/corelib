// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package quota держит ОБЩИЕ части учёта потолков: то, что у всех владельцев
// одинаково, и ровно это.
//
// # Что здесь есть и чего здесь нет
//
// Число, из которого вычитают, остаётся в базе владельца ресурса — списание
// обязано идти в транзакции вставки, а распределённой транзакции в стеке нет.
// Поэтому здесь НЕТ ни таблицы учёта, ни списания, ни отказа.
//
// Здесь — синхронизатор величин: то, что тянет дельту у владельца величин и
// правит снимки в проекции. Он одинаков у всех владельцев by construction,
// потому что и таблица проекции, и правило старшинства областей у них одни и те
// же. Владелец подключается, передав имя своей схемы и клиента к соседу, и не
// пишет ни одного своего оператора.
//
// # Почему это не «ещё один слой», а устранение шестой копии
//
// Синхронизатор — предмет, которого не существовало вовсе: столбцы под него
// (`limit_revision`, `synced_at`) были заведены в пяти схемах, индекс под них
// создан, а писателя не было ни одного. Написанный пять раз порознь, он
// разошёлся бы на правиле старшинства — то есть там, где расхождение не видно
// ни одной из сторон: каждая копия по отдельности верна, а администратор
// получает разный эффект от одного и того же действия в разных доменах.
package quota

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Scope — область, на которой величина назначена. Порядок старшинства —
// PROJECT > ACCOUNT > DEFAULT — выражен методом AtLeast, а не сравнением строк в
// месте применения: правило одно, и жить оно обязано в одном месте.
type Scope string

const (
	ScopeDefault Scope = "DEFAULT"
	ScopeAccount Scope = "ACCOUNT"
	ScopeProject Scope = "PROJECT"
)

// rank — старшинство области. Не экспортируется: наружу торчит только
// отношение, а не число, чтобы у порядка не завелось второго представления.
func (s Scope) rank() int {
	switch s {
	case ScopeProject:
		return 3
	case ScopeAccount:
		return 2
	case ScopeDefault:
		return 1
	default:
		return 0
	}
}

// Valid отвечает, знает ли платформа такую область.
//
// Неизвестная область — НЕ «примем как умолчание»: это ровно тот случай, когда
// сосед сказал то, чего мы не понимаем, и применить это значило бы записать в
// проекцию догадку. Классификация чужого ответа без корзины «прочее».
func (s Scope) Valid() bool { return s.rank() > 0 }

// AtLeast отвечает, не ниже ли эта область, чем other.
func (s Scope) AtLeast(other Scope) bool { return s.rank() >= other.rank() }

// Change — одно изменение величины, каким его видит владелец проекции.
//
// Нейтрально к транспорту НАМЕРЕННО: пакет не импортирует сгенерённые стабы,
// поэтому синхронизатор проверяем без соседа и без сети, а владелец волен
// приносить дельту чем угодно.
type Change struct {
	// Kind — вид ресурса точечным токеном платформы.
	Kind string
	// Scope / ScopeID — где величина назначена. У умолчания ScopeID пуст.
	Scope   Scope
	ScopeID string
	// Value — назначенная величина. На отзыве не применяется (см. Withdrawn).
	Value int64
	// Revision — глобально возрастающий номер изменения у владельца величин. Он
	// же курсор дельты и он же то, с чем сверяется снимок строки.
	Revision int64
	// Withdrawn — величина снята. Строка проекции убирается, и следующее
	// списание разрешит потолок заново, получив следующую по старшинству
	// область.
	Withdrawn bool
}

// Validate отвергает изменение, которое нельзя применить не догадываясь.
//
// Отвергается ИМЕННО ЗДЕСЬ, а не в SQL: оператор, получивший неизвестную
// область, тихо не совпал бы ни с одной строкой и отчитался бы успехом —
// «применено 0», неотличимое от «нечего применять».
func (c Change) Validate() error {
	switch {
	case c.Kind == "":
		return errors.New("kind: required")
	case !c.Scope.Valid():
		return fmt.Errorf("scope: unknown %q", string(c.Scope))
	case c.Scope != ScopeDefault && c.ScopeID == "":
		return fmt.Errorf("scope_id: required for scope %s", string(c.Scope))
	case c.Revision <= 0:
		return fmt.Errorf("revision: must be positive, got %d", c.Revision)
	case !c.Withdrawn && c.Value < 0:
		return fmt.Errorf("value: must not be negative, got %d", c.Value)
	}
	return nil
}

// Source — откуда владелец берёт дельту. Реализуется клиентом к владельцу
// величин.
type Source interface {
	// ListChangedSince отдаёт изменения с ревизией больше курсора и следующий
	// курсор. Следующий курсор возвращается ВСЕГДА, включая пустую страницу.
	ListChangedSince(ctx context.Context, cursor string, pageSize int32) ([]Change, string, error)
}

// Projection — проекция величин у владельца ресурса.
type Projection interface {
	// ClaimPass берёт проход на ОДНУ реплику и отдаёт курсор, с которого он
	// продолжит. Пустой курсор означает «с начала времён» — ровно то, что нужно
	// проекции, ни разу не тянувшей дельту.
	//
	// Второе значение говорит, достался ли проход нам. Проигрыш — штатный исход,
	// а не отказ: дельта идемпотентна по ревизии, пропущенный тик ничего не
	// теряет, следующий догонит.
	//
	// lease — сколько проход считается занятым с момента последней отметки.
	ClaimPass(ctx context.Context, lease time.Duration) (cursor string, ok bool, err error)
	// ApplyChange правит строки снимка, адресуя их по столбцам, и отдаёт число
	// затронутых строк.
	ApplyChange(ctx context.Context, ch Change) (int64, error)
	// SaveCursor двигает курсор и прибавляет число применённых строк к
	// накопительному счётчику. Зовётся ТОЛЬКО когда есть что записать.
	SaveCursor(ctx context.Context, cursor string, appliedRows int64) error
	// Heartbeat отмечает состоявшийся проход, ничего больше не меняя.
	//
	// Отдельный глагол, а не побочный эффект сохранения курсора: иначе «жив» и
	// «работал» слились бы в одно число, и различить остановившийся
	// синхронизатор от живого на неизменной конфигурации стало бы нечем —
	// оба применяют ноль строк.
	Heartbeat(ctx context.Context) error
}

// Config — настройки синхронизатора.
type Config struct {
	// Interval — как часто тянуть дельту. Ноль означает умолчание.
	Interval time.Duration
	// PageSize — размер страницы дельты. Ноль означает умолчание.
	PageSize int32
	// PullTimeout — срок одного прохода. Ноль означает умолчание.
	PullTimeout time.Duration
}

const (
	defaultInterval    = 30 * time.Second
	defaultPageSize    = 200
	defaultPullTimeout = 20 * time.Second
	// maxPagesPerRun ограничивает догоняющий проход, чтобы первый запуск на
	// длинной истории не держал один вызов бесконечно. Остаток догоняется
	// следующим проходом — курсор персистентен, поэтому работа не теряется.
	maxPagesPerRun = 50
)

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.PageSize <= 0 {
		c.PageSize = defaultPageSize
	}
	if c.PullTimeout <= 0 {
		c.PullTimeout = defaultPullTimeout
	}
	return c
}

// Syncer тянет дельту величин и правит проекцию.
type Syncer struct {
	src  Source
	proj Projection
	cfg  Config
	log  *slog.Logger

	// pullsTotal / rowsTotal — накопительные, ради того чтобы «ни одна строка не
	// синхронизирована за всё время» было ЗАМЕТНО. Механизм, не тронувший ни
	// одной строки за всю свою жизнь, — находка, а не тишина; без счётчика он
	// неотличим от исправно работающего на неизменной конфигурации.
	// mu защищает накопленное. Заведён вместе с экспортом `Health`: писатель —
	// горутина цикла, а читатель теперь может быть любым, и «сегодня гонки нет»
	// есть свойство нынешних вызывающих, а не типа. Экспорт приглашает второго.
	mu         sync.Mutex
	pullsTotal int64
	rowsTotal  int64
	// lastSuccess — когда проход состоялся в последний раз. Нулевое значение
	// означает «ни разу за всю жизнь» и отличимо от любого настоящего момента
	// (#666).
	lastSuccess time.Time
	// now — часы. Поле, а не прямой вызов, чтобы возраст снимка проверялся
	// пробой без ожидания реального времени.
	now func() time.Time
}

// Health — состояние тянущего, каким его читает наблюдатель.
//
// Тип заведён потому, что у накопительных счётчиков ДОЛЖЕН быть читатель:
// величина, которую никто не читает, ничего не делает заметным. Читатель в
// прод-коде — сам `Run`: он называет эти поля в строке отказа и в строке
// останова.
type Health struct {
	// Pulls / Rows — сколько проходов состоялось и сколько строк снимка они
	// затронули за всю жизнь процесса.
	Pulls, Rows int64
	// LastSuccess — момент последнего состоявшегося прохода; нулевой, если его
	// не было ни разу.
	LastSuccess time.Time
	// NeverSynchronised — за всю жизнь не состоялось НИ ОДНОГО прохода.
	//
	// Отдельным полем, а не выводом «Pulls == 0» у каждого читателя: вывод,
	// сделанный в двух местах, разойдётся в третьем.
	NeverSynchronised bool
}

// Health отдаёт накопленное вместе с признаком «ни одного прохода за всю жизнь».
func (s *Syncer) Health() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Health{
		Pulls:             s.pullsTotal,
		Rows:              s.rowsTotal,
		LastSuccess:       s.lastSuccess,
		NeverSynchronised: s.pullsTotal == 0,
	}
}

// healthAttrs — состояние тянущего в виде полей журнальной строки.
//
// Одно место, потому что это РЕШЕНИЕ («что делает мёртвого тянущего заметным»),
// а не механика: две копии разошлись бы на признаке, и одна из строк перестала
// бы отличать «предел не меняли» от «тянущий мёртв».
func (s *Syncer) healthAttrs() []any {
	h := s.Health()
	attrs := []any{
		slog.Int64("rows_total", h.Rows),
		slog.Int64("pulls_total", h.Pulls),
		slog.Bool("never_synchronised", h.NeverSynchronised),
	}
	if !h.NeverSynchronised {
		attrs = append(attrs, slog.Duration("last_success_age", s.now().Sub(h.LastSuccess)))
	}
	return attrs
}

// NewSyncer собирает синхронизатор. Отказывает сразу, если подключать нечего:
// собранный без источника или без проекции, он был бы формой без содержания —
// исполнялся бы по расписанию и не делал ничего.
func NewSyncer(src Source, proj Projection, cfg Config, log *slog.Logger) (*Syncer, error) {
	if src == nil {
		return nil, errors.New("quota sync: source is required")
	}
	if proj == nil {
		return nil, errors.New("quota sync: projection is required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{src: src, proj: proj, cfg: cfg.withDefaults(), log: log, now: time.Now}, nil
}

// RunOnceBudgeted делает один проход ПОД СВОИМ СРОКОМ.
//
// Единственная точка, где срок прохода назначается. Прежде его ставил цикл, а
// подъём звал `RunOnce` голым контекстом процесса — то есть первый проход, тот
// самый, что случается в загрузочную бурю, шёл без срока вовсе, и замолчавший
// сосед держал бы подъём сервиса сколько угодно (#666). Владеть сроком обязано
// одно место, иначе следующий вызывающий снова его забудет.
func (s *Syncer) RunOnceBudgeted(ctx context.Context) (int64, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, s.cfg.PullTimeout)
	defer cancel()
	return s.RunOnce(runCtx)
}

// RunOnce делает один догоняющий проход и отдаёт число затронутых строк снимка.
//
// Страницы тянутся, пока курсор двигается: владелец величин отдаёт дельту
// постранично, и остановка на первой странице означала бы, что администратор,
// поменявший больше страницы за раз, ждёт следующего тика на каждую страницу.
func (s *Syncer) RunOnce(ctx context.Context) (int64, bool, error) {
	// Проход берётся ОДНОЙ репликой. Аренда равна периоду тика: короче — и две
	// реплики с разной фазой успели бы взять проход в одном окне; длиннее — и
	// смерть победителя стоила бы лишнего окна простоя.
	cursor, taken, err := s.proj.ClaimPass(ctx, s.cfg.Interval)
	if err != nil {
		return 0, false, fmt.Errorf("claim pass: %w", err)
	}
	if !taken {
		return 0, false, nil
	}

	var rows int64
	for page := 0; page < maxPagesPerRun; page++ {
		changes, next, err := s.src.ListChangedSince(ctx, cursor, s.cfg.PageSize)
		if err != nil {
			return rows, true, fmt.Errorf("pull changes: %w", err)
		}

		var pageRows int64
		for _, ch := range changes {
			if err := ch.Validate(); err != nil {
				// Непонятое изменение НЕ пропускается молча и НЕ применяется
				// наугад: проход останавливается, курсор не двигается, и
				// следующий проход встретит то же самое. Иначе дельта уехала бы
				// вперёд, а строка осталась бы со старой величиной навсегда —
				// и снаружи это выглядело бы как исправная синхронизация.
				return rows, true, fmt.Errorf("change %s@%s rev %d: %w",
					ch.Kind, string(ch.Scope), ch.Revision, err)
			}
			n, err := s.proj.ApplyChange(ctx, ch)
			if err != nil {
				return rows, true, fmt.Errorf("apply %s@%s rev %d: %w",
					ch.Kind, string(ch.Scope), ch.Revision, err)
			}
			pageRows += n
		}

		rows += pageRows

		caughtUp := len(changes) == 0 || next == cursor

		// Курсор двигается ПОСЛЕ применения страницы и в том же проходе:
		// сохранённый раньше, он потерял бы изменения при отказе применения.
		// Повтор применения безопасен — оператор идемпотентен по ревизии.
		//
		// Пустая догоняющая страница курсор НЕ переписывает: запись, которой
		// нечего записать, стоила бы оператора на каждом тике и вдобавок
		// двигала бы счётчик работы там, где работы не было.
		if !caughtUp || len(changes) > 0 {
			if err := s.proj.SaveCursor(ctx, next, pageRows); err != nil {
				return rows, true, fmt.Errorf("save cursor: %w", err)
			}
		}

		if caughtUp {
			break
		}
		cursor = next
	}

	// Проход состоялся — и это отмечается ВСЕГДА, включая проход, которому
	// нечего было применять. Иначе остановившийся синхронизатор выглядел бы
	// точно так же, как живой на неизменной конфигурации.
	if err := s.proj.Heartbeat(ctx); err != nil {
		return rows, true, fmt.Errorf("heartbeat: %w", err)
	}

	s.mu.Lock()
	s.pullsTotal++
	s.rowsTotal += rows
	s.lastSuccess = s.now()
	s.mu.Unlock()
	return rows, true, nil
}

// Run гоняет проходы до отмены контекста.
//
// Отказ прохода НЕ фатален и НЕ тих: он логируется и повторяется следующим
// тиком. Величина — не путь запроса, недоступность соседа здесь не должна
// ронять сервис; но и «молча продолжаем» тоже не годится, поэтому каждый отказ
// называется, а «ни одной строки за всё время» видно по накопительному счёту.
//
// РЕПЛИКИ: клейм — проход берёт одна реплика условной правкой строки курсора
// (Projection.ClaimPass); проигравший пропускает тик.
func (s *Syncer) Run(ctx context.Context) { s.run(ctx, true) }

// RunAfterInterval — то же, что Run, но первый проход откладывается на один тик.
//
// Зовётся тем, кто проход УЖЕ СДЕЛАЛ сам (подъём). Прежде такого глагола не
// было, и подъём вместе с циклом давали ровно ДВА прохода на старт с разницей в
// десятки миллисекунд — оба про одно событие, оба в загрузочную бурю, когда
// сосед и без того на пределе (#666).
//
// Отдельный глагол, а не скрытое поле «уже ходили»: исход цикла не должен
// зависеть от состояния, которого вызывающий не назвал.
func (s *Syncer) RunAfterInterval(ctx context.Context) { s.run(ctx, false) }

// run — само тело цикла; `passNow` говорит, делать ли проход немедленно или
// дождаться первого тика.
//
// РЕПЛИКИ: клейм — проход берёт одна реплика условной правкой строки курсора
// (Projection.ClaimPass); проигравший пропускает тик. Отложенный первый проход
// (`passNow == false`) этого не меняет: он сдвигает МОМЕНТ первого вопроса, а не
// то, кому достанется проход.
func (s *Syncer) run(ctx context.Context, passNow bool) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()

	s.log.Info("quota limit syncer started",
		slog.Duration("interval", s.cfg.Interval),
		slog.Int("page_size", int(s.cfg.PageSize)))

	if !passNow {
		// Проход только что сделал вызывающий. Ждём тика, а не повторяем его.
		select {
		case <-ctx.Done():
			s.log.Info("quota limit syncer stopped", s.healthAttrs()...)
			return
		case <-t.C:
		}
	}

	for {
		rows, ran, err := s.RunOnceBudgeted(ctx)

		switch {
		case err != nil:
			// Состояние тянущего идёт В ТОЙ ЖЕ строке, что и отказ: разбирающий
			// читает отказ, и именно здесь ему нужно знать, случался ли удачный
			// проход хоть раз. Отдельной строкой это было бы двумя записями об
			// одном событии, и вторую пришлось бы искать.
			s.log.Error("quota limit sync failed",
				append([]any{slog.String("error", err.Error())}, s.healthAttrs()...)...)
		case !ran:
			// Проход исполняет другая реплика. Отдельная строка от «нечего
			// применять»: иначе «нас развели» и «дельта пуста» выглядели бы
			// одинаково, а различить их — ровно то, ради чего развод заведён.
			s.log.Debug("quota limit sync skipped: another replica holds the pass")
		case rows > 0:
			s.log.Info("quota limits synchronised",
				slog.Int64("rows", rows),
				slog.Int64("rows_total", s.rowsTotal))
		}

		select {
		case <-ctx.Done():
			s.log.Info("quota limit syncer stopped", s.healthAttrs()...)
			return
		case <-t.C:
		}
	}
}
