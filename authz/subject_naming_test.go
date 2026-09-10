// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import "testing"

// subject_naming_test.go — ПОЧЕМУ пара «тип, идентификатор» не стала именем
// субъекта (kacho#1463).
//
// # Зачем отдельный исход, если [TenantSubject] уже отвечает «не субъект»
//
// Отвечает — ОДНИМ значением на два несравнимых события. Тот, кто по имени
// субъекта что-то находит, встречает обе строки: выданную ГРУППЕ (норма —
// предпочтительная форма выдачи, потоков по группе не заводится) и строку, чьё
// имя производитель потерял (дефект — отзыв не доедет до потока). Считая их
// одним числом, наблюдатель получает величину, ненулевую в штатной работе, и
// повесить на неё тревогу нельзя: дефект остаётся в шуме нормы.
//
// Замер, из-за которого проба написана: `bool` равен `false` на ПЯТИ входах,
// покрывающих ОБА исхода, — то есть вторым значением эти два не разделяются.

// TestNameTenantSubjectSeparatesUsersetKindFromUnnameable — разделяет ли исход
// норму и дефект.
//
// Обе стороны утверждаются в одной таблице намеренно: проба, спрашивающая
// только про группу, зеленела бы на устройстве, которое ВСЁ зовёт usersetом.
func TestNameTenantSubjectSeparatesUsersetKindFromUnnameable(t *testing.T) {
	cases := []struct {
		name        string
		typ, id     string
		wantSubject string
		wantNaming  SubjectNaming
	}{
		{"человек назван", "user", "usr00000000000000001", "user:usr00000000000000001", SubjectNamed},
		{"служебная учётка названа", "service_account", "sva00000000000000001", "service_account:sva00000000000000001", SubjectNamed},

		// НОРМА: тип назван, он принадлежит словарю продукта, но адресуемым
		// принципалом не является — удостоверение предъявляет участник, а не
		// множество.
		{"группа — норма, а не потеря", "group", "grp00000000000000001", "", SubjectUserset},

		// ДЕФЕКТ: имени нет, хотя оно должно было быть.
		{"типа нет вовсе", "", "usr00000000000000001", "", SubjectUnnameable},
		{"тип вне словаря продукта", "nonsense", "usr00000000000000001", "", SubjectUnnameable},
		{"тип годен, идентификатор пуст", "user", "", "", SubjectUnnameable},
		{"идентификатор несёт служебный знак", "user", "usr:alice", "", SubjectUnnameable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			subject, naming := NameTenantSubject(c.typ, c.id)
			if subject != c.wantSubject {
				t.Errorf("имя %q, ожидалось %q", subject, c.wantSubject)
			}
			if naming != c.wantNaming {
				t.Errorf("исход %v, ожидался %v (тип %q, идентификатор %q)", naming, c.wantNaming, c.typ, c.id)
			}
		})
	}
}

// TestTenantSubjectKeepsItsContract — законный близнец: у старой двери исход
// прежний.
//
// Она зовётся отовсюду, где спрашивают право, и её ответ обязан остаться тем же
// после того, как причина отказа стала называться: расширение наблюдения не
// вправе поменять решение о доступе.
func TestTenantSubjectKeepsItsContract(t *testing.T) {
	cases := []struct {
		typ, id string
		wantOK  bool
	}{
		{"user", "usr00000000000000001", true},
		{"service_account", "sva00000000000000001", true},
		{"group", "grp00000000000000001", false},
		{"", "usr00000000000000001", false},
		{"nonsense", "usr00000000000000001", false},
		{"user", "", false},
		{"user", "usr:alice", false},
	}
	for _, c := range cases {
		subject, ok := TenantSubject(c.typ, c.id)
		if ok != c.wantOK {
			t.Errorf("TenantSubject(%q,%q) отдал ok=%v, ожидалось %v", c.typ, c.id, ok, c.wantOK)
		}
		if ok != (subject != "") {
			t.Errorf("TenantSubject(%q,%q): ok=%v при имени %q — согласие и имя разошлись",
				c.typ, c.id, ok, subject)
		}
	}
}
