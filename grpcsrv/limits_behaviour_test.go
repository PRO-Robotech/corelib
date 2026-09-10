// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

// limits_behaviour_test.go — пределы сервера утверждаются на ПРОВОДЕ, а не на
// списке опций.
//
// «Опция передана» и «сервер её исполняет» — разные утверждения, и расходятся они
// молча: `grpc.MaxConcurrentStreams(0)` библиотека молча превращает обратно в
// `math.MaxUint32`, а предел, равный `MaxUint32`, вдобавок НЕ объявляется клиенту
// вовсе — соединение выглядит настроенным, а ограничения нет. Поэтому здесь
// поднимается настоящий сервер, и утверждения делаются о том, что видит
// вызывающий: о кадре настроек и о коде отказа.
//
// Умолчания библиотеки на собираемой версии (`google.golang.org/grpc v1.83.2`,
// прочитаны в исходнике, а не по памяти):
//
//	maxConcurrentStreams  = math.MaxUint32 (server.go:189) + кадр настроек НЕ
//	                        посылается, когда значение равно MaxUint32
//	                        (internal/transport/http2_server.go:186)
//	maxSendMessageSize    = math.MaxInt32  (server.go:62)  — не ограничен
//	maxReceiveMessageSize = 4 МиБ          (server.go:61)  — ограничен

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// echoFullMethod — координата пробного RPC. Служба собирается литералом
// дескриптора, а не генерируется: предмет пробы — предел РАЗМЕРА сообщения, и
// поле произвольных байт (`google.protobuf.BytesValue`) выражает его точнее
// любого доменного сообщения.
const echoFullMethod = "/kacho.grpcsrv.probe.Echo/Echo"

// echoDesc — служба «верни столько байт, сколько попросили».
//
// Запрос несёт полезную нагрузку (её размер проверяет предел приёма), ответ —
// нагрузку запрошенной длины (её размер проверяет предел отправки). Обе стороны
// одного дескриптора, поэтому один и тот же сервер проверяется обоими случаями.
func echoDesc(respBytes func(req *wrapperspb.BytesValue) int) *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "kacho.grpcsrv.probe.Echo",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Echo",
			Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				in := new(wrapperspb.BytesValue)
				if err := dec(in); err != nil {
					return nil, err
				}
				call := func(ctx context.Context, req any) (any, error) {
					return wrapperspb.Bytes(make([]byte, respBytes(req.(*wrapperspb.BytesValue)))), nil
				}
				if interceptor == nil {
					return call(ctx, in)
				}
				return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: echoFullMethod}, call)
			},
		}},
		Metadata: "kacho/grpcsrv/probe.proto",
	}
}

// serveProbe поднимает сервер с пробной службой и отдаёт готовое соединение.
func serveProbe(t *testing.T, respBytes func(*wrapperspb.BytesValue) int, opts ...grpc.ServerOption) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := NewServer(opts...)
	srv.RegisterService(echoDesc(respBytes), nil)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Клиентские пределы намеренно распахнуты: предметом пробы обязан быть
		// СЕРВЕР. С умолчанием клиента (4 МиБ на приём) отказ на крупном ответе
		// пришёл бы от самого клиента, и проба зеленела бы при снятом серверном
		// пределе.
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(64<<20),
			grpc.MaxCallSendMsgSize(64<<20),
		))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// echo зовёт пробный RPC с нагрузкой указанного размера.
func echo(t *testing.T, conn *grpc.ClientConn, reqBytes int) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return conn.Invoke(ctx, echoFullMethod,
		wrapperspb.Bytes(make([]byte, reqBytes)), new(wrapperspb.BytesValue))
}

