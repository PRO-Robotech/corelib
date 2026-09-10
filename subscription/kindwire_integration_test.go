// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
)

// TestOpenedCarriesTheKindDictionary — словарь видов ПОЛУЧАЕМ клиентом, и
// получаем он его тем самым сообщением, которое приходит первым и всегда.
//
// Проба подписывается, НЕ называя ни одного вида, — то есть идёт ровно тем
// путём, каким словарь читает клиент, который не знает ни одного вида. До этого
// поля такого пути не существовало вовсе: единственным способом узнать годное
// значение был перебор против отказа на живой ручке.
func TestOpenedCarriesTheKindDictionary(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{})

	// Типы объекта, не слова хранилища, и в возрастающем порядке: порядок
	// объявлен частью контракта у поля `known_kinds`.
	want := []string{"vpc_network", "vpc_subnet"}
	if got := sb.opened.GetKnownKinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("словарь видов в служебном сообщении %q, ожидался %q", got, want)
	}
}

// TestWireKindIsTheObjectTypeNotTheJournalWord — на проводе едет ТО ЖЕ слово,
// которое перечисляет словарь и принимает ось.
//
// Три утверждения об одном предмете, и каждое отдельно необходимо:
//
//	ось принимает слово провода       — иначе словарь называет то, что не принять;
//	отбор доезжает до слова хранилища — иначе принятая ось не сужает ничем;
//	событие несёт слово провода       — иначе клиент получает третье написание,
//	                                    которого нет ни в словаре, ни в оси.
func TestWireKindIsTheObjectTypeNotTheJournalWord(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	s.emit(t, "Subnet", "sub00000000000000001", "CREATED", "prj-a")

	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Kinds: []string{"vpc_network"},
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	if got := sb.opened.GetHonoredFilters(); len(got) != 1 || got[0] != "kinds" {
		t.Fatalf("честно отобранные оси %v, ожидалась одна — kinds", got)
	}

	got := recvEvents(t, sb, 1)
	if got[0].GetResourceId() != "net00000000000000001" {
		t.Fatalf("отбор по виду провода не доехал до слова хранилища: пришло %q", got[0].GetResourceId())
	}
	if got[0].GetKind() != "vpc_network" {
		t.Fatalf("событие несёт вид %q — это слово ХРАНИЛИЩА владельца; клиенту едет тип объекта", got[0].GetKind())
	}
	// Отрицательная половина: подсеть под сужение не попала. Без неё
	// утверждение выше зеленело бы на сервере, который не сужает вовсе и просто
	// отдал первое событие журнала.
	requireQuiet(t, sb)
}
