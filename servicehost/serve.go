// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package servicehost — НОСИТЕЛЬ входящего пути: одна точка входа, поднимающая
// оба слушателя сервиса с одним и тем же контуром работы с владельцем прав.
//
// # Что здесь механизм, а что правило
//
// [Serve] возвращает `error`, а НЕ сервер, и регистрация получает
// `grpc.ServiceRegistrar` — интерфейс с единственным методом. Поэтому у
// вызывающего не остаётся объекта, к которому можно приделать своё звено:
// восьмой сервис, желающий собственный порядок цепочки, не «нарушит правило» —
// ему не на чем его записать. Это свойство ПОСТРОЕНИЯ.
//
// Порядок звеньев внутри самой функции построением не держится — его держит то,
// что функция ОДНА и правка её видна как правка контура, а не как правка
// сервиса. Это честная граница, и выдавать её за невозможность нельзя.
//
// # Чем этот пакет отличается от `pkg/servicecontract`
//
// Тот решает, ЧТО МОЖНО ЗАПИСАТЬ, и его отказы — свойства самого дескриптора
// (О1, О6, О7, О8, О10). Здесь живут отказы, которым нужен СЛУЖИМЫЙ НАБОР RPC и
// выведенный из него каталог прав: О2, О3, О4, О5, О9. Разделение не
// косметическое: половину отказов невозможно вычислить, пока не известно, что
// процесс реально служит.
package servicehost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/catalogderive"
	"github.com/PRO-Robotech/corelib/grpcclient"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// Registrar приносит ТОЛЬКО обработчики.
//
// Тип не позволяет добавить звено — это и есть фиксированный слот. Аргумент —
// `grpc.ServiceRegistrar`, интерфейс с единственным методом `RegisterService`;
// сервера вызывающий не получает ни в каком виде.
//
// Регистратор вызывается РОВНО ОДИН раз на слушатель. «Позовём дважды, он же
// чистый» контрактом не является: чистоту чужой функции проверить нечем, а
// побочный эффект во втором вызове проявился бы как удвоение фоновой работы.
type Registrar func(grpc.ServiceRegistrar)

// Serve поднимает ОБА слушателя и обслуживает их до отмены ctx.
//
// Порядок, и он несущий: собрать серверы → зарегистрировать → снять СЛУЖИМЫЙ
// набор у самих серверов → вывести каталог и карту прав → прогнать отказы
// старта → и только потом слушать. Ни одно соединение не принимается раньше,
// чем отработали все десять отказов.
func Serve(ctx context.Context, d servicecontract.Descriptor, public, internal Registrar) error {
	if !d.Accepted() {
		return errors.New("servicehost: дескриптор не принят конструктором (servicecontract.New). " +
			"Нулевое значение собирается литералом и не проходило ни одного отказа старта — " +
			"поднимать по нему процесс значит не проверить ничего")
	}
	// Заявление о собственном контуре СТОИТ процесса носителя, и вот это место —
	// его цена. Без отказа здесь `OwnContour` был бы ручкой, снимающей проверки
	// проводки: объяви её — и носитель всё равно поднимет контур, а объявленного
	// в дескрипторе не будет вовсе. Поэтому объявивший собственный контур обязан
	// собрать его сам, целиком.
	if because := d.OwnContour(); because != "" {
		return fmt.Errorf("servicehost: дескриптор объявил СОБСТВЕННЫЙ контур входящего пути (%q) — "+
			"проводки носителя он не несёт (её принесение отвергается отказом старта О14), "+
			"и поднимать по нему контур нечем. Либо снимите OwnContour и принесите проводку, "+
			"либо не зовите носителя", because)
	}
	spec := d.Spec()
	log := spec.Logger

	// ── звено решения о доступе: слот, заполняемый ПОСЛЕ отказов старта ──────
	var slot decisionSlot

	publicSrv, internalSrv, pairErr := serverPair(spec, &slot)
	if pairErr != nil {
		return pairErr
	}

	// ── потолок темпа: обёртка регистратора, а не звено цепочки ──────────────
	//
	// Регистрация идёт ЧЕРЕЗ ограничитель, и другого пути зарегистрировать
	// службу у вызывающего нет — сервера он не получает. Поэтому «слушатель без
	// потолка» перестаёт быть тем, что автор сервиса обязан не забыть.
	adm, admErr := buildAdmission(spec)
	if admErr != nil {
		return admErr
	}
	because, _ := spec.Admission.NotApplicableBecause()
	adm.arm(log, spec.Service, because)
	adm.handOut(publicSrv, internalSrv, public, internal)

	served := mergeServed(servedOf(publicSrv), servedOf(internalSrv))
	domains, err := domainsOf(served)
	if err != nil {
		return err
	}
	rpcMap, err := catalogderive.Derive(domains...)
	if err != nil {
		return fmt.Errorf("servicehost: %s не поднимается — карта прав не выводится из аннотаций "+
			"дескрипторов, слинкованных в этот бинарь: %w", spec.Service, err)
	}
	cat := catalogOf(domains)

	c, err := audit(spec, served, cat, rpcMap)
	if err != nil {
		return err
	}
	// Перепись печатается ВСЕГДА, а не только при находках: «ноль находок»
	// обязано быть отличимо от «ноль прочитанного». Процесс, поднявшийся молча,
	// неотличим от процесса, чьи отказы ничего не осмотрели.
	log.Info("servicehost: start refusals passed",
		"service", string(spec.Service),
		"mode", spec.Mode.String(),
		"census", c.String(),
		"domains", domains)

	intr, closeEdge, err := decisionLink(spec, rpcMap)
	if err != nil {
		return err
	}
	if closeEdge != nil {
		defer closeEdge()
	}
	slot.install(intr)

	// Счёт допущенных и отвергнутых — задача носителя, а не сервиса: она живёт
	// столько же, сколько слушатели, и гаснет вместе с ними. Своя отмена нужна
	// затем, чтобы падение слушателя не оставляло задачу ждать общий контекст:
	// [Serve] обязана вернуть управление, а не пережить свои же серверы.
	reportCtx, stopReport := context.WithCancel(ctx)
	defer stopReport()
	reportDone := make(chan struct{})
	go func() {
		defer close(reportDone)
		adm.report(reportCtx, log)
	}()

	serveErr := listenAndServe(ctx, spec, publicSrv, internalSrv)
	stopReport()
	<-reportDone
	return serveErr
}

