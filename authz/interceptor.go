// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/operations"
)

// InterceptorOptions — конфигурация gRPC interceptor'а.
type InterceptorOptions struct {
	// ServiceName — имя сервиса для метрик / логов: "kacho-vpc" / "kacho-compute" / etc.
	ServiceName string

	// Map — RPCMap.
	Map RPCMap

	// Client — implements CheckClient (gRPC client к InternalIAMService.Check).
	Client CheckClient

	// Cache — кеш положительных вердиктов. ОБЯЗАТЕЛЕН: nil → конструктор
	// отказывает (см. NewInterceptor). Тот же экземпляр передаётся в
	// listen_invalidate.go.
	//
	// Почему обязателен, а не «nil → заведём сами». Кешируется только
	// «разрешено», поэтому срок жизни записи И ЕСТЬ окно отзыва — время, в
	// течение которого субъект с уже отобранным правом продолжает проходить.
	// Это параметр безопасности; `RevocationPolicy` объявляет, каким ему быть
	// позволено, а перепись накрывает ровно те площадки, которые кеш НАЗЫВАЮТ.
	// Пока конструктор заводил кеш за молчащего вызывающего, существовал второй
	// путь получить окно — не попав ни в перепись, ни под потолок, и не оставив
	// в composition root ни одной строки, которую кто-нибудь прочтёт.
	//
	// Своего числа не имеешь — передай `NewCache(0)`: это ЯВНОЕ «беру
	// умолчание политики», и гейт учитывает такую площадку как унаследованную.
	Cache *Cache

	// Logger — slog logger.
	Logger *slog.Logger

	// Breakglass — если true, interceptor пропускает все RPC без Check + WARN.
	// Source: env `KACHO_<SVC>_AUTHZ__BREAKGLASS=true` (читать в composition root).
	Breakglass bool

	// DenyRateLimitPerSec — token-bucket per-Principal на denied storm.
	// 0 / negative → **disabled**.
	//
	// Умолчания у этого поля НЕТ — ни здесь, ни в конструкторе. Не заполнил
	// вызывающий, значит бюджет выключен, и никакой темп проверок не
	// ограничивается. Прежняя редакция этой строки писала «default 100/s»: сотня
	// действительно стоит у vpc (`authz.deny-rate-limit-per-sec`) и у nlb
	// (литерал в composition root), но это ИХ выбор, а не поведение corelib.
	// Формулировка читалась как «не заполнишь — получишь сотню», то есть
	// описывала защиту там, где её нет, и ровно в ту сторону, в которую ошибаться
	// нельзя. Заполняй явно тот, кому бюджет нужен.
	//
	// Бюджет тратит РОВНО ОДИН класс исходов: те, которые кэш не поглощает, —
	// отказ, отказ с сокрытием существования, промах «нет пути» и недоступность
	// модели. Все они уходят в модель прав на КАЖДОМ повторе (кэшируются только
	// положительные ответы) и потому образуют шторм, который больше нечем
	// ограничить.
	//
	// Разрешение бюджет НЕ тратит — единственное исключение, и оно обосновано:
	// положительный ответ кэшируется, то есть уже самоограничен. Пока он платил,
	// аутентифицированный вызывающий, обходящий много разных объектов на холодном
	// кэше, получал ResourceExhausted на запросах, которые ему разрешены.
	//
	// Недоступность модели ПЛАТИТ намеренно: сбрасывать нагрузку с падающего
	// kaname особенно важно, а CheckTimeout ограничивает лишь длительность
	// одного вызова, не их темп. Из-за этого текст отказа не называет отказы (см.
	// decisionError) — иначе он описывал бы перебой как шторм отказов вызывающего.
	DenyRateLimitPerSec float64

	// CheckTimeout — таймаут на один Check-call.
	// Default 2*time.Second (если ≤0).
	CheckTimeout time.Duration

	// SubjectExtractor — функция, извлекающая (subject string, ok bool) из
	// ctx. По умолчанию — `defaultSubjectExtractor` использует
	// `operations.PrincipalFromContext(ctx)`. Можно переопределить
	// для тестов.
	SubjectExtractor func(ctx context.Context) (subjectFGA string, principalID string, ok bool)

	// AllowSystemPrincipal — если true, system-principal (Type="system",
	// ID="bootstrap") пропускается без Check. Используется для bootstrap'а /
	// миграции / фоновых job'ов, которым нет смысла Check'аться. Default false.
	AllowSystemPrincipal bool
}

