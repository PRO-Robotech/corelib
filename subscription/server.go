// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/pagetoken"
)

// readBatch — сколько строк журнала читается одним запросом.
//
// Держится НЕ БОЛЬШЕ [visibilityBatch]: тогда один прочитанный батч журнала
// укладывается ровно в один вопрос к модели прав.
const readBatch = 100

// visibilityBatch — максимум идентификаторов в ОДНОМ вопросе к модели прав.
// Контракт пакетной проверки отвергает партию больше сотни, и то же ограничение
// записано в правиле платформы про авторизацию на уровне данных.
const visibilityBatch = 100

// errorDomain — домен служебных подробностей отказа.
//
// Он ПЛАТФОРМЕННЫЙ, а не доменный: отказ производит общий сервер, а не владелец
// журнала, и клиент, ключующийся на него, обязан получать одну и ту же строку от
// всех владельцев.
const errorDomain = "subscription.kacho.cloud"

// reasonPositionLost — машинный признак отказа «позиция больше не возобновима».
//
// Клиент ключуется на НЕГО, а не на прозу сообщения: тон сообщения стабилен, но
// не разбираем.
const reasonPositionLost = "SUBSCRIPTION_POSITION_LOST"

// ProjectGate — как спросить модель прав про ДОСТУП К ПРОЕКТУ, названному осью
// подписки.
//
// # Почему отдельный вопрос, если каждая строка и так сужается
//
// Без него вызывающий, назвавший недоступный ему проект, получил бы ОТКРЫТЫЙ
// поток, молчащий вечно: ни одна строка не прошла бы построчное сужение, и это
// выглядело бы как «изменений нет». Молчание — утверждение о мире, и делать его
// про проект, которого вызывающий не вправе видеть, сервер не может.
//
// # Почему форма отказа приносится владельцем
//
// Отказ обязан быть НЕОТЛИЧИМ от «такого проекта нет»: различимый текст
// превращает подписку в способ узнать существование чужого проекта. Форма
// отсутствия принадлежит владельцу — он же отвечает ею на обычное чтение, —
// поэтому она приносится сюда, а не сочиняется здесь второй раз.
type ProjectGate struct {
	// ObjectType — тип объекта проекта в модели прав.
	ObjectType string
	// Action — действие, которым спрашивается доступ.
	Action string
	// Relations — отношения, любого из которых достаточно.
	Relations []string
	// NotFoundFormat — форма отсутствия владельца с ОДНИМ `%s` под
	// идентификатор (`"Project %s not found"`). Ею отвечает и отказ доступа.
	NotFoundFormat string
}

// declared — сказал ли владелец про стража хоть что-нибудь.
func (g ProjectGate) declared() bool {
	return g.ObjectType != "" || g.Action != "" || len(g.Relations) > 0 || g.NotFoundFormat != ""
}

func (g ProjectGate) validate() error {
	if g.ObjectType == "" || g.Action == "" || len(g.Relations) == 0 {
		return fmt.Errorf("subscription: ProjectGate объявлен неполно (ObjectType, Action и хотя бы одно Relations обязательны) — иначе ось project_id остаётся без стража, и недоступный проект отдаёт молчащий поток")
	}
	if strings.Count(g.NotFoundFormat, "%s") != 1 {
		return fmt.Errorf("subscription: ProjectGate.NotFoundFormat %q обязан нести ровно один %%s — форма отказа обязана совпасть с формой отсутствия владельца дословно, иначе различимый текст выдаёт существование чужого проекта", g.NotFoundFormat)
	}
	return nil
}

