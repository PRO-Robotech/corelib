// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db_test

// connbudget_test.go — служба не вправе обещать себе больше соединений, чем
// база готова принять.
//
// Две величины — размер пула и число реплик — назначаются в РАЗНЫХ местах и
// потому расходятся молча: пул объявлен на реплику, предел базы общий на все.
// Пока нагрузки нет, расхождение ничем не выдаёт себя; узнаётся оно отказом в
// подключении ровно тогда, когда соединения действительно понадобились.
//
// Пробы утверждают ЧИСЛА и ИСХОД (стартует служба или отказывается), а не
// наличие константы.

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/db"
)

func TestConnBudget_RefusesWhenTheProductExceedsTheCeiling(t *testing.T) {
	// Ровно та посадка, на которой это и намерили: пул 100 на реплику, пять
	// реплик, предел базы 200.
	budget := db.ConnBudget{PoolMaxConns: 100, Replicas: 5}
	ceiling := db.ConnCeiling{MaxConnections: 200, SuperuserReserved: 3}

	err := budget.Validate(ceiling)
	if err == nil {
		t.Fatal("500 обещанных соединений против 200 принимаемых — принято. Служба обещает " +
			"себе больше, чем база готова дать, и узнаёт об этом отказом под нагрузкой")
	}
	// Отказ обязан НАЗЫВАТЬ числа: оператор чинит настройку, а не гадает.
	for _, want := range []string{"100", "5", "500", "200"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в отказе нет числа %q: %v", want, err)
		}
	}
}

func TestConnBudget_AcceptsWhatFits(t *testing.T) {
	// Положительный контроль: без него «отвергнуто» было бы неотличимо от
	// «отвергается всё», и проверка ничего не сужала бы.
	budget := db.ConnBudget{PoolMaxConns: 30, Replicas: 5}
	ceiling := db.ConnCeiling{MaxConnections: 200, SuperuserReserved: 3}

	if err := budget.Validate(ceiling); err != nil {
		t.Fatalf("150 обещанных против 197 доступных отвергнуты: %v — проверка ловит форму, "+
			"а не существо", err)
	}
}

func TestConnBudget_TheReserveIsPartOfTheCeiling(t *testing.T) {
	// Граница проходит по ДОСТУПНОМУ, а не по объявленному: слоты, которые база
	// держит за собой, службе не принадлежат. 197 = 200 − 3.
	ceiling := db.ConnCeiling{MaxConnections: 200, SuperuserReserved: 3}

	if err := (db.ConnBudget{PoolMaxConns: 197, Replicas: 1}).Validate(ceiling); err != nil {
		t.Errorf("ровно доступное отвергнуто: %v", err)
	}
	if err := (db.ConnBudget{PoolMaxConns: 198, Replicas: 1}).Validate(ceiling); err == nil {
		t.Error("на единицу сверх доступного принято — запас базы роздан службе")
	}
}

// Необъявленный бюджет реплик — ОТКАЗ, а не «проверять нечего».
//
// Ноль здесь означал бы «не сужаем»: произведение обратилось бы в ноль и прошло
// бы любую проверку. Ровно тот класс, что пустой круг доверенных отправителей,
// который пропускает всех.
func TestConnBudget_UndeclaredReplicaBudgetIsRefusedNotSkipped(t *testing.T) {
	ceiling := db.ConnCeiling{MaxConnections: 200, SuperuserReserved: 3}

	if err := (db.ConnBudget{PoolMaxConns: 100, Replicas: 0}).Validate(ceiling); err == nil {
		t.Error("бюджет реплик не объявлен — принято. Ноль реплик обращает произведение " +
			"в ноль, и проверка проходит всегда, ничего не проверив")
	}
	if err := (db.ConnBudget{PoolMaxConns: 0, Replicas: 5}).Validate(ceiling); err == nil {
		t.Error("размер пула не объявлен — принято, по той же причине")
	}
}

// Предел базы, который не удалось прочитать, — тоже отказ.
func TestConnBudget_UnreadableCeilingIsRefused(t *testing.T) {
	if err := (db.ConnBudget{PoolMaxConns: 10, Replicas: 1}).Validate(db.ConnCeiling{}); err == nil {
		t.Error("нулевой предел базы принят — «предел не прочитан» стало бы неотличимо " +
			"от «предел бесконечен»")
	}
}