// Interceptor реализует gRPC unary + stream interceptor'ы.
//
// Собирает его НОСИТЕЛЬ (`pkg/servicehost/serve.go`), а не композиционный
// корень сервиса: сервис объявляет источник решения полем дескриптора, и
// носитель по нему либо берёт клиента у владельца модели, либо набирает соседа
// по объявленному ребру. Здесь стояла координата в дереве отдельного сервиса —
// такого файла нет, и ручной сборки цепочки в сервисах тоже нет:
//
//	authzIntr := authz.NewInterceptor(authz.InterceptorOptions{...})
//	grpc.NewServer(
//	    grpc.ChainUnaryInterceptor(... , authzIntr.Unary()),
//	    grpc.ChainStreamInterceptor(... , authzIntr.Stream()),
//	)
type Interceptor struct {
	opts        InterceptorOptions
	rateLimiter *rateLimiter

	// counters (lock-free atomic для observability).
	//
	// Величин КЕША здесь нет намеренно: их считает сам кеш (`Cache.Stats`).
	// Собственный счётчик попаданий у звена был вторым местом учёта одной
	// величины — а два места об одном предмете расходятся молча, и разойтись им
	// есть на чём: истечения записи, давления потолка и снятия звено не видит
	// вовсе.
	allowedTotal     uint64
	deniedTotal      uint64
	unavailableTotal uint64
	breakglassTotal  uint64
	unmappedTotal    uint64
	rateLimitedTotal uint64
}