// Config — что приносит КОМПОЗИЦИОННЫЙ КОРЕНЬ владельца, поднимая сервер.
//
// Здесь стоят величины, принадлежащие ПОСАДКЕ, а не журналу: они приезжают из
// объявления сервиса (`pkg/servicecontract`), и потому не зашиты.
type Config struct {
	// Journal — объявление владельца: где журнал, каким каналом будит, как
	// строка становится событием.
	Journal Journal

	// DSN — строка соединения для ВЫДЕЛЕННОГО соединения вне пула. Подписка
	// держит его всё время своей жизни: `LISTEN` требует своей сессии, а
	// сессия из пула вернулась бы в него вместе с подпиской.
	DSN string

	// Narrower — сужатель по правам вызывающего, тот же, что у списков.
	//
	// Обязателен и обязан СУЖАТЬ. За этим методом нет пообъектной проверки на
	// крае (он `scope_filtered`), поэтому откатываться не на что: сужатель,
	// подвешенный и не сужающий, отдал бы весь журнал молча — под кодом,
	// который выглядит фильтрующим.
	Narrower *listnarrow.Narrower

	// ProjectGate — страж оси `project_id`. Обязателен у владельца, чей журнал
	// проектное измерение имеет; у [ProjectAbsent] запрещён — сторожить нечего.
	ProjectGate ProjectGate

	// MaxStreams — потолок числа ОДНОВРЕМЕННЫХ потоков процесса.
	//
	// Каждый поток держит своё соединение вне пула, поэтому потолок — не вкус, а
	// арифметика: число реплик × потолок + непуловые соединения обязаны
	// помещаться в предел владельца базы. Превышение отвечает ОТКАЗОМ, а не
	// молчаливой очередью: очередь превратила бы исчерпание в неограниченное
	// ожидание, неотличимое для клиента от «событий нет».
	MaxStreams int

	// StreamBudget — срок жизни одного потока. Приезжает из объявления сервиса
	// (`servicecontract.Spec.StreamBudget`) и здесь не имеет умолчания: величина
	// посадки, которую никто не выбирал, не обсуждаема и не сужаема.
	//
	// По истечении поток закрывается ЧИСТО (`OK`), а не ошибкой: клиент
	// возобновляется со своей позиции. Обрыв ошибкой читался бы как сетевой сбой.
	StreamBudget time.Duration

	// IdlePoll — холостой перепрос: как часто поток перечитывает журнал, не
	// дождавшись пробуждения.
	//
	// Он не «на всякий случай»: ОТКАТИВШИЙСЯ писатель уведомления не шлёт, и
	// подтверждение горизонта приезжает именно этим перепросом.
	IdlePoll time.Duration

	// Logger — журнал процесса. Ноль резолвится в [slog.Default].
	Logger *slog.Logger

	// now — часы. Только для проб: наблюдаемое поведение горизонта зависит от
	// времени, и проба, ждущая настоящих тридцати секунд, не проба.
	now func() time.Time
}

// Server — ОБЩИЙ сервер потока изменений. Один на платформу, по экземпляру на
// владельца журнала.
//
// Он реализует `subscriptionv1.InternalSubscriptionServiceServer`, и владелец
// регистрирует ЕГО на своём внутреннем слушателе — не свою обёртку вокруг него.
type Server struct {
	subscriptionv1.UnimplementedInternalSubscriptionServiceServer

	cfg  Config
	log  *slog.Logger
	now  func() time.Time
	slot chan struct{}
}

// NewServer собирает сервер и судит объявление.
//
// Отказ здесь — отказ ПОДЪЁМА, а не запроса: величина посадки, о которой никто
// не сказал, не должна обнаруживаться первым запросом в бою.
func NewServer(cfg Config) (*Server, error) {
	if err := cfg.Journal.Validate(); err != nil {
		return nil, err
	}
	if cfg.DSN == "" {
		return nil, fmt.Errorf("subscription: Config.DSN пуст — подписка держит выделенное соединение вне пула, и взять его неоткуда")
	}
	if cfg.Narrower == nil || !cfg.Narrower.Narrows() {
		return nil, fmt.Errorf("subscription: Config.Narrower отсутствует либо не сужает — за этим методом нет пообъектной проверки на крае, откатываться не на что")
	}
	if cfg.MaxStreams <= 0 {
		return nil, fmt.Errorf("subscription: Config.MaxStreams = %d — потолок числа потоков есть арифметика соединений базы, а не вкус; умолчания у него нет", cfg.MaxStreams)
	}
	if cfg.StreamBudget <= 0 {
		return nil, fmt.Errorf("subscription: Config.StreamBudget не объявлен — срок жизни потока приезжает из объявления сервиса (servicecontract.Spec.StreamBudget) и здесь не зашивается")
	}
	if cfg.IdlePoll <= 0 {
		return nil, fmt.Errorf("subscription: Config.IdlePoll не объявлен — откатившийся писатель уведомления не шлёт, и без холостого перепроса горизонт не подтверждается никогда")
	}
	if cfg.Journal.Storage.Project == ProjectAbsent {
		if cfg.ProjectGate.declared() {
			return nil, fmt.Errorf("subscription: ProjectGate объявлен у владельца без проектного измерения — сторожить нечего")
		}
	} else if err := cfg.ProjectGate.validate(); err != nil {
		return nil, err
	}

	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	return &Server{
		cfg:  cfg,
		log:  log,
		now:  now,
		slot: make(chan struct{}, cfg.MaxStreams),
	}, nil
}

