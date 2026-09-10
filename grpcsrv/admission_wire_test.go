// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

// admission_wire_test.go — то же свойство, но на ПРОВОДЕ.
//
// Соседний файл утверждает решение допуска, вызывая его напрямую. Этого мало
// ровно в одном: он ничего не говорит о МЕСТЕ проверки. Обёртка дескриптора
// подставляет допуск между цепочкой звеньев и обработчиком, и именно это здесь
// доказывается — тем, что ключ виден только ПОСЛЕ звена, устанавливающего
// личность, и что незакрытый вызов держит слот одновременности, пока обработчик
// не вернулся.
//
// Плюс два свойства покрытия, которые иначе остаются на честном слове: обёрнуты
// ВСЕ методы дескриптора (включая потоковые) и НЕ обёрнуты служебные поверхности
// носителя (проверка здоровья), потому что отказ проверке готовности означал бы
// перезапуск пода из-за нагрузки на API.

import (
	"context"
	"io"
	"net"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/corelib/operations"
)

const (
	wireService    = "kacho.grpcsrv.probe.Twoface"
	wireListMethod = "/" + wireService + "/ListThings"
	wireMakeMethod = "/" + wireService + "/MakeThing"
	wireStream     = "/" + wireService + "/GetStream"
)

// twofaceDesc — служба с чтением, мутацией и потоком: три формы, которые обёртка
// обязана покрыть, и по одному представителю каждой.
//
// `entered` и `gate` вместе дают ПОРЯДОК, а не ожидание по времени: обработчик
// объявляет о своём входе и только потом блокируется. Случай, опрашивающий
// счётчик в цикле, утверждал бы то же самое, но его вердикт зависел бы от того,
// сколько заняла чужая работа, — а недетерминизм входа однажды прочтут как
// свойство предмета.
func twofaceDesc(entered chan<- struct{}, gate chan struct{}) *grpc.ServiceDesc {
	unary := func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor, full string) (any, error) {
		in := new(wrapperspb.BytesValue)
		if err := dec(in); err != nil {
			return nil, err
		}
		call := func(ctx context.Context, _ any) (any, error) {
			if gate != nil {
				if entered != nil {
					entered <- struct{}{} // слот занят — об этом объявлено ДО блокировки
				}
				<-gate // держим слот одновременности, пока случай не отпустит
			}
			return wrapperspb.Bytes(nil), nil
		}
		if interceptor == nil {
			return call(ctx, in)
		}
		return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: full}, call)
	}
	return &grpc.ServiceDesc{
		ServiceName: wireService,
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "ListThings", Handler: func(srv any, ctx context.Context, dec func(any) error, i grpc.UnaryServerInterceptor) (any, error) {
				return unary(srv, ctx, dec, i, wireListMethod)
			}},
			{MethodName: "MakeThing", Handler: func(srv any, ctx context.Context, dec func(any) error, i grpc.UnaryServerInterceptor) (any, error) {
				return unary(srv, ctx, dec, i, wireMakeMethod)
			}},
		},
		Streams: []grpc.StreamDesc{{
			StreamName: "GetStream",
			Handler: func(_ any, ss grpc.ServerStream) error {
				return ss.SendMsg(wrapperspb.Bytes(nil))
			},
			ServerStreams: true,
		}},
		Metadata: "kacho/grpcsrv/probe.proto",
	}
}

// principalHeaderInterceptor — звено, ставящее личность из метаданных, как это
// делает цепочка носителя. Нужно, чтобы случай утверждал МЕСТО проверки: ключ
// виден допуску только потому, что звено отработало раньше него.
func principalHeaderInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(MDKeyPrincipalID); len(v) > 0 && v[0] != "" {
				ctx = operations.WithPrincipal(ctx, operations.Principal{Type: "user", ID: v[0]})
			}
		}
		return handler(ctx, req)
	}
}

