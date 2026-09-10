// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

// admission_unknown_service_test.go — то же свойство на поверхности, у которой
// ДЕСКРИПТОРА НЕТ.
//
// # Предмет
//
// Соседние случаи доказывают обёртку регистратора: она видит дескриптор и потому
// покрывает всё, что через неё зарегистрировано. У края такого дескриптора нет:
// чужие методы он пересылает обработчиком неизвестной службы, и обёртка там
// покрыла бы только собственную поверхность края, промолчав на всём
// проксируемом. Здесь проверяется вторая форма — потоковое звено, — и проверяется
// ровно то, что делает её пригодной:
//
//	покрыт метод, которого сервер НЕ ЗНАЕТ (иначе форма бесполезна);
//	классификатор видит НАСТОЯЩЕЕ имя метода, а не имя обработчика
//	  (иначе чтения покупали бы бюджет мутаций и наоборот);
//	ключ берётся у звена, стоящего РАНЬШЕ (иначе ограничитель ключуется до
//	  решения о личности и снимается подстановкой заголовка);
//	слот одновременности возвращается по выходу обработчика (иначе предел
//	  превращается в счётчик прожитых запросов).

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/corelib/operations"
)

const (
	// Методы, которых на сервере НЕТ ни в одном дескрипторе: их обслуживает
	// обработчик неизвестной службы — ровно как край обслуживает чужие домены.
	unknownReadMethod  = "/kacho.cloud.vpc.v1.NetworkService/Get"
	unknownWriteMethod = "/kacho.cloud.vpc.v1.NetworkService/Create"
)

// principalStreamLink — потоковое звено, ставящее личность из метаданных, как
// это делает цепочка края. Оно и есть «раньше»: без него ключ пуст, и весь поток
// лёг бы в одно безымянное ведро.
func principalStreamLink() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(MDKeyPrincipalID); len(v) > 0 && v[0] != "" {
				ctx = operations.WithPrincipal(ctx, operations.Principal{Type: "user", ID: v[0]})
			}
		}
		return handler(srv, ctxStream{ServerStream: ss, ctx: ctx})
	}
}

// ctxStream — поток с подменённым контекстом. Ровно то, что делает звено
// личности у края: иначе поставленная им личность до следующего звена не
// доезжает.
type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ctxStream) Context() context.Context { return s.ctx }

// serveUnknown поднимает сервер БЕЗ единой зарегистрированной службы: всё
// уезжает в обработчик неизвестной службы, за которым стоит звено допуска.
//
// gate/entered дают ПОРЯДОК, а не ожидание по времени: обработчик объявляет о
// входе и только потом блокируется, поэтому вердикт не зависит от того, сколько
// заняла чужая работа.
func serveUnknown(t *testing.T, a *Admission, entered chan<- struct{}, gate chan struct{}) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	unknown := func(_ any, ss grpc.ServerStream) error {
		if gate != nil {
			if entered != nil {
				// Неблокирующе: после снятия задвижки обработчик отрабатывает
				// снова, а читателя объявлений к тому моменту уже нет. Блокировка
				// здесь дала бы зависание, которое читалось бы как невозвращённый
				// слот — то есть проба обвинила бы предмет в своей собственной
				// фикстуре.
				select {
				case entered <- struct{}{}:
				default:
				}
			}
			<-gate
		}
		if err := ss.RecvMsg(new(wrapperspb.BytesValue)); err != nil && err != io.EOF {
			return err
		}
		return ss.SendMsg(wrapperspb.Bytes(nil))
	}
	srv := grpc.NewServer(
		grpc.UnknownServiceHandler(unknown),
		// Порядок несущий: личность ставится РАНЬШЕ допуска.
		grpc.ChainStreamInterceptor(principalStreamLink(), a.StreamInterceptor()),
	)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// proxyCall — вызов метода, которого сервер не знает: клиент шлёт его как
// обычный унарный, сервер обслуживает как поток.
func proxyCall(conn *grpc.ClientConn, principal, method string) error {
	ctx, cancel := context.WithTimeout(
		metadata.AppendToOutgoingContext(context.Background(), MDKeyPrincipalID, principal),
		20*time.Second)
	defer cancel()
	return conn.Invoke(ctx, method, wrapperspb.Bytes(nil), new(wrapperspb.BytesValue))
}

// TestUnknownServiceTrafficIsAdmittedAndRefusedLikeAnyOther — отказ и
// положительный контроль в ОДНОМ случае, чтобы их нельзя было развести.
func TestUnknownServiceTrafficIsAdmittedAndRefusedLikeAnyOther(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", wireLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)
	conn := serveUnknown(t, a, nil, nil)

	// Всплеск чтений = 1 × 2 = 2.
	require.NoError(t, proxyCall(conn, "usr-1", unknownReadMethod))
	require.NoError(t, proxyCall(conn, "usr-1", unknownReadMethod))

	err = proxyCall(conn, "usr-1", unknownReadMethod)
	require.Error(t, err, "проксируемое чтение сверх всплеска обязано быть отвергнуто: "+
		"без этого потолок края покрывает только его собственную поверхность")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, MsgReadRateExceeded, status.Convert(err).Message(),
		"текст отказа — часть контракта, и он обязан назвать ИМЕННО исчерпанную ось")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ на том же соединении: другой арендатор проходит.
	// Без него отказ выше был бы неотличим от «сервер сломался после двух вызовов»,
	// а ключ по личности — от общего на процесс ведра.
	require.NoError(t, proxyCall(conn, "usr-2", unknownReadMethod),
		"ведро обязано быть НА АРЕНДАТОРА: сосед не платит за чужой поток")
}