// TestServerAdvertisesItsConcurrentStreamLimit — сервер ОБЪЯВЛЯЕТ предел
// одновременных вызовов в кадре настроек соединения.
//
// Это и есть несущее наблюдаемое: неограниченный предел библиотека не объявляет
// вовсе, поэтому «настройка отсутствует» и «настройка равна бесконечности» на
// проводе неотличимы — клиент в обоих случаях считает, что открывать можно
// сколько угодно. Одно соединение при этом занимает процесс, обслуживающий всех.
func TestServerAdvertisesItsConcurrentStreamLimit(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := NewServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	raw, err := net.Dial("tcp", lis.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	require.NoError(t, raw.SetDeadline(time.Now().Add(20*time.Second)))

	_, err = raw.Write([]byte(http2.ClientPreface))
	require.NoError(t, err)

	fr := http2.NewFramer(raw, raw)
	var settings *http2.SettingsFrame
	for i := 0; i < 8 && settings == nil; i++ {
		f, ferr := fr.ReadFrame()
		require.NoError(t, ferr, "сервер обязан прислать преамбулу соединения")
		if sf, ok := f.(*http2.SettingsFrame); ok && !sf.IsAck() {
			settings = sf
		}
	}
	require.NotNil(t, settings, "в преамбуле соединения обязан быть кадр настроек")

	got, ok := settings.Value(http2.SettingMaxConcurrentStreams)
	require.True(t, ok,
		"сервер не объявил предел одновременных вызовов: библиотека умалчивает эту настройку ровно "+
			"тогда, когда предел не ограничен, поэтому её отсутствие означает «открывай сколько хочешь» — "+
			"одно соединение занимает процесс, обслуживающий всех арендаторов")
	require.Equal(t, uint32(DefaultConcurrentStreamsPerConnection), got,
		"объявленный предел обязан совпадать с решением §8.6")
}

// TestServerRefusesAnOversizedResponse — отрицание: ответ сверх предела отправки
// не уходит с сервера.
//
// До явной опции предел отправки был `math.MaxInt32`, то есть один вызов мог
// потребовать двух гигабайт памяти процесса.
func TestServerRefusesAnOversizedResponse(t *testing.T) {
	over := DefaultSendMsgBytes + (1 << 20)
	conn := serveProbe(t, func(*wrapperspb.BytesValue) int { return over })

	err := echo(t, conn, 1)
	require.Error(t, err, "ответ сверх предела отправки обязан быть отвергнут сервером")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

// TestServerSendsAResponseUnderTheSendLimit — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к
// предыдущему случаю: законный ответ проходит.
//
// Без него отрицание зеленело бы и на сервере, отвергающем вообще всё.
func TestServerSendsAResponseUnderTheSendLimit(t *testing.T) {
	conn := serveProbe(t, func(*wrapperspb.BytesValue) int { return 2 << 20 })

	require.NoError(t, echo(t, conn, 1),
		"ответ размером с законную страницу обязан проходить: предел отправки выбран с запасом над ней")
}

// TestServerRefusesAnOversizedRequest — предел приёма исполняется.
//
// Величина совпадает с умолчанием библиотеки, и решение ОСТАВИТЬ её записано в
// §8.6. Проба нужна именно потому, что решение молчаливое: без неё будущее
// ослабление предела не покраснеет нигде.
func TestServerRefusesAnOversizedRequest(t *testing.T) {
	conn := serveProbe(t, func(*wrapperspb.BytesValue) int { return 1 })

	err := echo(t, conn, DefaultRecvMsgBytes+(1<<20))
	require.Error(t, err, "запрос сверх предела приёма обязан быть отвергнут сервером")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

// TestServerAcceptsARequestUnderTheReceiveLimit — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к
// предыдущему случаю.
func TestServerAcceptsARequestUnderTheReceiveLimit(t *testing.T) {
	conn := serveProbe(t, func(*wrapperspb.BytesValue) int { return 1 })

	require.NoError(t, echo(t, conn, 3<<20),
		"запрос под пределом приёма обязан проходить")
}

// TestCallerOptionOverridesTheDefaultLimits — умолчания носителя не запечатаны:
// вызывающий, которому нужен другой предел, ставит свою опцию, и она выигрывает.
//
// Порядок здесь несущий: умолчания идут ПЕРВЫМИ, поэтому опция вызывающего
// применяется позже и перекрывает их. Обратный порядок сделал бы величины
// неизменяемыми, а несовпадающую опцию — принятой и проигнорированной.
func TestCallerOptionOverridesTheDefaultLimits(t *testing.T) {
	conn := serveProbe(t, func(*wrapperspb.BytesValue) int { return 1 },
		grpc.MaxRecvMsgSize(6<<20))

	require.NoError(t, echo(t, conn, 5<<20),
		"опция вызывающего обязана перекрывать умолчание носителя")
}
