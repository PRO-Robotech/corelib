// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/subscription"
)

// dictionaryProbeJournal — владелец, у которого слово ХРАНИЛИЩА и слово ПРОВОДА
// различаются у КАЖДОГО вида, и видов трое.
//
// Трое, а не один: порядок объявлен частью контракта, и на одном элементе
// утверждение о порядке истинно при любой реализации. Различаются у каждого —
// потому что совпадение хотя бы у одного делает подмену одного слова другим
// невидимой ровно на нём (у соседнего живого владельца слово и тип совпадают у
// двух видов из трёх, и это и есть тот случай, когда дефект не виден на глаз).
func dictionaryProbeJournal() subscription.Journal {
	return subscription.Journal{
		Channel: "probe_outbox",
		Storage: subscription.Storage{
			Table:          "probe_outbox",
			PositionColumn: "sequence_no",
			KindColumn:     "resource_kind",
			IDColumn:       "resource_id",
			ChangeColumn:   "event_type",
			PayloadColumn:  "payload",
			ProjectColumn:  "project_id",
			Project:        subscription.ProjectInColumn,
			Retention:      subscription.RetainsEverything,
		},
		Mapping: subscription.Mapping{
			Kinds: map[string]subscription.Kind{
				// Слева — как строка записана в журнале владельца; справа — как
				// предмет зовёт модель прав. Слева нарочно ТРИ разных написания:
				// голое имя с заглавной, слово с чужим префиксом и слово, чей
				// префикс совпадает с доменом, но хвост — нет.
				"Instance":          {ObjectType: "probe_instance", Action: "probe.instances.list"},
				"nlb_load_balancer": {ObjectType: "probe_network_load_balancer", Action: "probe.balancers.list"},
				"target-group":      {ObjectType: "probe_target_group", Action: "probe.targetGroups.list"},
			},
			Changes: map[string]subscriptionv1.SubscriptionEvent_Change{
				"CREATED": subscriptionv1.SubscriptionEvent_CREATED,
			},
			State: func(subscription.Row) (*anypb.Any, subscription.StateAbsence, error) {
				return nil, subscription.StateNotProduced, nil
			},
		},
	}
}

// TestKindDictionaryIsTheObjectTypeVocabulary — словарь, который владелец
// объявляет клиенту, есть словарь ТИПОВ ОБЪЕКТА модели прав, а не слов его
// хранилища.
//
// Это и есть «написание одно»: производитель у слова ровно один — тот же, что
// решает вопрос о видимости строки. Второе написание завести некуда: поля под
// него в объявлении владельца НЕТ.
func TestKindDictionaryIsTheObjectTypeVocabulary(t *testing.T) {
	got := dictionaryProbeJournal().KindDictionary()

	// Порядок объявлен частью контракта: словарь живёт отображением, обход
	// отображения в Go случаен by construction, и клиент, ведущий состояние по
	// индексу, читал бы каждое открытие как смену словаря.
	want := []string{"probe_instance", "probe_network_load_balancer", "probe_target_group"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("словарь видов %q, ожидался %q (типы объекта, лексикографически)", got, want)
	}

	// Отрицательная половина той же пары: НИ ОДНО слово хранилища наружу не
	// выходит. Без неё утверждение выше зеленело бы на словаре, который отдаёт
	// оба написания сразу.
	for _, journalWord := range []string{"Instance", "nlb_load_balancer", "target-group"} {
		for _, k := range got {
			if k == journalWord {
				t.Fatalf("слово ХРАНИЛИЩА %q попало в словарь клиента: как владелец записал строку — его частное дело", journalWord)
			}
		}
	}
}

// TestKindDictionaryDeduplicatesOneObjectTypeWrittenByTwoJournalWords — два
// слова журнала об одном предмете дают ОДИН вид.
//
// Законный случай: владелец, писавший строку двумя словами (историческое и
// нынешнее), объявляет их обоими ключами одного типа. Клиенту это видеть незачем
// — он спрашивает про предмет, а не про то, чем владелец его записал.
func TestKindDictionaryDeduplicatesOneObjectTypeWrittenByTwoJournalWords(t *testing.T) {
	j := dictionaryProbeJournal()
	j.Mapping.Kinds["instance"] = subscription.Kind{ObjectType: "probe_instance", Action: "probe.instances.list"}

	got := j.KindDictionary()
	want := []string{"probe_instance", "probe_network_load_balancer", "probe_target_group"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("словарь видов %q, ожидался %q — второе слово журнала об одном предмете дало второй вид", got, want)
	}
}

// TestAcceptTakesTheWireWordAndRefusesTheJournalWord — ось `kinds` принимает то
// же слово, которое перечисляет словарь, и ТОЛЬКО его.
//
// Утверждаются обе стороны. Приём слова журнала был бы вторым способом сказать
// одно и то же — и ровно тем способом, о котором клиенту неоткуда узнать.
func TestAcceptTakesTheWireWordAndRefusesTheJournalWord(t *testing.T) {
	j := dictionaryProbeJournal()

	f, err := j.Accept(&subscriptionv1.SubscriptionRequest{Kinds: []string{"probe_instance"}})
	if err != nil {
		t.Fatalf("вид из собственного словаря владельца отвергнут: %v", err)
	}
	if !reflect.DeepEqual(f.Kinds, []string{"probe_instance"}) {
		t.Fatalf("принятое сужение по видам %q", f.Kinds)
	}

	_, err = j.Accept(&subscriptionv1.SubscriptionRequest{Kinds: []string{"Instance"}})
	if err == nil {
		t.Fatal("слово ХРАНИЛИЩА принято как вид — у одного предмета появилось бы два написания, и второе нигде не объявлено")
	}
}

// TestRefusalOfAnUnknownKindNamesTheDictionary — отказ восстанавливает следующий
// шаг вызывающего.
//
// Отказ, называющий только негодное значение, оставляет вызывающего там же,
// откуда он пришёл: единственным путём узнать годное остаётся перебор против
// этого самого отказа. Перечень берётся ТЕМ ЖЕ вызовом, каким сервер отвечает в
// служебном сообщении, — разойтись им негде.
func TestRefusalOfAnUnknownKindNamesTheDictionary(t *testing.T) {
	j := dictionaryProbeJournal()

	_, err := j.Accept(&subscriptionv1.SubscriptionRequest{Kinds: []string{"probe_volume"}})
	if err == nil {
		t.Fatal("неизвестный вид принят")
	}
	msg := err.Error()
	if !strings.Contains(msg, "probe_volume") {
		t.Errorf("отказ не называет отвергнутое значение: %q", msg)
	}
	for _, kind := range j.KindDictionary() {
		if !strings.Contains(msg, kind) {
			t.Errorf("отказ не называет известный вид %q — вызывающему опять некуда пойти, кроме перебора: %q", kind, msg)
		}
	}
}
