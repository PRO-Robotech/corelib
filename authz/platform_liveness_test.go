// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// Проба живости, которую фундамент регистрирует каждому серверу сам
// (`grpcsrv.NewServer`), обязана проходить звено решения о доступе без токена:
// её зовут край и kubelet, у которых личности арендатора нет и быть не может, а
// в карте прав сервиса этой службы нет by construction — карта выводится из
// аннотаций доменного контракта (corelib#90).
//
// Пробы стоят на проводе, а не на прямом вызове звена: предмет — «сервер,
// собранный конструктором фундамента, с этим звеном в цепочке», и только так
// видно, что разрешение достаётся ровно тому методу, который сервер служит.

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// syncBuffer — журнал, в который пишут обработчики сервера из своих горутин.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// countLines — число строк журнала, несущих данное сообщение.
func (b *syncBuffer) countLines(msg string) int {
	n := 0
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "msg="+msg+" ") || strings.HasSuffix(line, "msg="+msg) {
			n++
		}
	}
	return n
}

// livenessServer поднимает сервер КОНСТРУКТОРОМ ФУНДАМЕНТА со звеном решения о
// доступе в обеих цепочках и отдаёт клиент к нему. Карта — доменная, без единой
// записи о здоровье: ровно та, что выводится из контракта сервиса.
func livenessServer(t *testing.T, m authz.RPCMap, checks *int) (*grpc.ClientConn, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	var mu sync.Mutex
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache: authz.NewCache(0),
		Map:   m,
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			mu.Lock()
			*checks++
			mu.Unlock()
			return true, nil
		}),
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
	})
	lis := bufconn.Listen(1 << 20)
	srv := grpcsrv.NewServer(
		grpc.ChainUnaryInterceptor(intr.Unary()),
		grpc.ChainStreamInterceptor(intr.Stream()),
	)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("клиент к серверу фундамента: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, logs
}

// TestHealthCheckAnswersWithoutATokenOnTheFoundationServer — предикат задачи:
// Check без личности отвечает SERVING, модель прав не спрашивается, и строки
// «неразмеченный RPC» в журнале нет.
func TestHealthCheckAnswersWithoutATokenOnTheFoundationServer(t *testing.T) {
	checks := 0
	conn, logs := livenessServer(t, makeMap(), &checks)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check без токена отвергнут: %v (код %v)", err, status.Code(err))
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("Check без токена: статус %v, ожидался SERVING", resp.GetStatus())
	}
	if checks != 0 {
		t.Fatalf("о пробе живости спросили модель прав %d раз — освобождение обязано стоять ДО вопроса", checks)
	}
	if n := logs.countLines("authz_unmapped_rpc"); n != 0 {
		t.Fatalf("проба живости записана неразмеченной %d раз:\n%s", n, logs)
	}
}

// TestHealthWatchStaysBehindTheDoor — законный близнец: соседний метод той же
// службы, регистрируемый тем же конструктором, освобождения НЕ получает.
// Освобождение заводится вместе с тем, кто его зовёт, а Watch не зовёт никто.
func TestHealthWatchStaysBehindTheDoor(t *testing.T) {
	checks := 0
	conn, logs := livenessServer(t, makeMap(), &checks)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Watch без токена: ожидался PermissionDenied, получено %v", err)
	}
	if !strings.Contains(status.Convert(err).Message(), "rpc not mapped") {
		t.Fatalf("Watch отвергнут не как неразмеченный: %q", status.Convert(err).Message())
	}
	if n := logs.countLines("authz_unmapped_rpc"); n != 1 {
		t.Fatalf("строк о неразмеченном Watch %d, ожидалась 1:\n%s", n, logs)
	}
}

// TestDomainMethodAbsentFromTheMapIsStillRefused — второй близнец: освобождение
// именное, а не «всё, чего нет в карте». Доменный метод вне карты отвергается
// по-прежнему.
func TestDomainMethodAbsentFromTheMapIsStillRefused(t *testing.T) {
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache: authz.NewCache(0),
		Map:   makeMap(),
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			t.Fatal("о неразмеченном методе модель прав не спрашивают")
			return false, nil
		}),
	})
	for _, m := range []string{
		"/kacho.cloud.vpc.v1.UnknownService/Foo",
		// Та же форма имени, другая служба: сравнение обязано быть по полному
		// имени метода, а не по его хвосту.
		"/kacho.cloud.vpc.v1.Health/Check",
	} {
		_, err := runUnary(intr, context.Background(), m, &fakeReq{id: "x"})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("%s: ожидался PermissionDenied, получено %v", m, err)
		}
	}
}

// TestContractDeclarationOfTheProbeOutranksThePlatformOne — карта побеждает:
// если контракт однажды объявит метод с этим именем, действует его запись, а не
// освобождение фундамента. Иначе аннотация молча перестала бы действовать.
func TestContractDeclarationOfTheProbeOutranksThePlatformOne(t *testing.T) {
	checks := 0
	m := makeMap()
	m[healthpb.Health_Check_FullMethodName] = authz.RPCEntry{
		Relation: "viewer",
		Extract: authz.StaticExtractor("project", func(any) (string, error) {
			return "prj_x", nil
		}),
	}
	conn, _ := livenessServer(t, m, &checks)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("при записи контракта Check без личности обязан быть отвергнут, получено %v", err)
	}
}
