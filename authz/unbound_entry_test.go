// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
)

// TestInterceptor_EntryWithoutExtractorDenies — запись, требующая отношения, но
// не несущая извлекателя объекта, отвергается голосом прав, а не роняет вызов.
//
// Производитель такой записи — форма ScopeBound (З14): вывод оставляет
// извлекатель пустым, заполняет его привязка сервера. Носитель непривязанную
// запись до перехватчика не пускает (отказ старта), но перехватчик — отдельная
// дверь: его строят и без носителя, и пустой извлекатель там был бы вызовом
// nil-функции — паникой в горутине запроса, а не отказом. Модель при этом не
// спрашивается ни о чём: объекта нет, спрашивать не о чем.
func TestInterceptor_EntryWithoutExtractorDenies(t *testing.T) {
	const method = "/corelib.notify.InternalNotificationFeedService/Claim"
	for _, e := range []authz.RPCEntry{
		{Relation: "reader", BoundType: "notification_feed"},
		{Relation: "reader"},
	} {
		stub := authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			t.Fatalf("модель спрошена о записи без объекта")
			return false, nil
		})
		intr := authz.NewInterceptor(authz.InterceptorOptions{
			Cache:  authz.NewCache(0),
			Map:    authz.RPCMap{method: e},
			Client: stub,
		})
		handled := false
		_, err := intr.Unary()(ctxWithPrincipal(t, "usr_alice", "user"), &fakeReq{id: "x"},
			&grpc.UnaryServerInfo{FullMethod: method},
			func(context.Context, any) (any, error) { handled = true; return nil, nil })
		if handled {
			t.Fatalf("обработчик вызван для записи без объекта (BoundType=%q)", e.BoundType)
		}
		if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "permission denied" {
			t.Fatalf("ожидался PERMISSION_DENIED «permission denied», получено %v", err)
		}

		called, serr := runStream(intr, ctxWithPrincipal(t, "usr_alice", "user"), method)
		if called || status.Code(serr) != codes.PermissionDenied {
			t.Fatalf("поток: обработчик=%v, ошибка %v — ожидался отказ", called, serr)
		}
	}
}
