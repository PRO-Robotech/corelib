// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

import (
	"testing"

	"github.com/PRO-Robotech/corelib/observability"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// TestPostureOf_UnacceptedDescriptorYieldsNoRecord — дескриптор, который не
// проходил конструктор (литерал) либо nil, носитель не исполнит, и самоотчёт о
// нём не пишется: форма вне перечня, запись отвергает её конструктор. Близнец —
// принятые дескрипторы трёх форм (TestX5_PostureOfDerivesTheListenerForm…).
func TestPostureOf_UnacceptedDescriptorYieldsNoRecord(t *testing.T) {
	var literal servicecontract.Descriptor
	for name, d := range map[string]*servicecontract.Descriptor{"литерал": &literal, "nil": nil} {
		form, none := PostureOf(d)
		if _, err := observability.NewBootPosture(observability.BootPosture{
			Service: "kacho-x5probe", ListenerForm: form, NoServedServices: none,
		}); err == nil {
			t.Fatalf("%s: запись из PostureOf непринятого дескриптора принята — самоотчёт описал бы "+
				"процесс, который носитель не поднимет", name)
		}
	}
}

// TestX5_InternalOnlyRefusesAMissingInternalRegistrar — форма без регистратора
// единственного слушателя: отказ старта, а не слушатель без служимого. Дельта к
// близнецу (TestX5_InternalOnlyCarrierJudges…) одна — внутренний регистратор nil.
func TestX5_InternalOnlyRefusesAMissingInternalRegistrar(t *testing.T) {
	f := newX5Fixture(t)
	d, err := servicecontract.New(f.internalOnlySpec(freeAddr(t)))
	if err != nil {
		t.Fatalf("законный дескриптор формы отвергнут: %v", err)
	}
	if serr := Serve(t.Context(), d, nil, nil); serr == nil {
		t.Fatal("носитель формы без внутреннего регистратора вернул nil")
	} else {
		t.Logf("красный: %v", serr)
	}
}
