// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db_test

// connbudget_nonpool_test.go — соединения ПОМИМО ПУЛА входят в обещанное
// (kaname#113).
//
// # Что было неверно
//
// Проверка считала только пул и говорила об этом честно: «пройденная проверка
// НЕОБХОДИМА, но не достаточна». Честность не спасала — служба, держащая потоки
// подписки, обещает базе на реплику пул ПЛЮС потолок потоков, и проверка
// отвечала «помещается» там, где база отказывает в соединении всему процессу.
//
// # Почему величина ОБЪЯВЛЯЕТСЯ, а не выдумывается здесь
//
// Сколько соединений служба держит сверх пула, знает служба: у одной это потоки
// подписки, у другой — ожидание оповещений и опросчики очередей. Число,
// вписанное фундаментом «на глаз», было бы ложной точностью — ровно то, что
// прежняя редакция и отказывалась делать.
//
// # Почему НЕОБЪЯВЛЕННОЕ отличимо от объявленного НУЛЯ
//
// Ноль — законное объявление: служба, не держащая ничего сверх пула, обязана
// иметь возможность это СКАЗАТЬ. Если бы ноль означал «не объявлено», такая
// служба не могла бы отличить себя от той, которая просто забыла, — и проверка
// снова считала бы нижнюю границу, называя её полной.

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/db"
)

func intPtr(n int) *int { return &n }

// TestConnBudget_NonPoolConnectionsCountTowardThePromise — ОТРИЦАНИЕ: посадка,
// помещающаяся по пулу и НЕ помещающаяся вместе с потоками, отвергается.
//
// Числа — из задачи kaname#113: пул 8, реплик 2, потоков 64, потолок 97.
func TestConnBudget_NonPoolConnectionsCountTowardThePromise(t *testing.T) {
	budget := db.ConnBudget{PoolMaxConns: 8, Replicas: 2, NonPoolConnsPerReplica: intPtr(64)}
	ceiling := db.ConnCeiling{MaxConnections: 97, SuperuserReserved: 3}

	if err := (db.ConnBudget{PoolMaxConns: 8, Replicas: 2}).Validate(ceiling); err != nil {
		t.Fatalf("предпосылка не выполнена: по одному пулу посадка обязана помещаться "+
			"(16 против 94), иначе проба меряет не то: %v", err)
	}

	err := budget.Validate(ceiling)
	if err == nil {
		t.Fatal("обещано 2 × (8 + 64) = 144 против 94 принимаемых — принято. " +
			"Под нагрузкой потоков база откажет в соединении ВСЕМУ процессу")
	}
	for _, want := range []string{"8", "64", "2", "144", "94"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в отказе нет числа %q: оператор чинит настройку, а не гадает: %v", want, err)
		}
	}
}

// TestConnBudget_OneReplicaWithTheSameStreamsFits — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ
// (законный близнец из задачи): та же служба на ОДНОЙ реплике помещается.
//
// Без него отрицание выше выполняется тождественно на проверке, отвергающей всё.
func TestConnBudget_OneReplicaWithTheSameStreamsFits(t *testing.T) {
	budget := db.ConnBudget{PoolMaxConns: 8, Replicas: 1, NonPoolConnsPerReplica: intPtr(64)}
	ceiling := db.ConnCeiling{MaxConnections: 97, SuperuserReserved: 3}

	if err := budget.Validate(ceiling); err != nil {
		t.Fatalf("72 обещанных против 94 принимаемых отвергнуты: %v", err)
	}
}

// TestConnBudget_DeclaredZeroIsADeclaration — объявленный ноль есть объявление.
func TestConnBudget_DeclaredZeroIsADeclaration(t *testing.T) {
	budget := db.ConnBudget{PoolMaxConns: 40, Replicas: 2, NonPoolConnsPerReplica: intPtr(0)}
	ceiling := db.ConnCeiling{MaxConnections: 97, SuperuserReserved: 3}

	if err := budget.Validate(ceiling); err != nil {
		t.Fatalf("80 обещанных против 94 принимаемых отвергнуты: %v", err)
	}
	if !budget.CountsEverything() {
		t.Fatal("объявленный ноль обязан считаться объявлением: служба, ничего не " +
			"держащая сверх пула, должна иметь возможность это СКАЗАТЬ")
	}
}

// TestConnBudget_UndeclaredIsALowerBoundAndSaysSo — необъявленное не отвергается
// (это не ломающее изменение фундамента), но проверка ГОВОРИТ, что считает
// нижнюю границу.
//
// Молчание здесь было бы тем же классом, что «пусто = не сужаем»: вызывающий
// прочитал бы «помещается» как полный вердикт.
func TestConnBudget_UndeclaredIsALowerBoundAndSaysSo(t *testing.T) {
	budget := db.ConnBudget{PoolMaxConns: 40, Replicas: 2}
	if budget.CountsEverything() {
		t.Fatal("необъявленные непуловые соединения обязаны быть отличимы от объявленного нуля")
	}

	ceiling := db.ConnCeiling{MaxConnections: 97, SuperuserReserved: 3}
	if err := budget.Validate(ceiling); err != nil {
		t.Fatalf("необъявленное не отвергается: вводить обязательное поле значило бы "+
			"уронить старт каждой службы, ещё не объявившей своё: %v", err)
	}

	// А вот в тексте ОТКАЗА граница обязана быть названа.
	err := (db.ConnBudget{PoolMaxConns: 100, Replicas: 5}).Validate(ceiling)
	if err == nil {
		t.Fatal("предпосылка не выполнена: посадка обязана не помещаться")
	}
	if !strings.Contains(err.Error(), "нижняя граница") {
		t.Errorf("отказ обязан называть неполноту счёта, пока непуловые не объявлены: %v", err)
	}
}

// TestConnBudget_NegativeDeclarationIsRefused — отрицательное объявление есть
// ошибка настройки, а не «вычесть из обещанного».
func TestConnBudget_NegativeDeclarationIsRefused(t *testing.T) {
	budget := db.ConnBudget{PoolMaxConns: 8, Replicas: 1, NonPoolConnsPerReplica: intPtr(-1)}
	ceiling := db.ConnCeiling{MaxConnections: 97, SuperuserReserved: 3}

	if err := budget.Validate(ceiling); err == nil {
		t.Fatal("отрицательное число непуловых соединений принято — оно уменьшало бы " +
			"обещанное и делало бы проверку тем мягче, чем грубее ошибка настройки")
	}
}