// TestUnknownServiceMethodNameReachesTheClassifier — классификатор видит
// НАСТОЯЩЕЕ имя метода, а не имя обработчика неизвестной службы.
//
// Если бы в звено доезжало имя обработчика, все проксируемые вызовы получили бы
// ОДИН класс — и, по полярности классификатора, класс мутации. Тогда чтения
// края тратили бы впятеро более узкий бюджет, а различить это можно было бы
// только по тексту отказа, который никто не читает.
func TestUnknownServiceMethodNameReachesTheClassifier(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", wireLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)
	conn := serveUnknown(t, a, nil, nil)

	// Оси РАЗНЫЕ: два чтения выбирают читательский всплеск целиком, и мутация
	// после них всё равно проходит — её ведро не тронуто.
	require.NoError(t, proxyCall(conn, "usr-x", unknownReadMethod))
	require.NoError(t, proxyCall(conn, "usr-x", unknownReadMethod))
	require.NoError(t, proxyCall(conn, "usr-x", unknownWriteMethod),
		"мутация обязана иметь СВОЁ ведро: одна ось на оба класса означала бы, что "+
			"страница чтения выедает бюджет создания")
	require.NoError(t, proxyCall(conn, "usr-x", unknownWriteMethod),
		"всплеск мутаций тот же (1 × 2), и вторая обязана пройти")

	err = proxyCall(conn, "usr-x", unknownWriteMethod)
	require.Error(t, err)
	require.Equal(t, MsgMutationRateExceeded, status.Convert(err).Message(),
		"отвергнута обязана быть ось МУТАЦИЙ: другой текст означает, что до звена "+
			"доехало имя обработчика, а не имя метода")
}

// TestUnknownServiceKeyComesFromTheLinkBeforeIt — МЕСТО звена.
//
// Тот же вход, но без звена личности: ключ пуст, весь поток ложится в одно
// безымянное ведро, и второй арендатор платит за первого. Это и есть цена
// ограничителя, ключующегося раньше решения о личности, — измеренная, а не
// объявленная.
func TestUnknownServiceKeyComesFromTheLinkBeforeIt(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", wireLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	lis, lerr := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, lerr)
	unknown := func(_ any, ss grpc.ServerStream) error {
		if rerr := ss.RecvMsg(new(wrapperspb.BytesValue)); rerr != nil && rerr != io.EOF {
			return rerr
		}
		return ss.SendMsg(wrapperspb.Bytes(nil))
	}
	// Звена личности НЕТ — допуск остался первым.
	srv := grpc.NewServer(
		grpc.UnknownServiceHandler(unknown),
		grpc.ChainStreamInterceptor(a.StreamInterceptor()),
	)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, cerr := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, cerr)
	t.Cleanup(func() { _ = conn.Close() })

	require.NoError(t, proxyCall(conn, "usr-a", unknownReadMethod))
	require.NoError(t, proxyCall(conn, "usr-b", unknownReadMethod))
	err = proxyCall(conn, "usr-c", unknownReadMethod)
	require.Error(t, err, "без звена личности ключ пуст: три РАЗНЫХ арендатора делят одно ведро")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, 1, a.Stats().Subjects,
		"ведро обязано быть ровно одно — безымянное; иначе опыт не показывает того, "+
			"ради чего звено допуска стоит ПОСЛЕ звена личности")
}

// TestUnknownServiceReleasesTheSlotWhenTheHandlerReturns — слот одновременности
// держится ровно пока работает обработчик и возвращается по его выходу.
//
// Без освобождения предел превращается в счётчик прожитых запросов: арендатор
// упирается в него навсегда, и это выглядит как «предел слишком мал», а не как
// утечка.
func TestUnknownServiceReleasesTheSlotWhenTheHandlerReturns(t *testing.T) {
	clock := newFrozenClock()
	// Одновременность — 1, темпа заведомо хватает: предмет случая именно слот.
	limits := AdmissionLimits{ReadPerSec: 100, MutationPerSec: 100, BurstFactor: 5, InFlight: 1}
	a, err := NewAdmission("public", limits, PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	entered := make(chan struct{})
	gate := make(chan struct{})
	conn := serveUnknown(t, a, entered, gate)

	first := make(chan error, 1)
	go func() { first <- proxyCall(conn, "usr-slot", unknownReadMethod) }()
	<-entered // слот занят, и об этом объявлено ДО блокировки

	err = proxyCall(conn, "usr-slot", unknownReadMethod)
	require.Error(t, err, "второй одновременный вызов обязан упереться в предел одновременности")
	require.Equal(t, MsgInFlightExceeded, status.Convert(err).Message())

	close(gate)
	require.NoError(t, <-first)

	// Слот вернулся: тот же арендатор проходит снова.
	require.NoError(t, proxyCall(conn, "usr-slot", unknownReadMethod),
		"слот обязан вернуться по выходу обработчика")
	st := a.Stats()
	require.EqualValues(t, 2, st.Admitted,
		"допущено ровно два: удержавший слот и прошедший после его возврата")
	require.EqualValues(t, 1, st.RejectedInFlight,
		"отвергнут ровно один, и отвергнут ПО ОДНОВРЕМЕННОСТИ, а не по темпу: "+
			"иначе случай доказывал бы не то, что в его заголовке")
	require.EqualValues(t, 0, st.RejectedRate)
}