// NewInterceptor конструктор. Panics при invalid options.
func NewInterceptor(opts InterceptorOptions) *Interceptor {
	if opts.Map == nil {
		panic("authz: InterceptorOptions.Map is nil")
	}
	if opts.Client == nil && !opts.Breakglass {
		panic("authz: InterceptorOptions.Client is nil and Breakglass=false")
	}
	// Отказ, а не умолчание. Кеш положительных вердиктов задаёт окно отзыва
	// (см. InterceptorOptions.Cache); заведённый за молчащего вызывающего, он
	// дал бы окно, которого никто не выбирал и которое не попадает в перепись
	// RevocationPolicy. Текст читает оператор при отказе в старте — он обязан
	// называть и ручку, и выход.
	//
	// Выход здесь ОДИН — своя ручка. Прежняя редакция текста предлагала второй,
	// `NewCache(0)` как «взять умолчание платформы», и предлагала недостижимое:
	// держатель окна без собственной ручки роняет гейт дерева
	// TestEveryVerdictCacheProcessDeclaresItsOwnKnob, а на носителе контура не
	// поднимается вовсе (`pkg/servicecontract`: у окна
	// «умолчания быть не может»). Текст отказа, советующий запрещённое, — худший
	// вид неисполнимой возможности: его читают ровно в ту минуту, когда ищут, что
	// делать дальше. Неположительная величина остаётся законной ровно как
	// «ручка есть, значение не задано» (RevocationPolicy.Resolve), а не как
	// «ручки нет».
	if opts.Cache == nil {
		panic("authz: InterceptorOptions.Cache is nil: кеш положительных вердиктов задаёт окно отзыва доступа и обязан быть назван явно; передайте NewCache(ttl) с величиной из СВОЕЙ ручки окна — процесс без собственной ручки окна отзыва не поднимается, см. pkg/authz.RevocationPolicy")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.CheckTimeout <= 0 {
		opts.CheckTimeout = 2 * time.Second
	}
	if opts.SubjectExtractor == nil {
		opts.SubjectExtractor = defaultSubjectExtractor
	}
	if opts.ServiceName == "" {
		opts.ServiceName = "kacho"
	}
	return &Interceptor{
		opts:        opts,
		rateLimiter: newRateLimiter(opts.DenyRateLimitPerSec),
	}
}

// verdict — итог authorize(): решение + объект, к которому оно относится.
// Объект нужен уже ПОСЛЕ решения — из него собирается текст existence-hiding
// NOT_FOUND, который обязан совпасть с настоящим промахом владельца (см.
// hide_existence.go). Раньше решение возвращалось без объекта, и текст сказать
// было нечем — отсюда голое "not found", отличимое от контрактного тона.
type verdict struct {
	decision   Decision
	objectType string
	objectID   string
	err        error
}

// decisionError мапит verdict в gRPC-ошибку, которую interceptor возвращает
// вместо вызова handler'а. Passthrough-решения (Allowed/Internal/NoPath — надо
// вызвать handler) дают nil. ЕДИНЫЙ источник маппинга для Unary() и Stream():
// новый Decision обрабатывается ровно в одном месте, иначе unary/stream-switch'и
// расходятся вручную и streaming-RPC молча ломается на не-учтённом решении.
func decisionError(v verdict) error {
	dec, err := v.decision, v.err
	switch dec {
	case DecisionAllowed, DecisionInternal, DecisionNoPath:
		// Passthrough: caller вызывает handler (Internal — internal RPC без Check).
		return nil
	case DecisionHideExistence:
		// Existence-hiding: объект есть, но caller не вправе видеть → NOT_FOUND
		// (handler НЕ вызывается, чтобы не слить ресурс). Текст — ПОБАЙТНО тот же,
		// что отдал бы владелец на реальном промахе: иначе «есть-но-не-твой»
		// отличимо от «нет такого» по одному лишь сообщению, и сокрытие
		// превращается в оракул существования.
		return status.Error(codes.NotFound, hideExistenceMessage(v.objectType, v.objectID))
	case DecisionDenied:
		return status.Error(codes.PermissionDenied, "permission denied")
	case DecisionUnavailable:
		// Fail-closed — но НЕ голосом решения о правах.
		//
		// Запрос отвергается так же, как на отказе: обработчик не вызывается, ничего
		// не происходит. Расходится ОДНО — код, и он и есть то единственное, по чему
		// вызывающий решает, осмыслен ли повтор. `PERMISSION_DENIED` означает «тебе
		// нельзя», то есть «повторять бессмысленно»: решение зависит от вызывающего,
		// отношения и объекта, и повтор идентичного запроса не меняет ни одного из
		// трёх. Здесь же не сказано НИЧЕГО о правах — модель просто не ответила
		// вовремя, и через мгновение ответит.
		//
		// Цена подмены наблюдалась на стенде и она несимметрична. У потребителя,
		// разбирающего ответ соседа по полосам, ветка недоступности не срабатывала
		// НИКОГДА — этот код до неё не доходил, — и перебой длиной в доли секунды
		// становился терминальным отказом операции; на пути освобождения аренды это
		// означало изоляцию ресурса как отравленного. Обратная ошибка (одеть отказ в
		// код недоступности) стоила бы вечного повтора запроса, который не пройдёт
		// никогда, — поэтому полосы обязаны звучать по-разному, а не «строже».
		//
		// Тот же ответ на то же условие уже дают край (`gateway/internal/middleware`)
		// и сервисы, решающие пообъектно (registry, nlb): недоступность владельца
		// модели — `UNAVAILABLE`, fail-closed. Здесь было единственное расхождение.
		// Полосы держит проба `decision_lane_codes_test.go` (задача #497).
		return status.Error(codes.Unavailable, "authorization service unavailable")
	case DecisionUnmapped:
		// Fail-closed для не-mapped RPC.
		return status.Error(codes.PermissionDenied, "permission denied (rpc not mapped)")
	case DecisionRateLimited:
		// Формулировка НЕ называет отказы: бюджет тратит каждый исход, который кэш
		// не поглощает (отказ, сокрытие, промах «нет пути», недоступность модели),
		// и назвать их все «denied» значило бы соврать оператору в трёх случаях из
		// четырёх (см. InterceptorOptions.DenyRateLimitPerSec).
		return status.Error(codes.ResourceExhausted, "too many authorization checks; retry later")
	default:
		// Unknown decision — fail-closed.
		if err != nil {
			return status.Errorf(codes.PermissionDenied, "%v", err)
		}
		return status.Error(codes.PermissionDenied, "permission denied (unknown decision)")
	}
}

// Unary возвращает grpc.UnaryServerInterceptor.
func (i *Interceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		v := i.authorize(ctx, info.FullMethod, req)
		if derr := decisionError(v); derr != nil {
			return nil, derr
		}
		return handler(ctx, req)
	}
}