// serverPair собирает ОБА сервера из ОДНОЙ пары цепочек.
//
// Цепочки строятся по одному разу и подаются обоим слушателям — то есть
// «внутренний получает то же, что публичный» перестаёт быть свойством, которое
// автор обязан выдержать, и становится свойством построения: другой цепочки в
// этой функции просто нет. «Internal = доверенный» — запрещённое допущение, и
// прежняя редакция звала строитель дважды, оставляя место для того, чтобы однажды
// освободить внутренний слушатель от звена.
//
// Наблюдаемая половина того же свойства держится пробой
// `TestBothListenersRefuseIdenticallyOnTheWire`: она поднимает ОБА сервера,
// собранных этой функцией, и сверяет, что вызывающий видит от них одно и то же.
func serverPair(spec servicecontract.Spec, slot *decisionSlot) (public, internal *grpc.Server, err error) {
	// Измеритель задержки — ОДИН на процесс, полос у него две.
	//
	// Один: серии заводятся в реестре, и второй измеритель над тем же реестром
	// был бы повторной регистрацией того же семейства. Две полосы: один и тот же
	// метод служится обоими слушателями, и слитый ряд — среднее двух разных
	// величин (см. [grpcsrv.Listener]).
	if spec.Metrics == nil {
		return nil, nil, fmt.Errorf("servicehost: %s не поднимается — реестра метрик нет, "+
			"а слушатель без измерителя задержки служил бы молча. Дескриптор с таким полем не "+
			"проходит конструктор (servicecontract.New, О13); сюда он попал в обход него",
			spec.Service)
	}
	lat, lerr := grpcsrv.NewServerLatency(spec.Metrics)
	if lerr != nil {
		return nil, nil, fmt.Errorf("servicehost: %s не поднимается — измеритель задержки не "+
			"заводится в переданном реестре: %w. Так выглядит несогласованное объявление серии "+
			"(то же имя с другой размерностью); поднять процесс значило бы отдать ему "+
			"диагностическую поверхность без семейства, которого на ней не будет никогда",
			spec.Service, lerr)
	}
	// Исход личности наблюдается ЗДЕСЬ, а не у каждого сервиса, и по той же
	// причине, по какой здесь стоит измеритель задержки: пока счётчик заводит
	// каждый у себя, «не завёл» НЕОТЛИЧИМО от «завёл такой же», а без него
	// «личность объявлена и не приехала» неотличимо от роста безымянных вызовов.
	arrival, aerr := grpcsrv.NewIdentityArrival(spec.Metrics)
	if aerr != nil {
		return nil, nil, fmt.Errorf("servicehost: %s не поднимается — счётчик исходов личности "+
			"не заводится в переданном реестре: %w. Так выглядит несогласованное объявление серии "+
			"(то же имя с другой размерностью); поднять процесс значило бы отдать ему полосу, "+
			"на которой рассинхрон написания ключей неотличим от законной безымянности",
			spec.Service, aerr)
	}
	build := func(creds credentials.TransportCredentials, on grpcsrv.Listener) *grpc.Server {
		return grpcsrv.NewServer(
			grpc.Creds(creds),
			grpc.ChainUnaryInterceptor(unaryChain(spec, slot, lat, arrival, on)...),
			grpc.ChainStreamInterceptor(streamChain(spec, slot, lat, arrival, on)...),
		)
	}
	return build(spec.PublicCreds, grpcsrv.ListenerPublic),
		build(spec.InternalCreds, grpcsrv.ListenerInternal), nil
}