// Subscribe — единственный глагол подписки.
//
// # Порядок отказов, и он ЕСТЬ ПРЕДМЕТ
//
//  1. ЛИЧНОСТЬ — безусловно и первой. Ответ безымянному не зависит от посадки:
//     его никто не назвал, и это верно при любой конфигурации;
//  2. СУЖАТЕЛЬ — есть ли он и сужает ли;
//  3. ФОРМА ЗАПРОСА — виды, идентификаторы, начало. Терминальные отказы обязаны
//     наступать ДО обращения к базе: иначе вызывающий получает повторяемый код
//     на ввод, который валидным не станет никогда, а отказ начинает зависеть от
//     доступности базы;
//  4. ДОСТУП К ПРОЕКТУ — прежде чем занимать слот и соединение;
//  5. СЛОТ — расход ограниченного ресурса;
//  6. СОЕДИНЕНИЕ и `LISTEN`.
func (s *Server) Subscribe(
	req *subscriptionv1.SubscriptionRequest,
	stream subscriptionv1.InternalSubscriptionService_SubscribeServer,
) error {
	ctx := stream.Context()

	// 1. Личность. Пустой субъект НЕ является субъектом.
	if _, err := listnarrow.SubjectFromContext(ctx); err != nil {
		s.log.Warn("subscription refused: request names no caller")
		return status.Error(codes.PermissionDenied, "subscription requires an authenticated caller")
	}

	// 2. Сужатель. Требование заявляется и здесь, и в месте, где строки
	// используются: страж у одной двери переживает появление второго
	// вызывающего цикла чтения.
	if s.cfg.Narrower == nil || !s.cfg.Narrower.Narrows() {
		s.log.Error("subscription refused: per-row visibility filter is absent or does not narrow")
		return status.Error(codes.PermissionDenied, "subscription is unavailable without per-event authorization")
	}

	// 3. Форма запроса.
	filter, err := s.cfg.Journal.Accept(req)
	if err != nil {
		return err
	}
	start, err := AcceptStart(req)
	if err != nil {
		return err
	}

	// 4. Доступ к проекту — до слота и до соединения.
	if filter.ProjectID != "" {
		allowed, gerr := listnarrow.AllowedOnObject(ctx, s.cfg.Narrower,
			s.cfg.ProjectGate.ObjectType, s.cfg.ProjectGate.Action,
			s.cfg.ProjectGate.Relations, filter.ProjectID)
		if gerr != nil {
			return gerr
		}
		if !allowed {
			// Байт-в-байт форма отсутствия владельца: различимый текст выдал бы
			// существование чужого проекта.
			return status.Errorf(codes.NotFound, s.cfg.ProjectGate.NotFoundFormat, filter.ProjectID)
		}
	}

	// 5. Слот.
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	default:
		return status.Error(codes.ResourceExhausted,
			"too many concurrent subscriptions (limit reached)")
	}

	// 6. Соединение и подписка на канал.
	budgetCtx, cancelBudget := context.WithTimeout(ctx, s.cfg.StreamBudget)
	defer cancelBudget()

	conn, err := s.dial(budgetCtx)
	if err != nil {
		return status.Error(codes.Unavailable, "subscription backend unavailable")
	}
	defer s.hangUp(conn)

	return s.serve(budgetCtx, conn, filter, start, stream)
}

// dial поднимает выделенное соединение и подписывается на канал под собственными
// ограниченными сроками: медленное соединение не должно надолго удерживать слот.
func (s *Server) dial(ctx context.Context) (*pgx.Conn, error) {
	connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	conn, err := pgx.Connect(connectCtx, s.cfg.DSN)
	cancel()
	if err != nil {
		return nil, err
	}
	// Имя канала — осуждённый идентификатор из объявления владельца, не ввод
	// вызывающего.
	listenCtx, listenCancel := context.WithTimeout(ctx, 2*time.Second)
	_, err = conn.Exec(listenCtx, "LISTEN "+s.cfg.Journal.Channel)
	listenCancel()
	if err != nil {
		s.hangUp(conn)
		return nil, err
	}
	return conn, nil
}

