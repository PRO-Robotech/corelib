// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

// hide_existence_parity_internal_test.go — форма нейтрального отказа.
//
// # Что отсюда УЕХАЛО и почему (задача #2532, класс 2)
//
// Здесь стоял страж паритета двух таблиц — фундамента и края, — и он читал каталог
// края относительным путём наружу пакета. Его предмет есть паритет ДВУХ ДЕРЕВЬЕВ, и
// одно из них принадлежит платформе: фундамент, уехав в свой репозиторий, перестаёт
// видеть край и отвечал бы «пакет не прочитан» — «не выполнилось», поданное как
// красное.
//
// Страж переехал в платформу целиком, вместе со своим разбором и своей инъекцией:
// `internal/repohygiene/hideexistenceparity_test.go`. Там же он усилен третьей
// стороной — сверкой разобранного объявления с ЖИВЫМ значением, которая здесь была
// не нужна (страж жил в пакете карты и брал её значением напрямую).
//
// Осталось то, что о СВОЕЙ функции и ни одного чужого дерева не читает.

import (
	"testing"
)

// TestHideExistenceMessage_ShapeOfTheFallback — the two cases where byte-identity
// is impossible must yield the least-informative answer, never the internal type.
func TestHideExistenceMessage_ShapeOfTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		objectType string
		objectID   string
		want       string
	}{
		{"known type and id", "vpc_subnet", "sub00000000000000abc", "Subnet sub00000000000000abc not found"},
		{"unknown type", "something_new", "xyz00000000000000abc", "not found"},
		{"wildcard id", "vpc_subnet", "*", "not found"},
		{"absent id", "vpc_subnet", "", "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hideExistenceMessage(tc.objectType, tc.objectID); got != tc.want {
				t.Fatalf("hideExistenceMessage(%q,%q) = %q; want %q", tc.objectType, tc.objectID, got, tc.want)
			}
		})
	}
}
