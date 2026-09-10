// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db_test

// connbudget_integration_test.go — предел спрашивается у БАЗЫ, и проверка
// применяется к настоящим числам, а не к литералам пробы.
//
// Литералы отвечают на «умеет ли арифметика»; здесь спрашивается другое: доедет
// ли вопрос до базы, тот ли параметр прочитан и отказывает ли проверка на живом
// пуле. Числа берутся из базы и из пула — ни одно не вписано.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/corelib/db"
	"github.com/PRO-Robotech/corelib/pgtest"
)

func TestReadConnCeiling_AsksTheDatabaseNotTheConfiguration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: требует Postgres (testcontainers)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.NewDB(t))
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, pool)

	ceiling, err := db.ReadConnCeiling(ctx, pool)
	if err != nil {
		t.Fatalf("чтение пределов: %v", err)
	}
	if ceiling.MaxConnections <= 0 {
		t.Fatalf("предел соединений прочитан как %d — вопрос до базы не доехал либо прочитан "+
			"не тот параметр", ceiling.MaxConnections)
	}
	if ceiling.SuperuserReserved <= 0 {
		t.Errorf("запас суперпользователя прочитан как %d — у Postgres он по умолчанию "+
			"положителен, значит читается не то", ceiling.SuperuserReserved)
	}
	if ceiling.Available() >= ceiling.MaxConnections {
		t.Errorf("доступное (%d) не меньше объявленного (%d) — запас базы роздан службе",
			ceiling.Available(), ceiling.MaxConnections)
	}
	t.Logf("прочитано у базы: max_connections=%d, запас суперпользователя=%d, доступно=%d",
		ceiling.MaxConnections, ceiling.SuperuserReserved, ceiling.Available())
}

// Проверка применяется к ЖИВОМУ пулу и НАСТОЯЩЕМУ пределу: число реплик берётся
// такое, чтобы произведение заведомо не поместилось, — и оно выводится из
// прочитанного, а не вписано.
func TestConnBudget_RefusesALivePoolThatDoesNotFit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: требует Postgres (testcontainers)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.NewDB(t))
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, pool)

	ceiling, err := db.ReadConnCeiling(ctx, pool)
	if err != nil {
		t.Fatalf("чтение пределов: %v", err)
	}
	poolMax := pool.Config().MaxConns
	if poolMax <= 0 {
		t.Fatalf("пул сообщил предел %d — не у чего спрашивать", poolMax)
	}

	// Столько реплик, что произведение заведомо превышает доступное.
	tooMany := ceiling.Available()/int(poolMax) + 2
	err = db.ConnBudget{PoolMaxConns: poolMax, Replicas: tooMany}.Validate(ceiling)
	if err == nil {
		t.Fatalf("пул %d × %d реплик принят при доступных %d", poolMax, tooMany, ceiling.Available())
	}
	if !strings.Contains(err.Error(), "max_connections") {
		t.Errorf("отказ не назвал предел базы: %v", err)
	}

	// Законный близнец: та же посадка, помещающаяся в базу, принимается.
	fits := ceiling.Available() / int(poolMax)
	if fits < 1 {
		t.Skipf("пул одной реплики (%d) уже не помещается в доступное (%d) — законного "+
			"близнеца на этой базе не построить", poolMax, ceiling.Available())
	}
	if err := (db.ConnBudget{PoolMaxConns: poolMax, Replicas: fits}).Validate(ceiling); err != nil {
		t.Errorf("помещающаяся посадка (%d × %d ≤ %d) отвергнута: %v",
			poolMax, fits, ceiling.Available(), err)
	}
}
