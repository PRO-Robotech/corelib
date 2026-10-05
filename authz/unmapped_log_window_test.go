// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// Строка «неразмеченный RPC» ограничена по частоте (corelib#91): периодический
// источник вызовов (проба) иначе заполняет журнал одинаковыми строками — на
// стенде 4320 строк за 6 ч на сервис от одной пробы. Ограничение журнала не
// трогает РЕШЕНИЯ: каждый вызов по-прежнему отвергается и считается.

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
)

func unmappedInterceptor(t *testing.T) (*authz.Interceptor, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache: authz.NewCache(0),
		Map:   makeMap(),
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			t.Error("о неразмеченном методе модель прав не спрашивают")
			return false, nil
		}),
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
	})
	return intr, logs
}

// TestUnmappedLineIsWrittenOncePerWindowPerMethod — сто вызовов неразмеченного
// метода в одном окне дают одну строку; каждый из ста отвергнут и посчитан.
// Другой метод получает СВОЮ первую строку: ключ ограничения — метод, а не
// звено целиком, иначе первая проба глушила бы все прочие находки.
func TestUnmappedLineIsWrittenOncePerWindowPerMethod(t *testing.T) {
	intr, logs := unmappedInterceptor(t)
	const calls = 100
	for i := 0; i < calls; i++ {
		_, err := runUnary(intr, context.Background(), "/kacho.cloud.vpc.v1.UnknownService/Foo", &fakeReq{id: "x"})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("вызов %d: ограничение журнала смягчило решение: %v", i+1, err)
		}
	}
	if n := logs.countLines("authz_unmapped_rpc"); n != 1 {
		t.Fatalf("строк authz_unmapped_rpc за %d вызовов в одном окне: %d, ожидалась 1", calls, n)
	}
	if got := intr.Metrics().Unmapped; got != calls {
		t.Fatalf("счётчик решений Unmapped=%d, ожидалось %d — подавляется строка, а не учёт", got, calls)
	}

	_, _ = runUnary(intr, context.Background(), "/kacho.cloud.vpc.v1.UnknownService/Bar", &fakeReq{id: "x"})
	if n := logs.countLines("authz_unmapped_rpc"); n != 2 {
		t.Fatalf("другой метод не получил своей первой строки: строк %d, ожидалось 2\n%s", n, logs)
	}
}

// TestUnmappedLineWindowHoldsUnderConcurrency — то же свойство под гонкой:
// одновременные вызовы одного метода дают ровно одну строку (под -race).
func TestUnmappedLineWindowHoldsUnderConcurrency(t *testing.T) {
	intr, logs := unmappedInterceptor(t)
	const calls = 64
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			_, _ = runUnary(intr, context.Background(), "/kacho.cloud.vpc.v1.UnknownService/Foo", &fakeReq{id: "x"})
		}()
	}
	wg.Wait()
	if n := logs.countLines("authz_unmapped_rpc"); n != 1 {
		t.Fatalf("строк при %d одновременных вызовах: %d, ожидалась 1", calls, n)
	}
	if got := intr.Metrics().Unmapped; got != calls {
		t.Fatalf("Unmapped=%d, ожидалось %d", got, calls)
	}
}