// decisionLink строит звено решения о доступе — ОДНО на семь сервисов.
//
// До носителя каждый сервис держал свой пакет-обёртку над одним и тем же
// клиентом владельца модели: семь фабрик, расходившихся тем, какие поля они
// прокидывают, а какие оставляют умолчанию библиотеки. Здесь их одна, и
// величины приезжают полями дескриптора, а не берутся молча.
func decisionLink(spec servicecontract.Spec, m authz.RPCMap) (*authz.Interceptor, func(), error) {
	opts := authz.InterceptorOptions{
		ServiceName:  string(spec.Service),
		Map:          m,
		Cache:        authz.NewCache(spec.CacheWindow),
		Logger:       spec.Logger,
		CheckTimeout: spec.ClientBudget,
	}
	// Бюджет отказов. Ось несёт либо величину, либо названную причину, по которой
	// отсечка этому процессу не нужна; необъявленная ось до сюда не доезжает —
	// её отвергает конструктор дескриптора. Поэтому ноль здесь означает
	// объявленное «не применимо», а не забытое поле.
	if budget, ok := spec.DenyBudget.Get(); ok {
		opts.DenyRateLimitPerSec = budget
	}

	// Величины кеша уходят корню ДО того, как звено начнёт отвечать: приёмник
	// объявлен полем дескриптора и потому не может быть забыт (О12). Отдаётся
	// ЧИТАТЕЛЬ, а не кеш: наблюдающему нужны числа, а не право снять запись.
	observe := func(intr *authz.Interceptor) *authz.Interceptor {
		if spec.AuthzObserve != nil {
			spec.AuthzObserve(intr.Metrics)
		}
		return intr
	}

	switch spec.Authz {
	case servicecontract.AuthzSelf:
		// Владелец модели решает у себя: клиента приносит он сам, ребра к себе
		// не бывает. Порт непуст — это проверил конструктор дескриптора.
		opts.Client = withExistenceHiding(spec, m, spec.SelfCheck)
		return observe(authz.NewInterceptor(opts)), nil, nil

	case servicecontract.AuthzViaIAM:
		conn, err := grpc.NewClient(spec.CheckEdge.Addr(),
			grpc.WithTransportCredentials(spec.CheckEdge.Creds()),
			grpcclient.KeepaliveDialOption(true))
		if err != nil {
			return nil, nil, fmt.Errorf("servicehost: %s не поднимается — ребро решения о доступе "+
				"на %s не собирается: %w", spec.Service, spec.CheckEdge.Addr(), err)
		}
		// Перевод вопроса в контракт владельца приносит СЕРВИС
		// ([servicecontract.Spec.PeerCheck]); носитель набирает соседа и держит
		// соединение, но чужого контракта не знает. Непустоту сборщика проверил
		// конструктор дескриптора.
		opts.Client = withExistenceHiding(spec, m, spec.PeerCheck(conn))
		return observe(authz.NewInterceptor(opts)), func() { _ = conn.Close() }, nil

	default:
		// Недостижимо: конструктор дескриптора отвергает незаполненный источник.
		// Ветка остаётся отказом, а не пропуском: умолчание здесь было бы
		// решением о доступе, принятым никем.
		return nil, nil, fmt.Errorf("servicehost: %s не поднимается — источник решения о доступе "+
			"не назван", spec.Service)
	}
}