func (s *Server) hangUp(conn *pgx.Conn) {
	unlistenCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _ = conn.Exec(unlistenCtx, "UNLISTEN "+s.cfg.Journal.Channel)
	cancel()
	closeCtx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	_ = conn.Close(closeCtx)
	cancel2()
}

// serve — тело потока: служебное сообщение, догон, живая выдача.
func (s *Server) serve(
	ctx context.Context,
	conn *pgx.Conn,
	filter Filter,
	start Start,
	stream subscriptionv1.InternalSubscriptionService_SubscribeServer,
) error {
	storage := s.cfg.Journal.Storage
	// Состояние доставки — СВОЁ у каждого потока: у каждого свой вызывающий,
	// свой курсор и своё окно материализации гранта (`grant.go`).
	d := &delivery{}
	h := newWatermark(storage.Table, storage.PositionColumn, s.log, s.now)
	if err := s.settle(ctx, conn, h, storage.Table); err != nil {
		return err
	}
	if !h.Established() {
		// Срок потока истёк раньше, чем граница подтвердилась. Посадить
		// подписчика на неподтверждённый ноль нельзя (см. [Server.settle]), но и
		// закрыть поток МОЛЧА нельзя тоже.
		//
		// До служебного сообщения клиент не получал НИЧЕГО: ни позиции, с
		// которой возобновляться, ни кода. Чистый конец он читает как «событий
		// нет» и переоткрывает поток в тишине сколько угодно долго — немой
		// отказ, у которого нет ни клиентского, ни операторского признака.
		// Названный отказ восстанавливает следующий шаг: повторить.
		//
		// Исключение — ушедший КЛИЕНТ: адресата у отказа нет, и `Canceled`
		// сервером не производится, он лишь отражает чужое решение.
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		return status.Error(codes.Unavailable, "subscription position not settled")
	}

	floor := h.Floor(storage.Retention)

	cursor, err := s.resolveCursor(start, h, floor)
	if err != nil {
		return err
	}

	opened := &subscriptionv1.SubscriptionOpened{
		Position:       pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: cursor}),
		CaughtUp:       cursor >= h.Settled(),
		HonoredFilters: filter.Honored,
		// Словарь видов — тем же вызовом, каким отвергается неизвестный вид
		// (см. [Journal.Accept]). Второго перечня не существует, поэтому
		// объявленное клиенту и то, чем сервер судит, разойтись не могут.
		//
		// Он приходит ВСЕГДА, в том числе подписке, назвавшей ось `kinds`: иначе
		// прочесть словарь мог бы только тот, кто уже знает, что его не надо
		// сужать, — то есть тот, кому он не нужен.
		KnownKinds: s.cfg.Journal.KindDictionary(),
	}
	if storage.Retention == RetainsEverything {
		opened.RetainsEverything = true
	} else {
		opened.EarliestResumablePosition = pagetoken.EncodeSubscriptionPosition(
			pagetoken.SubscriptionPosition{Settled: floor})
	}
	if err := stream.Send(&subscriptionv1.SubscriptionMessage{
		Message: &subscriptionv1.SubscriptionMessage_Opened{Opened: opened},
	}); err != nil {
		return err
	}

	// Перепись доставки печатается ОДИН раз, при закрытии потока: обе её
	// величины окончательны только тогда. Она стоит `defer`-ом, чтобы называться
	// на КАЖДОМ исходе — чистом конце, отказе, уходе клиента, — иначе поток,
	// закончившийся отказом, унёс бы своё расхождение молча.
	defer d.census(s.log, storage.Table)

	for {
		cursor, err = s.drain(ctx, conn, h, d, cursor, filter, stream)
		if err != nil {
			return err
		}
		if err := s.waitForWork(ctx, conn); err != nil {
			return err
		}
		if ctx.Err() != nil {
			// Срок потока истёк либо клиент ушёл: закрываем ЧИСТО — клиент
			// возобновится со своей позиции. Обрыв ошибкой читался бы как
			// сетевой сбой, которым он не является.
			return nil
		}
		if err := h.Advance(ctx, conn); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return status.Error(codes.Unavailable, "subscription backend unavailable")
		}
	}
}