// Stream возвращает grpc.StreamServerInterceptor.
//
// На stream-RPC interceptor извлекает Authorization decision до открытия
// stream'а. Дальше идет обычный wrapping.
//
// NOTE: для stream-RPC `req` недоступен в interceptor'е до первого Recv() —
// поэтому StaticExtractor должен либо использовать пустой request (если
// RPCEntry knows fixed object_id вне request'а), либо stream-RPC не покрыт
// этим interceptor'ом (пометка Public=true в RPCMap для известных stream'ов
// типа `InternalResourceLifecycleService.Subscribe`).
//
// На MVP — все public stream-RPC должны помечать ObjectExtractor как
// возвращающий статичный object (e.g. project-scope из SubscribeRequest).
func (i *Interceptor) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Stream req не доступен до первого Recv → для stream-RPC передаем nil как req.
		// Это работает для типичных Watch / Subscribe streams, где либо метод public
		// (e.g. InternalResourceLifecycleService.Subscribe — Public=true), либо
		// extractor явно проектирует request'-less проверку.
		v := i.authorize(ss.Context(), info.FullMethod, nil)
		if derr := decisionError(v); derr != nil {
			return derr
		}
		return handler(srv, ss)
	}
}

// authorize — основная логика (вне зависимости от unary/stream). Возвращает
// verdict: решение ПЛЮС объект, к которому оно относится (нужен для текста
// existence-hiding — см. hide_existence.go).
func (i *Interceptor) authorize(ctx context.Context, fullMethod string, req any) verdict {
	logger := i.opts.Logger.With(
		slog.String("rpc", fullMethod),
		slog.String("service", i.opts.ServiceName),
	)

	// 1. Break-glass — обходит authz-Check, но **anonymous все равно denied**.
	// breakglass=true НЕ должен пускать anonymous'а создавать VPC/Compute
	// ресурсы — он лишь эмулирует "all authenticated users allowed", не
	// "everyone allowed".
	if i.opts.Breakglass {
		if isAnonymousSubject(ctx, i.opts.SubjectExtractor) {
			atomic.AddUint64(&i.deniedTotal, 1)
			logger.Warn("authz_breakglass_anonymous_denied")
			return verdict{decision: DecisionDenied, err: nil}
		}
		atomic.AddUint64(&i.breakglassTotal, 1)
		logger.Warn("authz_breakglass_used")
		return verdict{decision: DecisionAllowed, err: nil}
	}

	// 2. Lookup RPC. Любой не-замапленный RPC fail-closed — без name-based
	// исключений. Internal RPC, который должен быть exempt, обязан явно стоять в
	// RPCMap (Relation для Check либо Public=true): per-RPC authz-Check
	// обязателен на ОБОИХ listener'ах (public и :9091), а молчаливое исключение
	// по имени "Internal*" было fail-open вектором на internal-периметре.
	entry, ok := i.opts.Map.Lookup(fullMethod)
	if !ok {
		atomic.AddUint64(&i.unmappedTotal, 1)
		logger.Warn("authz_unmapped_rpc")
		return verdict{decision: DecisionUnmapped, err: ErrUnmapped}
	}
	if entry.Public {
		return verdict{decision: DecisionInternal, err: nil}
	}
	// 3. Subject extract. Идёт ПЕРЕД ветвлением на ScopeFiltered: «не спрашивать
	// про один объект» и «не выяснять, кто звонит» — разные вещи, и вторая никогда
	// не разрешена.
	subjectFGA, principalID, ok := i.opts.SubjectExtractor(ctx)
	if !ok {
		// Нет Principal'а в ctx → fail-closed.
		logger.Warn("authz_no_principal")
		atomic.AddUint64(&i.deniedTotal, 1)
		return verdict{decision: DecisionDenied, err: nil}
	}
	if entry.ScopeFiltered {
		// RPC, авторизуемый на УРОВНЕ ДАННЫХ: строки принадлежат объектам с
		// индивидуальными владельцами, которых запрос не называет, поэтому единого
		// объекта для одного вопроса не существует — единичный Check отверг бы весь
		// вызов `no path` ещё до того, как сужение отработает. Handler читает
		// страницу/батч и спрашивает модель про идентификаторы ЭТОЙ порции.
		//
		// Единичный Check снимается — выяснение личности НЕТ. Под таким RPC нет
		// второго рубежа: per-RPC Check отсутствует по построению, поэтому
		// неназванный вызывающий плюс отсутствующий (выключенный, деградировавший)
		// фильтр — снова исходная дыра. Отсечка выше безусловна и не зависит ни от
		// режима, ни от того, подвешен ли фильтр.
		return verdict{decision: DecisionInternal, err: nil}
	}
	if i.opts.AllowSystemPrincipal && principalID == "bootstrap" {
		// Gate the blanket allow to the GENUINE system identity
		// (Principal{Type:"system", ID:"bootstrap"}), never a client-supplied
		// principal-id string alone. The principal-id ("bootstrap") is derivable
		// from the x-kacho-principal-id header, which the non-trust-aware
		// UnaryPrincipalExtract honours unconditionally; matching on the id string
		// alone let a forged {Type:"user", ID:"bootstrap"} skip every per-RPC Check
		// (CWE-863). We re-read the canonical, type-carrying principal from ctx and
		// require the full system Type+ID; anything else falls through to the normal
		// Check path (fail-closed). NOTE: this closes the type-confusion vector but
		// does NOT by itself authenticate the peer — AllowSystemPrincipal must still
		// be enabled ONLY on a listener no untrusted peer can reach, or paired with
		// the trust-aware grpcsrv.UnaryTrustedPrincipalExtract (see
		// docs/architecture/known-divergences.md).
		if p, ok := operations.PrincipalFromContextOK(ctx); ok && p.Type == "system" && p.ID == "bootstrap" {
			return verdict{decision: DecisionAllowed, err: nil}
		}
		logger.Warn("authz_system_principal_type_mismatch",
			slog.String("principal_id", principalID))
	}

	// 4. Object extract.
	objectType, objectID, err := entry.Extract(req)
	if err != nil {
		logger.Warn("authz_object_extract_failed", slog.String("err", err.Error()))
		atomic.AddUint64(&i.deniedTotal, 1)
		return verdict{decision: DecisionDenied, err: fmt.Errorf("object extract: %w", err)}
	}
	object, err := FormatObject(objectType, objectID)
	if err != nil {
		logger.Warn("authz_object_format_failed", slog.String("err", err.Error()))
		atomic.AddUint64(&i.deniedTotal, 1)
		return verdict{decision: DecisionDenied, err: err}
	}

	// 5. Cache lookup. Кешируются только positive-результаты (negative никогда
	// не кешируется — см. cache.go package-doc), поэтому hit ⇒ allowed.
	if _, hit := i.opts.Cache.Get(subjectFGA, entry.Relation, objectType, objectID); hit {
		// Попадание учитывает сам кеш — он же учтёт промах на ветке ниже. Второй
		// счётчик здесь разъехался бы с первым на первом же ленивом снятии
		// истёкшей записи.
		atomic.AddUint64(&i.allowedTotal, 1)
		return verdict{decision: DecisionAllowed, err: nil}
	}

	// 6. Denied-storm cut-off: бюджет СПРАШИВАЕТСЯ до Check-call (отсечка после
	// вопроса не сняла бы нагрузку с kaname), но ТРАТИТСЯ ниже и только на
	// исход, который кэш не поглощает (см. DenyRateLimitPerSec). Bucket
	// per-Principal.
	if !i.rateLimiter.HasBudget(principalID) {
		atomic.AddUint64(&i.rateLimitedTotal, 1)
		logger.Warn("authz_rate_limited",
			slog.String("principal_id", principalID))
		return verdict{decision: DecisionRateLimited, err: nil}
	}

	// 7. Check call.
	cctx, cancel := context.WithTimeout(ctx, i.opts.CheckTimeout)
	defer cancel()
	allowed, err := i.opts.Client.Check(cctx, subjectFGA, entry.Relation, object)
	if err != nil {
		if errors.Is(err, ErrNoPath) {
			// No FGA hierarchy tuple for this object → resource likely does not
			// exist. Let the handler run: it will return NOT_FOUND from the DB.
			// This prevents authz from masking NOT_FOUND as 403 PermissionDenied.
			// Not cached, so enumeration of absent ids repeats the question every
			// time — it spends the budget like any other un-absorbed outcome.
			i.rateLimiter.Charge(principalID)
			atomic.AddUint64(&i.allowedTotal, 1)
			logger.Info("authz_no_path_passthrough",
				slog.String("subject", subjectFGA),
				slog.String("relation", entry.Relation),
				slog.String("object", object))
			return verdict{decision: DecisionNoPath, err: nil}
		}
		if errors.Is(err, ErrHideExistence) {
			// Объект существует, но caller не вправе видеть → existence-hiding:
			// блокируем handler и отдаём NOT_FOUND (не PermissionDenied), чтобы
			// «есть-но-не-твой» было неотличимо от «нет такого». Это отказ, и он
			// тоже не кэшируется → тратит бюджет.
			//
			// # Почему здесь НЕТ стража `ownerNotFoundText != ""`, который есть у
			// соседней ветки обычного отказа (ниже)
			//
			// Асимметрия НАМЕРЕННАЯ, и разводятся ветки по тому, КТО принял
			// решение скрывать. Ниже его принимает эта функция — по форме вызова и
			// записи каталога, — и раз решение её, она вправе от него отказаться,
			// когда говорить нечем: остаётся `PermissionDenied`. Здесь решение уже
			// принято ВЫШЕ, решателем, который сверил объект со своей базой; наше
			// дело — озвучить его как можно менее информативно
			// (см. godoc `hideExistenceMessage`).
			//
			// Откатиться тут к `PermissionDenied` значило бы обменять примету в
			// ТЕКСТЕ (404 «not found» против 404 «Widget abc not found») на примету
			// в КОДЕ (403 против 404) — то есть сделать различие ЛЕГЧЕ читаемым, а
			// не закрыть его. Правильный ответ у состояния «скрываем тип, за
			// который нечем говорить» один: его не должно существовать. Носитель
			// контура делает его недостижимым отказом старта
			// (`servicehost.auditProbedTypes`, О5б): каждый тип, о котором спросят
			// порт существования, обязан иметь строку в таблице промахов
			// владельцев, иначе процесс не поднимается.
			i.rateLimiter.Charge(principalID)
			atomic.AddUint64(&i.deniedTotal, 1)
			logger.Warn("authz_hide_existence",
				slog.String("subject", subjectFGA),
				slog.String("relation", entry.Relation),
				slog.String("object", object))
			// objectType/objectID ride along: the NOT_FOUND text is built from
			// them so it matches the owner's own miss byte for byte.
			return verdict{decision: DecisionHideExistence, objectType: objectType, objectID: objectID}
		}
		// Тоже не кэшируется, и происходит ровно тогда, когда модель прав уже
		// не справляется → бюджет обязан сбрасывать с неё нагрузку. CheckTimeout
		// ограничивает длительность ОДНОГО вызова, но не их темп.
		i.rateLimiter.Charge(principalID)
		atomic.AddUint64(&i.unavailableTotal, 1)
		logger.Error("authz_check_unavailable",
			slog.String("subject", subjectFGA),
			slog.String("relation", entry.Relation),
			slog.String("object", object),
			slog.String("err", err.Error()))
		return verdict{decision: DecisionUnavailable, err: errors.Join(ErrUnavailable, err)}
	}
	if !allowed {
		// Отказ не кэшируется → каждый повтор снова уходит в модель прав. Это и
		// есть шторм, который бюджет ограничивает.
		i.rateLimiter.Charge(principalID)
		atomic.AddUint64(&i.deniedTotal, 1)
		// Пообъектное чтение (и мутация, объявленная скрывающей) отвечает голосом
		// ВЛАДЕЛЬЦА: тем же NOT_FOUND, что даёт настоящий промах. Иначе вызывающий
		// отличает «нет доступа» от «нет такого» по одному лишь ответу — а край на
		// том же запросе уже отвечает промахом, и расхождение двух решателей
		// (у каждого свой кэш положительных вердиктов со своим окном) выносит эту
		// разницу наружу. Handler не вызывается ни в той, ни в другой ветке —
		// решение об отказе не смягчается, меняется только его ЗВУЧАНИЕ.
		if HidesExistenceOnDeny(fullMethod, entry, objectType) && ownerNotFoundText(objectType, objectID) != "" {
			logger.Warn("authz_denied_hidden",
				slog.String("subject", subjectFGA),
				slog.String("relation", entry.Relation),
				slog.String("object", object))
			return verdict{decision: DecisionHideExistence, objectType: objectType, objectID: objectID}
		}
		logger.Warn("authz_denied",
			slog.String("subject", subjectFGA),
			slog.String("relation", entry.Relation),
			slog.String("object", object))
		return verdict{decision: DecisionDenied, err: nil}
	}

	// 8. Cache positive.
	i.opts.Cache.SetAllowed(subjectFGA, entry.Relation, objectType, objectID)
	atomic.AddUint64(&i.allowedTotal, 1)
	return verdict{decision: DecisionAllowed, err: nil}
}