// withExistenceHiding надевает на решателя сверку существования, если сервис
// принёс порт.
//
// Порт судится осью скрытия ещё в конструкторе дескриптора: объявлено скрытие —
// порт обязателен, не объявлено — запрещён. Поэтому здесь достаточно спросить,
// принесён ли он: «принесён и не спрошен» было бы той самой проводкой, которую
// никто не дёргает, а «объявлено и не принесён» до этого места не доезжает.
//
// Набор пообъектных типов ВЫВОДИТСЯ из карты прав, а не выписывается: тип
// приезжает в карту своей аннотацией, и выписанный перечень был бы тем местом,
// которое забывают дополнить, — новый ресурс молча перестал бы скрывать
// существование.
func withExistenceHiding(spec servicecontract.Spec, m authz.RPCMap, inner authz.CheckClient) authz.CheckClient {
	if spec.Existence == nil {
		return inner
	}
	return &existenceAwareCheck{
		inner:  inner,
		probe:  spec.Existence,
		scoped: catalogderive.ObjectScopedTypes(m),
	}
}

// listenAndServe поднимает оба слушателя и гасит их по отмене ctx.
//
// Ошибка ВНУТРЕННЕГО слушателя учитывается в исходе процесса наравне с
// публичной: при его крахе отмена контекста гасит публичный, и тот по контракту
// grpc-go возвращает nil. Считай исход только по публичной ошибке — крах
// внутренней плоскости дал бы нулевой код возврата, оркестратор не перезапустил
// бы процесс, и весь админ-путь тихо стал бы недоступен.
func listenAndServe(ctx context.Context, spec servicecontract.Spec, publicSrv, internalSrv *grpc.Server) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	publicLis, err := net.Listen("tcp", spec.PublicAddr)
	if err != nil {
		return fmt.Errorf("servicehost: публичный слушатель %s: %w", spec.PublicAddr, err)
	}
	internalLis, err := net.Listen("tcp", spec.InternalAddr)
	if err != nil {
		_ = publicLis.Close()
		return fmt.Errorf("servicehost: внутренний слушатель %s: %w", spec.InternalAddr, err)
	}
	spec.Logger.Info("servicehost: listening",
		"service", string(spec.Service),
		"public", spec.PublicAddr,
		"internal", spec.InternalAddr)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		internalSrv.GracefulStop()
		publicSrv.GracefulStop()
	}()

	internalErr := make(chan error, 1)
	go func() {
		serr := internalSrv.Serve(internalLis)
		if serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			spec.Logger.Error("servicehost: internal listener stopped; tearing down process", "err", serr)
			cancel()
			internalErr <- serr
			return
		}
		internalErr <- nil
	}()

	publicErr := publicSrv.Serve(publicLis)
	cancel()
	<-stopped
	return serveResult(publicErr, <-internalErr)
}

// serveResult сводит исход процесса из ошибок ДВУХ слушателей.
//
// Ошибка публичного приоритетна — это первичный сигнал отказа краевой
// поверхности. Но ошибка внутреннего обязана дойти до исхода, когда публичной
// нет: при крахе внутреннего слушателя отмена контекста гасит публичный, и тот
// по контракту grpc-go возвращает nil. Считай исход только по публичной
// ошибке — крах админ-плоскости дал бы нулевой код возврата, оркестратор не
// перезапустил бы процесс, и недоступность осталась бы тихой.
func serveResult(publicErr, internalErr error) error {
	if publicErr = gracefulNil(publicErr); publicErr != nil {
		return publicErr
	}
	return gracefulNil(internalErr)
}

