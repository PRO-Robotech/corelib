// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicecontract_test

// serviceidentity_axis_test.go — ось звена идентичности служб (NTF-1 Р2, З13,
// CX1-34 (в)): перечень и таблица приходят полем дескриптора, а не аргументом
// сборки. Необъявленная ось — отказ; «звена нет» пишется ровно одним способом —
// изъятием с причиной, а не пустым значением.

import (
	"testing"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

func notifyIdentity(t *testing.T) grpcsrv.ServiceIdentity {
	t.Helper()
	id, err := grpcsrv.NewServiceIdentity(
		[]string{"/corelib.subscription.InternalSubscriptionService/Subscribe"},
		map[string]grpcsrv.ServiceName{"spiffe://kacho.cloud/ns/kacho/sa/kacho-notify": "notify"})
	if err != nil {
		t.Fatalf("звено: %v", err)
	}
	return id
}

func TestServiceIdentityAxisMustBeDeclared(t *testing.T) {
	s := lawful()
	s.ServiceIdentity = servicecontract.Axis[grpcsrv.ServiceIdentity]{}
	refuses(t, s, "ServiceIdentity")
}

func TestServiceIdentityAxisAcceptsAValueAndAReason(t *testing.T) {
	s := lawful()
	s.ServiceIdentity = servicecontract.Value(notifyIdentity(t))
	if _, err := servicecontract.New(s); err != nil {
		t.Fatalf("согласное звено отвергнуто: %v", err)
	}
	s.ServiceIdentity = servicecontract.NotApplicable[grpcsrv.ServiceIdentity]("службы-подписчики сюда не ходят")
	if _, err := servicecontract.New(s); err != nil {
		t.Fatalf("изъятие с причиной отвергнуто: %v", err)
	}
}

// Пустое звено значением — второй способ записать «звена нет». Он отвергается с
// указанием на единственный законный.
func TestEmptyServiceIdentityValueIsRefused(t *testing.T) {
	empty, err := grpcsrv.NewServiceIdentity(nil, nil)
	if err != nil {
		t.Fatalf("пустое звено: %v", err)
	}
	s := lawful()
	s.ServiceIdentity = servicecontract.Value(empty)
	refuses(t, s, "ServiceIdentity", "NotApplicable")
}
