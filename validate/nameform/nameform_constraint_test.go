// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package nameform_test

import (
	"testing"

	"github.com/PRO-Robotech/corelib/validate/nameform"
)

// TestIsConstraint — предикат «это ограничение формы имени, поставленное
// миграцией 715001 на ЭТУ таблицу».
//
// Предикат берёт ДВА аргумента, а не один, и это не удобство: миграция строит
// имя ограничения как `<таблица> || '_name_check'`, поэтому единственная верная
// проверка — сравнение с этой конструкцией. Суффиксный вариант («кончается на
// _name_check») выглядит проще и лжёт: `users_display_name_check` в схеме iam
// кончается ровно так же, а сторожит длину ДРУГОГО поля в схеме, которой 715001
// не касалась.
//
// Положительный и отрицательный контроль стоят рядом намеренно: предикат,
// проверенный только на «да», зеленеет и в тождественно-истинной форме, а
// проверенный только на «нет» — в тождественно-ложной.
func TestIsConstraint(t *testing.T) {
	// Пары взяты из ЖИВОЙ схемы (pg_constraint после прогона миграций каждого
	// сервиса), а не выдуманы: по одной на каждый из пяти сервисов, прошедших
	// 715001, плюс имена таблиц из двух и трёх слов.
	yes := []struct{ table, constraint string }{
		{"networks", "networks_name_check"},                   // vpc
		{"address_pools", "address_pools_name_check"},         // vpc, таблица из двух слов
		{"instances", "instances_name_check"},                 // compute
		{"guest_access_keys", "guest_access_keys_name_check"}, // compute, таблица из трёх слов
		{"volumes", "volumes_name_check"},                     // storage
		{"load_balancers", "load_balancers_name_check"},       // nlb
		{"regions", "regions_name_check"},                     // geo
	}
	for _, c := range yes {
		if !nameform.IsConstraint(c.table, c.constraint) {
			t.Errorf("IsConstraint(%q, %q) = false, ожидалось true", c.table, c.constraint)
		}
	}

	no := []struct{ table, constraint string }{
		// Ловушка суффиксного предиката: кончается на `_name_check`, но сторожит
		// длину поля display_name в схеме iam.
		{"users", "users_display_name_check"},
		{"networks", "networks_labels_valid"},
		{"addresses", "addresses_description_check"},
		{"subnets", "subnets_placement_type_chk"},
		{"listeners", "listeners_port_check"},
		{"regions", "regions_status_check"},
		// Пустые поля: pgconn отдаёт их, когда отказ не про ограничение таблицы.
		// Предикат обязан ответить «нет», а не совпасть сам с собой.
		{"", ""},
		{"", "_name_check"},
	}
	for _, c := range no {
		if nameform.IsConstraint(c.table, c.constraint) {
			t.Errorf("IsConstraint(%q, %q) = true, ожидалось false", c.table, c.constraint)
		}
	}
}