// serveGuarded поднимает сервер, чьи службы зарегистрированы ЧЕРЕЗ обёртку.
func serveGuarded(t *testing.T, a *Admission, entered chan<- struct{}, gate chan struct{}) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := NewServer(grpc.ChainUnaryInterceptor(principalHeaderInterceptor()))
	a.Registrar(srv).RegisterService(twofaceDesc(entered, gate), nil)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func callAs(conn *grpc.ClientConn, principal, method string) error {
	ctx, cancel := context.WithTimeout(
		metadata.AppendToOutgoingContext(context.Background(), MDKeyPrincipalID, principal),
		20*time.Second)
	defer cancel()
	return conn.Invoke(ctx, method, wrapperspb.Bytes(nil), new(wrapperspb.BytesValue))
}

// wireLimits — крошечный бюджет: предмет — исход на проводе, не числа посадки.
func wireLimits() AdmissionLimits {
	return AdmissionLimits{ReadPerSec: 1, MutationPerSec: 1, BurstFactor: 2, InFlight: 2}
}

// TestWireRefusesOverTheRateAndAdmitsUnderIt — пара «отказ + положительный
// контроль» на проводе, в одном случае, чтобы их нельзя было развести.
func TestWireRefusesOverTheRateAndAdmitsUnderIt(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", wireLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)
	conn := serveGuarded(t, a, nil, nil)

	// Всплеск чтений = 1 × 2 = 2.
	require.NoError(t, callAs(conn, "usr-1", wireListMethod))
	require.NoError(t, callAs(conn, "usr-1", wireListMethod))

	err = callAs(conn, "usr-1", wireListMethod)
	require.Error(t, err, "третье чтение сверх всплеска обязано быть отвергнуто")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, MsgReadRateExceeded, status.Convert(err).Message())

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ на том же соединении: другой арендатор проходит.
	// Без него отказ выше был бы неотличим от «сервер сломался после двух вызовов».
	require.NoError(t, callAs(conn, "usr-2", wireListMethod))
}

// TestWireCoversEveryMethodOfTheDescriptor — обёрнут КАЖДЫЙ метод, а не первый.
//
// Мутация и поток проверяются отдельно: обёртка, забывшая раздел `Streams`,
// оставила бы подписку без предела — а подписка держит слот всё время жизни.
func TestWireCoversEveryMethodOfTheDescriptor(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", wireLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)
	conn := serveGuarded(t, a, nil, nil)

	require.NoError(t, callAs(conn, "usr-m", wireMakeMethod))
	require.NoError(t, callAs(conn, "usr-m", wireMakeMethod))
	err = callAs(conn, "usr-m", wireMakeMethod)
	require.Error(t, err, "мутация обязана быть покрыта обёрткой")
	require.Equal(t, MsgMutationRateExceeded, status.Convert(err).Message())

	// Поток. Классификатор относит `GetStream` к чтениям (префикс `Get`), поэтому
	// бюджет здесь читательский.
	openStream := func(principal string) error {
		ctx, cancel := context.WithTimeout(
			metadata.AppendToOutgoingContext(context.Background(), MDKeyPrincipalID, principal),
			20*time.Second)
		defer cancel()
		cs, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, wireStream)
		if err != nil {
			return err
		}
		if err := cs.SendMsg(wrapperspb.Bytes(nil)); err != nil {
			return err
		}
		if err := cs.CloseSend(); err != nil {
			return err
		}
		for {
			if err := cs.RecvMsg(new(wrapperspb.BytesValue)); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
	}
	require.NoError(t, openStream("usr-s"))
	require.NoError(t, openStream("usr-s"))
	err = openStream("usr-s")
	require.Error(t, err, "поток обязан быть покрыт обёрткой: подписка держит слот всё время жизни")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

// TestWireLeavesTheCarrierSurfacesAlone — служебные поверхности носителя под
// предел НЕ попадают.
//
// Это не послабление, а требование: отказ проверке готовности означал бы
// перезапуск пода из-за нагрузки на API, то есть ограничитель сам стал бы
// причиной отказа обслуживания.
func TestWireLeavesTheCarrierSurfacesAlone(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", wireLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)
	conn := serveGuarded(t, a, nil, nil)

	hc := healthpb.NewHealthClient(conn)
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := hc.Check(ctx, &healthpb.HealthCheckRequest{})
		cancel()
		require.NoError(t, err, "проверка здоровья не должна упираться в предел арендатора")
		require.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.Status)
	}
	require.Zero(t, a.Stats().Admitted, "служебная поверхность не должна расходовать бюджет арендатора")
}

