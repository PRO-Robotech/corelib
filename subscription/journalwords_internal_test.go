// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// journalwords_internal_test.go — проба ВНУТРИ пакета, и это её единственная
// причина здесь быть.
//
// Перевод вида провода в слова хранилища не экспортируется намеренно: слово
// владельца наружу не выходит, и вызывающему его знать нечем и незачем.
// Открывать его ради пробы значило бы завести в прод-коде поверхность, у которой
// нет ни одного потребителя, кроме пробы, — то есть чинить наблюдаемость ценой
// контракта.
package subscription

import (
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
)

// TestEveryAcceptedKindTranslatesToAtLeastOneJournalWord — свойство, на котором
// молча стоит путь чтения.
//
// Отбор по видам уходит в запрос ПЕРЕВЕДЁННЫМ в слова хранилища. Верни перевод
// пусто — запрос отобрал бы по пустому множеству, и подписка открылась бы и
// замолчала навсегда: ни отказа, ни пропуска в нумерации. Ветки на этот случай в
// коде НЕТ намеренно — она недостижима, — поэтому недостижимость утверждается
// пробой, а не документируется мёртвым `if`.
//
// Утверждается по КАЖДОМУ виду словаря: перевод идёт обходом отображения, и вид,
// у которого он вырождается, был бы ровно тем, о котором проба на одном элементе
// не сказала бы ничего. Плюс обратная половина — переведённое слово отвечает
// ИМЕННО этому виду, а не соседнему.
func TestEveryAcceptedKindTranslatesToAtLeastOneJournalWord(t *testing.T) {
	j := Journal{
		Channel: "probe_outbox",
		Storage: Storage{
			Table: "probe_outbox", PositionColumn: "sequence_no",
			KindColumn: "resource_kind", IDColumn: "resource_id",
			ChangeColumn: "event_type", PayloadColumn: "payload",
			Project: ProjectAbsent, Retention: RetainsEverything,
		},
		Mapping: Mapping{
			Kinds: map[string]Kind{
				// Слева слово хранилища, справа слово провода. Различаются у
				// каждого вида, и два слова слева отвечают одному виду справа —
				// законный случай исторического написания.
				"Instance":          {ObjectType: "probe_instance", Action: "probe.instances.list"},
				"instance":          {ObjectType: "probe_instance", Action: "probe.instances.list"},
				"nlb_load_balancer": {ObjectType: "probe_balancer", Action: "probe.balancers.list"},
			},
			Changes: map[string]subscriptionv1.SubscriptionEvent_Change{
				"CREATED": subscriptionv1.SubscriptionEvent_CREATED,
			},
			State: func(Row) (*anypb.Any, StateAbsence, error) { return nil, StateNotProduced, nil },
		},
	}
	if err := j.Validate(); err != nil {
		t.Fatalf("объявление пробы отвергнуто общим судьёй: %v", err)
	}

	for _, kind := range j.KindDictionary() {
		f, err := j.Accept(&subscriptionv1.SubscriptionRequest{Kinds: []string{kind}})
		if err != nil {
			t.Fatalf("вид %q из собственного словаря отвергнут: %v", kind, err)
		}
		words := j.journalWords(f.Kinds)
		if len(words) == 0 {
			t.Fatalf("вид %q принят, но в слова хранилища не переводится: отбор ушёл бы по пустому "+
				"множеству, и подписка замолчала бы навсегда", kind)
		}
		for _, w := range words {
			if got := j.Mapping.Kinds[w].ObjectType; got != kind {
				t.Fatalf("вид %q перевёлся в слово %q, которое отвечает другому виду %q", kind, w, got)
			}
		}
	}

	// Два слова хранилища об одном виде переводятся ОБА: иначе историческая
	// строка молча выпала бы из отбора.
	if got := j.journalWords([]string{"probe_instance"}); len(got) != 2 {
		t.Fatalf("вид с двумя словами хранилища перевёлся в %v — историческая строка выпала бы из отбора", got)
	}
}