// gracefulNil обращает «сервер остановлен» в отсутствие ошибки.
//
// ПОЧЕМУ ЗДЕСЬ, А НЕ ТОЛЬКО У ВНУТРЕННЕГО СЛУШАТЕЛЯ. Внутренний фильтровал этот
// сентинел, публичный — нет, и асимметрия давала отказ на штатном гашении:
// остановка ПО НАШЕЙ ЖЕ просьбе (ctx отменён → GracefulStop) возвращалась
// вызывающему как ошибка старта. Наблюдалось пробой подъёма nlb, и только под
// полной параллелью прогона — то есть в окне, где отмена успевает опередить
// начало обслуживания. Флейк здесь был симптомом, а не природой: исход зависел
// от того, кто выиграл гонку, но неверным он был всегда.
//
// Отличать «нас попросили остановиться» от «мы упали» обязан носитель: у
// вызывающего для этого нет ничего, кроме текста ошибки.
func gracefulNil(err error) error {
	if err == nil || errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

// decisionSlot — место звена решения о доступе в цепочке.
//
// # Почему слот, а не готовое звено
//
// Звено спрашивает карту прав, карта выводится из СЛУЖИМОГО набора RPC, а
// служимое известно только после регистрации — то есть после того, как сервер
// уже собран со своей цепочкой. Слот разрывает этот круг, не заводя второго
// прохода регистрации.
//
// # Почему пустой слот отказывает, а не пропускает
//
// Состояние посадки разрешением не бывает. Слот пуст ровно между сборкой
// сервера и концом отказов старта, и в этом окне ни одно соединение не принято —
// [Serve] слушает позже. Но ветка обязана существовать и быть fail-closed: она
// есть единственное, что стоит между «порядок однажды переставили» и «процесс
// обслуживает запросы, не спрашивая ни о чьих правах». Производитель у неё
// есть и назван пробой `TestChainRefusesBeforeTheDecisionLinkIsInstalled`, а не
// оставлен умозрительным.
type decisionSlot struct {
	p atomic.Pointer[authz.Interceptor]
}

func (s *decisionSlot) install(i *authz.Interceptor) { s.p.Store(i) }

// errNoDecisionLink — отказ пустого слота. Текст называет предмет прямо: это
// рантайм-диагностика, которую читает оператор, а не публичный рассказ о том,
// где было открыто.
func errNoDecisionLink() error {
	return status.Error(codes.Unavailable,
		"authorization decision link is not installed yet; refusing the call")
}

func (s *decisionSlot) unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		i := s.p.Load()
		if i == nil {
			return nil, errNoDecisionLink()
		}
		return i.Unary()(ctx, req, info, h)
	}
}

func (s *decisionSlot) stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		i := s.p.Load()
		if i == nil {
			return errNoDecisionLink()
		}
		return i.Stream()(srv, ss, info, h)
	}
}

// servedOf снимает служимый набор У САМОГО СЕРВЕРА.
//
// Источник — `grpc.Server.GetServiceInfo`, а не записывающая обёртка над
// регистратором. Разница в том, что обёртка видит лишь то, что через неё
// прошло: регистрация в обход (в том числе та, что делает сам конструктор
// сервера — здоровье и рефлексия) осталась бы ей невидимой, и «служить RPC и не
// отдать его дескриптор» перестало бы быть одной операцией.
func servedOf(srv *grpc.Server) servedSet {
	var out servedSet
	for name, info := range srv.GetServiceInfo() {
		for _, m := range info.Methods {
			out.methods = append(out.methods, servicecontract.MethodFQN("/"+name+"/"+m.Name))
		}
	}
	sort.Slice(out.methods, func(i, j int) bool { return out.methods[i] < out.methods[j] })
	return out
}

// mergeServed сводит наборы обоих слушателей. Оба судятся ОДНИМИ отказами:
// «internal = доверенный» — запрещённое допущение, и освобождать внутренний
// слушатель от проверки объявленного было бы ровно им.
func mergeServed(sets ...servedSet) servedSet {
	seen := map[servicecontract.MethodFQN]struct{}{}
	var out servedSet
	for _, s := range sets {
		for _, m := range s.methods {
			if _, dup := seen[m]; dup {
				continue
			}
			seen[m] = struct{}{}
			out.methods = append(out.methods, m)
		}
	}
	sort.Slice(out.methods, func(i, j int) bool { return out.methods[i] < out.methods[j] })
	return out
}

// domainsOf выводит proto-пакеты из служимого набора (К-2: домен ВЫВОДИТСЯ).
//
// Платформенные регистрации изымаются поимённо: домена Kachō у них нет, и
// спрашивать про них аннотации не у чего.
func domainsOf(served servedSet) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, m := range served.methods {
		if _, exempt := platformServices[serviceOf(m)]; exempt {
			continue
		}
		dom, err := domainOf(m)
		if err != nil {
			return nil, fmt.Errorf("servicehost: домен не выводится из имени служимого метода: %w", err)
		}
		if _, dup := seen[dom]; dup {
			continue
		}
		seen[dom] = struct{}{}
		out = append(out, dom)
	}
	if len(out) == 0 {
		return nil, errors.New("servicehost: из служимого набора не вывелось ни одного домена. " +
			"Ноль целей есть ОТКАЗ, а не успех: процесс без домена неотличим от исправного")
	}
	sort.Strings(out)
	return out, nil
}