// TestWireHoldsTheSlotForTheDurationOfTheHandler — слот одновременности держится
// ровно пока работает обработчик.
//
// Здесь и проверяется МЕСТО проверки: допуск стоит вокруг обработчика, а не
// перед цепочкой, поэтому «в полёте» означает «обработчик ещё не вернулся», а не
// «запрос дошёл до сервера».
func TestWireHoldsTheSlotForTheDurationOfTheHandler(t *testing.T) {
	clock := newFrozenClock()
	// Темп заведомо не исчерпан (всплеск 20), предел одновременности — 2.
	limits := AdmissionLimits{ReadPerSec: 10, MutationPerSec: 10, BurstFactor: 2, InFlight: 2}
	a, err := NewAdmission("public", limits, PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	gate := make(chan struct{})
	entered := make(chan struct{}, 2)
	conn := serveGuarded(t, a, entered, gate)

	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- callAs(conn, "usr-hold", wireListMethod) }()
	}
	// Порядок, а не ожидание по времени: оба обработчика объявили о входе, значит
	// оба слота заняты — и это верно независимо от того, сколько заняла чужая
	// работа.
	<-entered
	<-entered

	err = callAs(conn, "usr-hold", wireListMethod)
	require.Error(t, err, "третий одновременный вызов обязан быть отвергнут")
	require.Equal(t, MsgInFlightExceeded, status.Convert(err).Message())

	close(gate)
	require.NoError(t, <-done)
	require.NoError(t, <-done)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: слоты освободились вместе с обработчиками.
	require.NoError(t, callAs(conn, "usr-hold", wireListMethod),
		"освобождённые слоты обязаны снова допускать")
}

// TestWireKeepsTheServedSetIdentical — обёртка НЕ меняет служимый набор.
//
// Свойство несущее и легко теряемое: носитель контура снимает служимый набор с
// самого сервера (`GetServiceInfo`) и выводит из него карту прав и отказы старта.
// Обёртка, забывшая перенести раздел потоков или переписавшая имя метода, тихо
// сократила бы этот набор — и все отказы старта, и аудит каталога прав стали бы
// судить о МЕНЬШЕМ числе RPC, оставаясь зелёными.
func TestWireKeepsTheServedSetIdentical(t *testing.T) {
	a, err := NewAdmission("public", wireLimits(), PrincipalSubject)
	require.NoError(t, err)

	plain := NewServer()
	plain.RegisterService(twofaceDesc(nil, nil), nil)

	guarded := NewServer()
	a.Registrar(guarded).RegisterService(twofaceDesc(nil, nil), nil)

	want := plain.GetServiceInfo()
	got := guarded.GetServiceInfo()
	require.Equal(t, len(want), len(got), "число служб обязано совпадать")

	names := func(info grpc.ServiceInfo) []string {
		out := make([]string, 0, len(info.Methods))
		for _, m := range info.Methods {
			out = append(out, m.Name)
		}
		sort.Strings(out)
		return out
	}
	for svc, info := range want {
		g, ok := got[svc]
		require.True(t, ok, "служба %s пропала после обёртки", svc)
		require.Equal(t, names(info), names(g),
			"состав методов службы %s изменился после обёртки", svc)
	}
	require.Contains(t, names(got[wireService]), "GetStream",
		"потоковый метод обязан остаться в служимом наборе: раздел потоков теряется молча")
}
