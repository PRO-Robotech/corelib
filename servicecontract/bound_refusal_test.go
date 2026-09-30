// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// bound_refusal_test.go — привязки серверов к экземплярам (З14, [servicecontract.Spec.Bound]).
//
// Здесь судится то, что есть свойство САМОГО дескриптора: форма привязки и её
// единственность по типу. Метод формы ScopeBound без привязки своего типа
// требует карты прав и судится носителем при её выводе (`catalogderive.Bind`).
package servicecontract_test

import (
	"testing"

	"github.com/PRO-Robotech/corelib/servicecontract"
)

const feedType servicecontract.ObjectType = "notification_feed"

// TestBoundLawfulBindingIsAcceptedAndHandedOut — законный близнец: одна
// привязка принимается, и носитель получает ровно её.
func TestBoundLawfulBindingIsAcceptedAndHandedOut(t *testing.T) {
	s := lawful()
	s.Bound = []servicecontract.Bound{{Type: feedType, ID: "notify-probe"}}
	d, err := servicecontract.New(s)
	if err != nil {
		t.Fatalf("законная привязка отвергнута — отрицания ниже вакуумны: %v", err)
	}
	got := d.Bindings()
	if len(got) != 1 || got[string(feedType)] != "notify-probe" {
		t.Fatalf("носитель получил не то, что объявлено: %v", got)
	}

	// Правка принятого после конструктора не меняет одобренного.
	s.Bound[0].ID = "other"
	if d.Bindings()[string(feedType)] != "notify-probe" {
		t.Fatalf("привязка принятого дескриптора изменилась правкой среза вызывающего")
	}
}

// TestBoundTwoBindingsOfOneTypeAreRefused — две привязки одного типа: какой
// экземпляр спрашивать — не решено никем. Отказ называет поле и тип.
func TestBoundTwoBindingsOfOneTypeAreRefused(t *testing.T) {
	s := lawful()
	s.Bound = []servicecontract.Bound{
		{Type: feedType, ID: "notify-probe"},
		{Type: feedType, ID: "notify-probe-b"},
	}
	refuses(t, s, "Bound", string(feedType))

	// Близнец: две привязки РАЗНЫХ типов законны.
	twin := lawful()
	twin.Bound = []servicecontract.Bound{
		{Type: feedType, ID: "notify-probe"},
		{Type: "probe_feed", ID: "notify-probe"},
	}
	if _, err := servicecontract.New(twin); err != nil {
		t.Fatalf("привязки разных типов отвергнуты: %v", err)
	}
}

// TestBoundMalformedBindingIsRefused — привязка, которой нельзя назвать объект
// модели, отвергается при старте, а не на каждом вызове.
func TestBoundMalformedBindingIsRefused(t *testing.T) {
	for name, b := range map[string]servicecontract.Bound{
		"empty type":     {Type: "", ID: "notify-probe"},
		"empty id":       {Type: feedType, ID: ""},
		"blank id":       {Type: feedType, ID: "  "},
		"separator":      {Type: feedType, ID: "notify:probe"},
		"userset":        {Type: feedType, ID: "notify#member"},
		"type separator": {Type: "notification:feed", ID: "notify-probe"},
	} {
		t.Run(name, func(t *testing.T) {
			s := lawful()
			s.Bound = []servicecontract.Bound{b}
			refuses(t, s, "Bound")
		})
	}
}

// TestBoundIsCarrierWiring — привязку читает только носитель, поэтому процесс,
// чей контур носитель не поднимает, принести её не может (О14, О15).
func TestBoundIsCarrierWiring(t *testing.T) {
	b := []servicecontract.Bound{{Type: feedType, ID: "notify-probe"}}

	own := ownContourSpec()
	own.Bound = b
	refuses(t, own, "Bound", "СОБСТВЕННЫЙ")

	nogrpc := noGRPCSpec()
	nogrpc.Bound = b
	refuses(t, nogrpc, "Bound")
}