// settle доводит наблюдение границы до ПОДТВЕРЖДЁННОГО — и только после этого
// подписчика сажают.
//
// Неподтверждённая граница равна нулю, а ноль этот означает «позиции ещё нет».
// Усвоив его как позицию, подписчик, пришедший БЕЗ позиции, садится в НАЧАЛО
// журнала: ему уезжает вся накопленная история, которую он не просил, а
// служебное сообщение объявляет его при этом догнавшим — `caught_up` сравнивает
// курсор с той же нулевой границей. Строки журнала дренаж не удаляет, поэтому
// хвост бывает длинным (kacho#1386).
//
// Ждём РОВНО того писателя, который держал журнал в момент наблюдения: его
// завершение и есть подтверждение, а холостой перепрос закрывает случай отката,
// о котором уведомления не будет. Это тот же размен, что объявлен у самой
// границы, — «не потерять» против «доставить сейчас».
//
// Ждать приходится ОДИН проход, а не пока журнал опустеет: подтверждает границу
// уход писателей НАБЛЮДЁННОГО номера, и появление новых писателей выше него
// подтверждения не отзывает. Под сплошной записью пустого мига не бывает вовсе,
// и требование такого мига означало бы «поток не открывается никогда» (kacho#1386).
//
// В лог попадает УДЕРЖАНИЕ — горизонт, стоящий на одном и том же писателе
// дольше stallWarnAfter (жалоба живёт у самой границы, отдельной здесь нет).
// Движущийся горизонт жалобы не даёт и не должен: он не застрял. Единственный
// случай, когда подтверждения не будет вовсе, — незавершающийся писатель, и он
// назван обоим: оператору жалобой, клиенту отказом [codes.Unavailable] вместо
// чистого конца (см. [Server.serve]).
//
// Пустого журнала это не задерживает: подтверждать в нём нечего, наблюдение
// состоится первым же проходом.
func (s *Server) settle(ctx context.Context, conn *pgx.Conn, h *Watermark, table string) error {
	for {
		if err := h.Advance(ctx, conn); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return status.Error(codes.Unavailable, "subscription backend unavailable")
		}
		if h.Established() {
			return nil
		}
		if err := s.waitForWork(ctx, conn); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// resolveCursor выбирает, с какого номера отдавать, и отвергает позицию, которую
// владелец больше не удерживает.
func (s *Server) resolveCursor(start Start, h *Watermark, floor int64) (int64, error) {
	switch {
	case start.FromBeginning:
		return floor, nil
	case start.Position != nil:
		if start.Position.Settled < floor {
			return 0, positionLost(pagetoken.EncodeSubscriptionPosition(
				pagetoken.SubscriptionPosition{Settled: floor}))
		}
		return start.Position.Settled, nil
	default:
		return h.Settled(), nil
	}
}

// positionLost — ЯВНЫЙ отказ на невозобновимой позиции.
//
// Молчаливое начало с ближайшего удержанного места клиент записал бы как
// «изменений не было», и дописать этот исход потом стало бы ломающим изменением.
// Возобновимая позиция называется машинно, а не только прозой: клиент ключуется
// на признак, а не разбирает сообщение.
func positionLost(earliest string) error {
	st := status.New(codes.OutOfRange,
		"subscription position is no longer resumable; relist and subscribe from the earliest resumable position")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reasonPositionLost,
		Domain: errorDomain,
		Metadata: map[string]string{
			"earliest_resumable_position": earliest,
		},
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// waitForWork ждёт пробуждения либо холостого перепроса.
//
// Перепрос обязателен, а не «на всякий случай»: откатившийся писатель
// уведомления не шлёт, и подтверждение горизонта приезжает только им.
func (s *Server) waitForWork(ctx context.Context, conn *pgx.Conn) error {
	waitCtx, cancel := context.WithTimeout(ctx, s.cfg.IdlePoll)
	_, err := conn.WaitForNotification(waitCtx)
	cancel()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Истёк перепрос либо ушёл клиент — обе ветви возвращают к чтению, а
		// решение о выходе принимает вызывающий по состоянию своего контекста.
		return nil
	}
	// Соединение потеряно. Это НЕ «событий нет»: молчание было бы утверждением
	// о мире, которого сервер сделать не может.
	return status.Error(codes.Unavailable, "subscription notification stream lost")
}
