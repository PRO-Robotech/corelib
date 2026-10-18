// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple

import "testing"

// TestValidateTuple_PublicReadAcceptsTheAnonymousClass — публикация объекта для
// чтения без входа принимает второе входное слово, класс `system_class:anonymous`
// (corelib#116, kaname#893). Владелец, у которого хранимая публикация и плоскость
// данных уже говорят классом, не обязан называть её подстановкой `user:*`.
//
// Каждая строка отказа меняет против принятой ровно один факт: класс
// (`authenticated` вместо `anonymous`), форму субъекта (множество, подстановка
// класса), отношение либо тип объекта. Значит, принимается ровно пара «аноним +
// `v_get` на типе из закрытого списка», а не «любой класс на чтении».
func TestValidateTuple_PublicReadAcceptsTheAnonymousClass(t *testing.T) {
	const repo = "registry_repository:reg53eeeg3578y4ah0q9/team/app"
	for _, tt := range []struct {
		name, domain, subject, relation, object string
		want                                    verdict
	}{
		{"anonymous class publishes own repository", "registry", "system_class:anonymous", "v_get", repo, accepted},
		{"anonymous class padded with spaces", "registry", "  system_class:anonymous  ", "v_get", repo, accepted},
		{"anonymous class under unknown domain", "", "system_class:anonymous", "v_get", repo, accepted},
		// Переходное слово принимается по-прежнему.
		{"twin: wildcard user still publishes", "registry", "user:*", "v_get", repo, accepted},
		// Один факт против принятой строки — и отказ.
		{"authenticated class is not a publication", "registry", "system_class:authenticated", "v_get", repo, refused},
		{"anonymous userset is not a publication", "registry", "system_class:anonymous#member", "v_get", repo, refused},
		{"class wildcard is not a publication", "registry", "system_class:*", "v_get", repo, refused},
		{"unknown class is not a publication", "registry", "system_class:everyone", "v_get", repo, refused},
		{"anonymous class but a list verb", "registry", "system_class:anonymous", "v_list", repo, refused},
		{"anonymous class but a write verb", "registry", "system_class:anonymous", "v_update", repo, refused},
		{"anonymous class but a tier relation", "registry", "system_class:anonymous", "viewer", repo, refused},
		{"anonymous class on a registry-level object", "registry", "system_class:anonymous", "v_get", "registry_registry:reg1", refused},
		{"anonymous class on a foreign domain", "registry", "system_class:anonymous", "v_get", "vpc_network:net1", refused},
		{"anonymous class on own non-publishable type", "vpc", "system_class:anonymous", "v_get", "vpc_network:net1", refused},
		{"anonymous class on the cluster", "registry", "system_class:anonymous", "v_get", "cluster:cluster_root", refused},
		{"anonymous class under unknown domain on a non-publishable type", "", "system_class:anonymous", "v_get", "vpc_network:net1", refused},
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

// TestPublicReadGrant_AcceptanceAndRemovalAgreeOnBothWords — снятие судится тем
// же предикатом, что приём: публикация, принятая одним из двух слов, узнаётся
// как «наша» и при сносе объекта. Иначе принятое не снималось бы, и чтение без
// входа пережило бы объект.
func TestPublicReadGrant_AcceptanceAndRemovalAgreeOnBothWords(t *testing.T) {
	for _, tt := range []struct {
		subject string
		want    bool
	}{
		{"user:*", true},
		{"system_class:anonymous", true},
		{" system_class:anonymous ", true},
		{"system_class:authenticated", false},
		{"system_class:anonymous#member", false},
		{"user:usr00000000000000a1", false},
	} {
		if got := IsPublicReadGrant(tt.subject, "v_get"); got != tt.want {
			t.Errorf("IsPublicReadGrant(%q, v_get) = %v; want %v", tt.subject, got, tt.want)
		}
		if got := IsProxyWritable(tt.subject, "v_get"); got != tt.want {
			t.Errorf("IsProxyWritable(%q, v_get) = %v; want %v", tt.subject, got, tt.want)
		}
		if IsPublicReadGrant(tt.subject, "v_list") {
			t.Errorf("IsPublicReadGrant(%q, v_list) = true; публикация — только v_get", tt.subject)
		}
	}
}

// TestPublicReadSubjects_ClosedList — перечень входных слов закрыт и выдаётся
// копией: перепись и гейты потребителя берут его здесь, а не выписывают.
func TestPublicReadSubjects_ClosedList(t *testing.T) {
	got := PublicReadSubjects()
	want := []string{PublicReadSubject, PublicReadClassSubject}
	if len(got) != len(want) {
		t.Fatalf("PublicReadSubjects() = %q; want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PublicReadSubjects() = %q; want %q", got, want)
		}
	}
	got[0] = "mutated"
	if PublicReadSubjects()[0] != PublicReadSubject {
		t.Fatal("PublicReadSubjects() отдаёт общий срез: правка потребителя меняет правило")
	}
	for _, s := range want {
		if !IsPublicReadGrant(s, string(PublicReadRelation)) {
			t.Errorf("слово перечня %q не признано предикатом приёма", s)
		}
	}
}
