// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple

import "testing"

// TestValidateTuple_ClassSubjectIsNotAHierarchySubject — класс субъектов модели
// (system_class: аноним, вошедший; kaname#838) не бывает субъектом отношения
// иерархии. Предок ресурса — проект или родитель, создатель — пользователь или
// сервисный аккаунт; класс же означает «всякий такой субъект», и как предок
// либо владелец он раздал бы доступ к ресурсу целому классу через иерархию,
// мимо выдачи прав, где грант перечислим, ограничен областью и отзываем.
//
// Строки отказа и законные близнецы стоят в одной таблице: близнец меняет ровно
// один факт — тип субъекта — и остаётся принятым, значит отказ даёт класс
// субъекта, а не форма записи, отношение или домен.
func TestValidateTuple_ClassSubjectIsNotAHierarchySubject(t *testing.T) {
	for _, tt := range []struct {
		name     string
		domain   string
		subject  string
		relation string
		object   string
		want     verdict
	}{
		{"class subject as project", "vpc", "system_class:anonymous", "project", "vpc_network:net1", refused},
		{"class subject as parent", "vpc", "system_class:authenticated", "parent", "vpc_network:net1", refused},
		{"class subject as owner", "vpc", "system_class:anonymous", "owner", "vpc_network:net1", refused},
		{"class userset subject as owner", "vpc", "system_class:authenticated#member", "owner", "vpc_network:net1", refused},
		{"class subject padded with spaces", "vpc", "  system_class:anonymous  ", "project", "vpc_network:net1", refused},
		// При неизвестном домене связывание по домену отключено — отказ обязан
		// стоять и там: он не зависит от того, чей объект.
		{"class subject under unknown domain", "", "system_class:anonymous", "project", "vpc_network:net1", refused},
		// Законные близнецы: тот же кортеж с субъектом иерархии.
		{"twin: project subject as project", "vpc", "project:prj1", "project", "vpc_network:net1", accepted},
		{"twin: project subject under unknown domain", "", "project:prj1", "project", "vpc_network:net1", accepted},
		{"twin: parent subject as parent", "vpc", "vpc_network:net1", "parent", "vpc_subnet:sub1", accepted},
		{"twin: user subject as owner", "vpc", "user:usr1", "owner", "vpc_network:net1", accepted},
		// Тип, лишь начинающийся с имени класса, классом не является.
		{"twin: type with class-like prefix", "vpc", "system_classic:x", "owner", "vpc_network:net1", accepted},
		// Пара публичного чтения — иное отношение, и набор иерархии её не
		// трогает: аноним принимается её вторым входным словом (corelib#116),
		// вошедший не принимается ни там, ни здесь.
		{"public read pair unchanged", "registry", "user:*", "v_get", "registry_repository:reg1/app", accepted},
		{"anonymous class on public read", "registry", "system_class:anonymous", "v_get", "registry_repository:reg1/app", accepted},
		{"authenticated class on public read", "registry", "system_class:authenticated", "v_get", "registry_repository:reg1/app", refused},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := verdictOf(ValidateTuple(tt.domain, tt.subject, tt.relation, tt.object))
			if got != tt.want {
				t.Fatalf("ValidateTuple(%q,%q,%q,%q) = %v; want %v",
					tt.domain, tt.subject, tt.relation, tt.object, got, tt.want)
			}
		})
	}
}
