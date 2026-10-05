// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

import (
	"testing"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// TestPublicPlatformMethodsAreServedByTheConstructor — самоистечение
// объявления: каждый метод, который фундамент объявляет публичным, обязан
// служиться сервером, собранным NewServer. Запись о методе, которого
// конструктор больше не регистрирует, — находка: освобождение пережило бы свой
// предмет и молча досталось бы следующему, кто займёт имя.
//
// Предикат внешний — перечень служб сервера (`GetServiceInfo`), а не тот же
// перечень, из которого объявление собрано.
func TestPublicPlatformMethodsAreServedByTheConstructor(t *testing.T) {
	served := map[string]bool{}
	for svc, info := range NewServer().GetServiceInfo() {
		for _, m := range info.Methods {
			served["/"+svc+"/"+m.Name] = true
		}
	}
	declared := PublicPlatformMethods()
	t.Logf("перепись: служимых методов %d, объявлено публичными %d", len(served), len(declared))
	if len(served) == 0 {
		t.Fatal("конструктор не служит ни одного метода — предикат самоистечения ничего не читает")
	}
	if len(declared) == 0 {
		t.Fatal("фундамент не объявил публичным ни одного метода — проба живости остаётся за дверью")
	}
	for _, m := range declared {
		if !served[m] {
			t.Errorf("метод %q объявлен публичным, но конструктор сервера его не служит", m)
		}
		if !IsPublicPlatformMethod(m) {
			t.Errorf("метод %q есть в перечне, но предикат его не признаёт — два места об одном предмете", m)
		}
	}
}

// TestPublicPlatformMethodsAreExactlyTheLivenessCheck — круг узок намеренно:
// публичным объявлен ровно Check. Watch той же службы и рефлексия остаются за
// дверью — их не зовёт ни край, ни kubelet.
func TestPublicPlatformMethodsAreExactlyTheLivenessCheck(t *testing.T) {
	got := PublicPlatformMethods()
	if len(got) != 1 || got[0] != healthpb.Health_Check_FullMethodName {
		t.Fatalf("публичные методы фундамента: %v, ожидался ровно %q", got, healthpb.Health_Check_FullMethodName)
	}
	for _, m := range []string{
		healthpb.Health_Watch_FullMethodName,
		"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
		"/grpc.health.v1.Health/check",
		"grpc.health.v1.Health/Check",
		"",
	} {
		if IsPublicPlatformMethod(m) {
			t.Errorf("метод %q признан публичным — освобождение шире пробы живости", m)
		}
	}
}
