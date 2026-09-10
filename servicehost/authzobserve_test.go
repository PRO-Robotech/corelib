// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// authzobserve_test.go — носитель контура ОТДАЁТ величины кеша вердиктов наружу.
//
// # Предмет
//
// Кеш положительных вердиктов строит носитель (`decisionLink`), а
// диагностическую поверхность держит композиционный корень. Пока величины не
// переходят эту границу, доля попаданий не наблюдается НИ У ОДНОГО из шести
// сервисов: счётчики живут в процессе и никуда не выходят.
//
// Проба идёт ТЕМ ЖЕ путём, каким собирает носитель, и утверждает РОСТ: читатель,
// который отдали корню, обязан показать попадание после повторного вопроса.
// Проба «читателя отдали» осталась бы зелёной на читателе, читающем не тот кеш.
package servicehost

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// TestCarrierHandsOutTheVerdictCacheReader — читатель величин кеша доходит до
// корня, и он читает ТОТ кеш, который спрашивает звено.
func TestCarrierHandsOutTheVerdictCacheReader(t *testing.T) {
	var read func() authz.Metrics

	spec := chainSpec()
	spec.Authz = servicecontract.AuthzSelf
	spec.SelfCheck = authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
		return true, nil
	})
	spec.DenyBudget = servicecontract.NotApplicable[float64]("проба меряет наблюдаемость кеша, а не темп")
	spec.AuthzObserve = func(r func() authz.Metrics) { read = r }

	intr, closeEdge, err := decisionLink(spec, carrierMap())
	if err != nil {
		t.Fatalf("звено решения о доступе не собралось: %v", err)
	}
	if closeEdge != nil {
		defer closeEdge()
	}
	if read == nil {
		t.Fatalf("носитель не отдал читателя величин кеша: доля попаданий не выходит из процесса, " +
			"и «кеш не попадает ни разу» снаружи неотличимо от «кеш поглощает весь поток»")
	}

	// Положительный контроль: до единого вопроса попаданий нет.
	if s := read().Cache; s.Hits != 0 || s.Misses != 0 {
		t.Fatalf("до первого вызова кеш не спрашивали, получено %+v", s)
	}

	call := func() {
		if _, cerr := intr.Unary()(principalCtx(), probedID,
			&grpc.UnaryServerInfo{FullMethod: probedMethod},
			func(context.Context, any) (any, error) { return "handled", nil }); cerr != nil {
			t.Fatalf("вызов через звено: %v", cerr)
		}
	}

	call()
	if s := read().Cache; s.Misses != 1 || s.Hits != 0 {
		t.Fatalf("первый вопрос обязан быть промахом, получено %+v", s)
	}
	call()
	if s := read().Cache; s.Hits != 1 {
		t.Fatalf("повтор того же вопроса обязан быть попаданием, получено %+v", s)
	}
}
