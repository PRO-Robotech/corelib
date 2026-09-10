// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

// identity_arrival_test.go — цепочка, собранная носителем, НАБЛЮДАЕТ исход
// личности каждого вызова (приёмка KAN-WIRE-1, KAN-W2-02, предмет `ПР-1`).
//
// # Почему проба здесь, а не только у звена
//
// Само звено умеет считать — это проверено у него (`pkg/grpcsrv`). Здесь
// проверяется другое: что носитель контура ему счётчик ОТДАЛ. Пока счётчик
// заводит каждый композиционный корень у себя, «не завёл» неотличимо от «завёл
// такой же»: серии просто нет, слушатель поднимается молча, и узнают об этом в
// разборе происшествия, когда данных уже не будет.
//
// Проба читает ЗНАЧЕНИЯ из реестра: «звено стоит в цепочке» остаётся верным и
// тогда, когда ему не с чем считать.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// TestCarrierChainObservesIdentityArrival — цепочка носителя кладёт исход
// личности в реестр, отданный дескриптором.
func TestCarrierChainObservesIdentityArrival(t *testing.T) {
	reg := prometheus.NewRegistry()
	spec := chainSpec()
	spec.Metrics = reg

	var slot decisionSlot
	lat, err := grpcsrv.NewServerLatency(reg)
	if err != nil {
		t.Fatalf("измеритель задержки: %v", err)
	}
	arrival, err := grpcsrv.NewIdentityArrival(reg)
	if err != nil {
		t.Fatalf("счётчик исходов личности: %v", err)
	}
	chain := unaryChain(spec, &slot, lat, arrival, grpcsrv.ListenerPublic)
	chain = chain[:len(chain)-1] // слот решения наполняет отдельный шаг подъёма

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "req-1"))
	if _, err := runUnaryChain(chain, ctx,
		"/kacho.cloud.demo.v1.WidgetService/Get", nil,
		func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("законный безымянный вызов отвергнут: %v", err)
	}

	rows := rowsOf(t, reg, "kacho_grpc_identity_arrival_total")
	t.Logf("осмотрено: рядов семейства исходов личности %d", len(rows))
	if len(rows) == 0 {
		t.Fatal("носитель собрал цепочку БЕЗ счётчика исходов личности: серии нет, и " +
			"«личность объявлена и не приехала» неотличимо от роста безымянных вызовов")
	}
	var total float64
	for _, m := range rows {
		if labelValue(m, "outcome") == "" {
			t.Errorf("ряд без метки исхода — полосы неразличимы: %v", m)
		}
		total += m.GetCounter().GetValue()
	}
	if total != 1 {
		t.Errorf("наблюдений %v, ожидалось ровно одно на один вызов", total)
	}
}