// catalogOf собирает строки каталога из АННОТАЦИЙ дескрипторов, слинкованных в
// бинарь, — из того же источника, из которого генерируется каталог края.
// Второго объявления не существует, поэтому расходиться нечему.
func catalogOf(domains []string) catalogView {
	out := catalogView{rows: map[servicecontract.MethodFQN]catalogRow{}}
	catalogderive.RangeAnnotated(domains, func(fullMethod string, md protoreflect.MethodDescriptor, a catalogderive.Annotations) {
		m := servicecontract.MethodFQN(fullMethod)
		dom, err := domainOf(m)
		if err != nil {
			// Недостижимо: имя собрано самим обходом из дескриптора. Строку всё
			// равно не пропускаем молча — иначе метод исчез бы из каталога и О2
			// доложил бы о нём как об отсутствующем, послав чинить не туда.
			dom = ""
		}
		out.rows[m] = catalogRow{
			Method:        m,
			Domain:        dom,
			ScopeFiltered: a.ScopeFiltered,
			HideExistence: a.HideExistence,
			ObjectType:    servicecontract.ObjectType(a.ScopeObjectType),
			// Признак снимается с дескриптора метода — с того же источника, из
			// которого приезжают аннотации. Выводить его из имени («Watch»,
			// «Subscribe») значило бы гадать: имя подписки нам никто не обещал.
			ServerStreaming: md.IsStreamingServer(),
		}
	})
	return out
}

// unaryChain / streamChain — ПОРЯДОК ЗВЕНЬЕВ, один на все сервисы.
//
// Позиции и причина на каждую:
//
//  1. измеритель задержки — САМОЕ внешнее звено. Он обязан накрывать всё, что
//     процесс делает ради вызова, включая отказ, произведённый ЛЮБЫМ звеном
//     ниже. Стоя внутри решения о доступе, он оставил бы неизмеренным каждый
//     отказ по правам — то есть ровно тот исход, ради которого в разбор
//     происшествия и приходят; стоя внутри восстановления паники — ещё и каждую
//     панику. Полоса слушателя приходит параметром: один и тот же метод служится
//     обоими, и слитый ряд был бы средним двух разных величин;
//  2. журнал доступа: он обязан видеть исход любого вызова,
//     включая паниковавший. Стоя внутри восстановления паники, он пропускал бы
//     ровно тот исход, который тяжелее всех, — раскрутка стека проходит мимо
//     записи, и самый серьёзный отказ остаётся единственным ненаблюдаемым;
//  3. восстановление после паники: ловит панику всего нижележащего, включая
//     сами звенья. Иначе одно разыменование nil на пути запроса ОДНОГО тенанта
//     прекращает обслуживание ВСЕХ. Журнал этажом выше видит его обычный
//     возврат `codes.Internal` и записывает как исход;
//  4. срок — ВНУТРИ восстановления и НАД всем остальным: он обязан накрывать и
//     вопрос о доступе, и запросы к своей БД, иначе вызов без срока держит
//     соединение из ограниченного пула сколько угодно. Величина у полос РАЗНАЯ:
//     unary берёт границу обработки запроса, стрим — срок жизни подписки, и на
//     стриме этой позиции может не быть вовсе (см. [streamChain]);
//  5. загрузочный гейт мутаций: отвергает создание, пока путь доставки
//     намерений регистрации не поднят. Стоит ДО выяснения личности намеренно —
//     это отказ по состоянию ПРОЦЕССА, он не зависит от того, кто спрашивает, и
//     платить за него работой по извлечению личности незачем;
//  6. личность пира по сертификату — чей это пир;
//  7. переданная личность, сужённая кругом — вправе ли пир говорить за другого.
//     Порядок 6→7 обязателен: решение о доверии по ещё не извлечённой личности
//     решением не является;
//  8. решение о доступе — читает уже извлечённого субъекта.
//
// # Чего в этой цепочке ПОКА НЕТ, и почему это названо, а не умолчано
//
// Не хватает слота ограничителя одновременности; позиция метрик, стоявшая здесь
// же, ЗАПОЛНЕНА — её занял измеритель задержки (позиция 1 выше). Причина, по
// которой ограничителя всё ещё нет, не «руки не дошли»: ограничитель
// требует ИЗМЕРЕННОЙ ёмкости модели прав — потолок, выведенный неверно,
// превращает нагрузку в отказы на ПОЛОЖИТЕЛЬНОМ пути, то есть чинит одно и
// ломает другое, причём молча.
//
// Позиция при этом зафиксирована здесь, а не оставлена на усмотрение: когда
// звено введут, оно встанет на своё место, а не туда, где окажется удобно.
func unaryChain(spec servicecontract.Spec, slot *decisionSlot,
	lat *grpcsrv.ServerLatency, arrival *grpcsrv.IdentityArrival,
	on grpcsrv.Listener) []grpc.UnaryServerInterceptor {
	chain := []grpc.UnaryServerInterceptor{
		lat.UnaryServerInterceptor(on),
		accessLogUnary(spec.Logger),
		grpcsrv.UnaryPanicRecovery(spec.Logger),
		handlingBudgetUnary(spec.HandlingBudget),
	}
	if gate, ok := spec.BootGate.Get(); ok && gate != nil {
		chain = append(chain, bootGateUnary(gate))
	}
	chain = append(chain, grpcsrv.PrincipalExtractUnary(carriedTrustDomain(spec), carriedForwarders(spec),
		grpcsrv.WithIdentityArrival(arrival))...)
	return append(chain, slot.unary())
}

