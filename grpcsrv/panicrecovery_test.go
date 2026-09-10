// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// Проба звена, восстанавливающего панику обработчика.
//
// Обе половины пары гоняются ОДНИМ И ТЕМ ЖЕ харнессом в дочернем процессе и
// различаются ровно одним: провязано звено или нет. Так отрицание («без звена
// процесс умирает») получает положительный контроль («со звеном тот же самый
// запрос отвечает, и процесс продолжает обслуживать») — иначе «умер» было бы
// неотличимо от «харнесс сломан и умирает всегда».
//
// Почему дочерний процесс. Предмет пробы — смерть ПРОЦЕССА: непойманная паника
// в серверной горутине печатает трассу и вызывает exit(2), унося с собой и сам
// тест. Утверждать это изнутри того же процесса нельзя by construction.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

// panicPayload — значение паники. Намеренно похоже на то, что паника несёт в
// жизни (внутренние координаты и значения), и намеренно НЕ похоже на легальный
// текст ответа: проба утверждает, что ни один его фрагмент не доехал до клиента.
const panicPayload = "invalid memory address or nil pointer dereference near kacho_internal_secret_marker"

const (
	probeService    = "kacho.grpcsrv.panicprobe.v1.PanicProbe"
	probeBoomMethod = "/" + probeService + "/Boom"
	probeFineMethod = "/" + probeService + "/Fine"
	probeBoomStream = "/" + probeService + "/BoomStream"
)

// childModeEnv — режим дочернего процесса: "with" (звено провязано) либо
// "without" (не провязано).
const childModeEnv = "KACHO_PANIC_PROBE_CHILD_MODE"

// probeServiceDesc — минимальный gRPC-сервис, поднимаемый пробой. Собран
// вручную, а не из сгенерированных стабов: предмет пробы — цепочка
// интерсепторов сервера, и она не должна зависеть ни от одного доменного
// контракта, который завтра переименуют.
func probeServiceDesc() *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: probeService,
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Boom",
				Handler: func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
					in := new(emptypb.Empty)
					if err := dec(in); err != nil {
						return nil, err
					}
					h := func(context.Context, any) (any, error) { panic(panicPayload) }
					if ic == nil {
						return h(ctx, in)
					}
					return ic(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: probeBoomMethod}, h)
				},
			},
			{
				MethodName: "Fine",
				Handler: func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
					in := new(emptypb.Empty)
					if err := dec(in); err != nil {
						return nil, err
					}
					h := func(context.Context, any) (any, error) { return &emptypb.Empty{}, nil }
					if ic == nil {
						return h(ctx, in)
					}
					return ic(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: probeFineMethod}, h)
				},
			},
		},
		Streams: []grpc.StreamDesc{
			{
				StreamName:    "BoomStream",
				Handler:       func(any, grpc.ServerStream) error { panic(panicPayload) },
				ServerStreams: true,
			},
		},
	}
}

// fatalf — минимальный приёмник отказа, чтобы харнесс работал и под go-test,
// и в дочернем процессе, где *testing.T недоступен.
type fatalf interface{ Fatalf(string, ...any) }

type childT struct{}

func (childT) Fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "дочерний харнесс: "+f+"\n", a...)
	os.Exit(3)
}

// dialProbe поднимает сервер с заданной цепочкой на bufconn и возвращает
// соединение к нему.
func dialProbe(t fatalf, unary []grpc.UnaryServerInterceptor, stream []grpc.StreamServerInterceptor) (*grpc.ClientConn, func()) {
	lis := bufconn.Listen(1 << 20)
	var opts []grpc.ServerOption
	if len(unary) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(unary...))
	}
	if len(stream) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(stream...))
	}
	srv := grpc.NewServer(opts...)
	srv.RegisterService(probeServiceDesc(), new(struct{}))
	go func() { _ = srv.Serve(lis) }()

	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("подключение к пробе: %v", err)
	}
	return cc, func() { _ = cc.Close(); srv.Stop() }
}

// runChild исполняет одну половину пары внутри дочернего процесса и завершает
// его. Возврат из вызова Invoke означает «процесс пережил панику».
func runChild(mode string) {
	unary, stream := childChain(mode)
	cc, closeFn := dialProbe(childT{}, unary, stream)
	defer closeFn()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := cc.Invoke(ctx, probeBoomMethod, &emptypb.Empty{}, &emptypb.Empty{})
	st, _ := status.FromError(err)
	fmt.Printf("ВЫЖИЛ code=%s msg=%q\n", st.Code(), st.Message())

	// Живой процесс обязан продолжать обслуживать: следующий запрос — успешный.
	if err := cc.Invoke(ctx, probeFineMethod, &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		fmt.Fprintf(os.Stderr, "после восстановленной паники сервер не обслуживает: %v\n", err)
		os.Exit(4)
	}
	fmt.Println("ОБСЛУЖИВАЕТ ДАЛЬШЕ")
	os.Exit(0)
}

// spawnChild перезапускает этот же тестовый бинарь в заданном режиме.
func spawnChild(t *testing.T, mode string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestPanicProbeChildHarness", "-test.v")
	cmd.Env = append(os.Environ(), childModeEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestPanicProbeChildHarness — точка входа дочернего процесса. В родительском
// прогоне (переменная не выставлена) он ничего не делает.
func TestPanicProbeChildHarness(t *testing.T) {
	mode := os.Getenv(childModeEnv)
	if mode == "" {
		t.Skip("не дочерний процесс")
	}
	runChild(mode)
}

// TestHandlerPanicWithoutRecoveryLinkKillsTheProcess — ПРЕДПОСЫЛКА всего
// предмета: grpc-go панику обработчика не восстанавливает, поэтому дефект в
// обработке одного запроса прекращает обслуживание всех.
//
// Если этот тест когда-нибудь позеленеет «сам» (процесс выживет без звена),
// значит библиотека сменила поведение — и тогда обоснование звена надо
// перепроверить, а не молча оставить.
func TestHandlerPanicWithoutRecoveryLinkKillsTheProcess(t *testing.T) {
	out, err := spawnChild(t, "without")
	if err == nil {
		t.Fatalf("без звена восстановления процесс ВЫЖИЛ — предпосылка сломана.\n%s", out)
	}
	if !strings.Contains(out, "panic:") {
		t.Fatalf("процесс умер, но не от паники — харнесс меряет не то.\n%s", out)
	}
	if strings.Contains(out, "ВЫЖИЛ") {
		t.Fatalf("клиент получил ответ, хотя процесс умер — харнесс меряет не то.\n%s", out)
	}
	t.Logf("без звена: процесс завершился (%v), трасса паники напечатана", err)
}