// Metrics — снимок величин звена решения о доступе.
//
// Читается коллектором `pkg/authz/authzmetrics`, зарегистрированным
// композиционным корнем сервиса на его диагностической поверхности. Прежде этот
// снимок объявлял себя «счётчиками для Prometheus», не имея в прод-коде НИ
// ОДНОГО читателя: величины росли и никуда не выходили, а «отказов не было» и
// «звено не спрашивали» снаружи выглядели одинаково.
type Metrics struct {
	Allowed     uint64
	Denied      uint64
	Unavailable uint64
	Breakglass  uint64
	Unmapped    uint64
	RateLimited uint64

	// Cache — величины окна вердиктов, прочитанные у САМОГО окна.
	//
	// Вложенным полем, а не парой плоских счётчиков рядом: у окна пять величин, и
	// доля попаданий без промахов не считается (нет знаменателя), а без размера и
	// причин вытеснения не объясняется. Из решений они не выводятся ни одна:
	// `Allowed` считает и попадание, и пропуск «нет пути», а `Denied` — ещё и
	// отказы, случившиеся ДО обращения к окну (разбор объекта, форматирование),
	// то есть вопросы, которых окну не задавали вовсе.
	Cache CacheStats
}

// Metrics возвращает snapshot счетчиков.
func (i *Interceptor) Metrics() Metrics {
	return Metrics{
		Allowed:     atomic.LoadUint64(&i.allowedTotal),
		Denied:      atomic.LoadUint64(&i.deniedTotal),
		Unavailable: atomic.LoadUint64(&i.unavailableTotal),
		Breakglass:  atomic.LoadUint64(&i.breakglassTotal),
		Unmapped:    atomic.LoadUint64(&i.unmappedTotal),
		RateLimited: atomic.LoadUint64(&i.rateLimitedTotal),
		Cache:       i.opts.Cache.Stats(),
	}
}

// CacheStats — величины окна вердиктов ЭТОГО звена.
//
// Отдельный метод, а не поле снимка решений: окно наблюдают собиратели метрик, и
// им нужны размер и причины вытеснения, которых у решений нет. Метод отдаёт
// величины ТОГО кеша, который звено и спрашивает, — второго экземпляра у него
// нет by construction (кеш обязателен полем опций, см. [NewInterceptor]).
func (i *Interceptor) CacheStats() CacheStats { return i.opts.Cache.Stats() }

// EvictInactiveSubjects — для periodic background job; удаляет rate-limiter
// buckets, у которых lastSeen старше maxAge. Вернет кол-во удаленных.
func (i *Interceptor) EvictInactiveSubjects(maxAge time.Duration) int {
	return i.rateLimiter.EvictInactive(maxAge)
}