// streamChain повторяет порядок unary-цепочки с двумя названными отличиями.
//
// Первое: на третьей позиции стоит НЕ граница обработки запроса, а срок жизни
// подписки — своя величина ([servicecontract.Spec.StreamBudget]). Взять сюда
// унарную значило бы рвать подписку по потолку одиночного вызова.
//
// Второе: этой позиции может не быть вовсе — когда ось объявлена неприменимой
// («серверных стримов не служу»). Пустая клетка при этом молчаливой не
// остаётся: необъявленную ось отвергает конструктор дескриптора, а заявление о
// неприменимости судит О11 по служимому набору.
//
// Загрузочного гейта мутаций здесь нет и в unary-варианте он условен по другой
// причине: мутация в этом продукте всегда unary (см. [bootGateUnary]).
func streamChain(spec servicecontract.Spec, slot *decisionSlot,
	lat *grpcsrv.ServerLatency, arrival *grpcsrv.IdentityArrival,
	on grpcsrv.Listener) []grpc.StreamServerInterceptor {
	chain := []grpc.StreamServerInterceptor{
		lat.StreamServerInterceptor(on),
		accessLogStream(spec.Logger),
		grpcsrv.StreamPanicRecovery(spec.Logger),
	}
	if budget, ok := spec.StreamBudget.Get(); ok {
		chain = append(chain, streamBudgetLink(budget))
	}
	chain = append(chain, grpcsrv.PrincipalExtractStream(carriedTrustDomain(spec), carriedForwarders(spec),
		grpcsrv.WithIdentityArrival(arrival))...)
	return append(chain, slot.stream())
}

// carriedForwarders — круг отправителей контура, поднятого носителем.
//
// Ось здесь ВСЕГДА несёт значение, и это гарантия конструктора, а не удача:
// изъятие («принимать переданную личность некому») он отвергает у всякого, чей
// контур поднимает носитель, — именно потому, что пару звеньев извлечения
// личности носитель ставит безусловно. Нулевой круг, доставшийся отсюда, означал
// бы «доверяем любому проверенному пиру», поэтому молчаливого пути к нему нет:
// до `Serve` доходит только принятый дескриптор.
func carriedForwarders(spec servicecontract.Spec) grpcsrv.TrustedForwarders {
	circle, _ := spec.Forwarders.Get()
	return circle
}

// carriedTrustDomain — домен доверия контура, поднятого носителем.
//
// Ось здесь ВСЕГДА несёт значение по тому же основанию, что и круг отправителей:
// изъятие («личность сертификата я не разбираю») конструктор дескриптора
// отвергает у всякого, чей контур поднимает носитель, — именно потому, что пару
// звеньев извлечения носитель ставит безусловно. Необъявленный домен, доставшийся
// отсюда, означал бы процесс, не признающий своим ни одного предъявителя, поэтому
// молчаливого пути к нему нет: до `Serve` доходит только принятый дескриптор.
func carriedTrustDomain(spec servicecontract.Spec) grpcsrv.TrustDomain {
	domain, _ := spec.TrustDomain.Get()
	return domain
}
