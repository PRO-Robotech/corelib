// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple_test

import (
	"errors"
	"testing"

	"github.com/PRO-Robotech/corelib/authz/proxytuple"
	"github.com/PRO-Robotech/corelib/platformmodules"
)

// vocabulary_test.go — «чей это тип» отвечает СЛОВАРЬ, а не приставка имени
// (задача #1885).
//
// # Что именно изменилось и что НЕ изменилось
//
// Домен объекта, который модулю позволено писать, берётся у словаря имён модулей
// (pkg/platformmodules), а не равен имени вызывающего по соглашению. У всех
// пяти сегодняшних эмитентов объявленный домен совпадает с их коротким именем,
// поэтому НИ ОДИН живой вердикт не меняется — и это надо доказывать обеими
// сторонами, иначе проверка, отнявшая живое право, зеленела бы на всём
// отвергающем.
//
// # Пара обязательна: балансировщик — тот случай, ради которого словарь заведён
//
// У него три написания различны: служба `nlb`, модуль каталога `loadbalancer`,
// тип модели `nlb_listener`. Наивный предикат «имя службы == модуль каталога»
// не совпал бы у него НИКОГДА и отнял бы три живых типа, а наблюдаемо это было
// бы как «ресурс создан, доступа нет».

func TestOwnDomainTupleIsStillAccepted(t *testing.T) {
	// Обе стороны пары: своя тройка принимается, чужая отвергается.
	for _, c := range []struct {
		name    string
		caller  string
		object  string
		accept  bool
		comment string
	}{
		{name: "балансировщик пишет на свой тип", caller: "nlb",
			object: "nlb_listener:lst1", accept: true},
		{name: "балансировщик пишет на свою целевую группу", caller: "nlb",
			object: "nlb_target_group:tg1", accept: true},
		{name: "сеть пишет на свой тип", caller: "vpc",
			object: "vpc_network:net1", accept: true},
		{name: "сеть пишет на тип балансировщика", caller: "vpc",
			object: "nlb_listener:lst1", accept: false},
		{name: "балансировщик пишет на тип сети", caller: "nlb",
			object: "vpc_network:net1", accept: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := proxytuple.ValidateTuple(c.caller, "user:usr1", "owner", c.object)
			switch {
			case c.accept && err != nil:
				t.Fatalf("своя тройка отвергнута — проверка отняла живое право: %v", err)
			case !c.accept && !errors.Is(err, proxytuple.ErrRefused):
				t.Fatalf("чужая тройка принята: err=%v", err)
			}
		})
	}
}

// TestCallerOutsideTheVocabularyIsRefused — модуль, которого словарь не знает,
// отвергается, а не проверяется приставкой собственного имени.
//
// До #1885 «чей это тип» отвечало соглашение об именовании: любой вызывающий
// получал право на типы с приставкой своего же имени, в том числе тот, о ком
// платформа ничего не объявляла. Это не дыра сегодня (таких типов в модели нет),
// но оно делало совпадение имён УСЛОВИЕМ работы: тип, чьё имя в модели не
// начинается с имени его модуля, был невыразим.
func TestCallerOutsideTheVocabularyIsRefused(t *testing.T) {
	if _, known := platformmodules.ObjectDomainOfService("gateway"); known {
		t.Fatal("предпосылка пробы исчезла: `gateway` объявлен словарём — возьмите " +
			"другое необъявленное имя, иначе проба судит не тот случай")
	}

	err := proxytuple.ValidateTuple("gateway", "user:usr1", "owner", "gateway_thing:t1")
	if !errors.Is(err, proxytuple.ErrRefused) {
		t.Fatalf("необъявленный модуль принят по совпадению приставки со своим именем: "+
			"err=%v", err)
	}
}

// TestModuleWithNoObjectTypesOfItsOwnIsRefused — объявленный модуль, у которого
// СВОИХ типов объекта нет, отвергается тоже.
//
// Пустой домен в словаре означает «модель не объявляет ни одного типа этого
// модуля» — то есть писать ему не на что. Схлопни мы его с «не знаю такого», и
// проверка приставкой вернулась бы через заднюю дверь: `HasPrefix(obj, "_")`
// молча отвергает всё, но по НЕ ТОЙ причине, а первый же тип домена сделал бы
// его достижимым без единого решения.
func TestModuleWithNoObjectTypesOfItsOwnIsRefused(t *testing.T) {
	domain, known := platformmodules.ObjectDomainOfService("geo")
	if !known || domain != "" {
		t.Skipf("предпосылка пробы изменилась: у geo объявлен домен типов %q — "+
			"перепишите пробу под модуль, у которого их всё ещё нет", domain)
	}

	err := proxytuple.ValidateTuple("geo", "user:usr1", "owner", "geo_zone:z1")
	if !errors.Is(err, proxytuple.ErrRefused) {
		t.Fatalf("модуль без собственных типов объекта принят: err=%v", err)
	}
}

// TestUnknownCallerDomainStillSkipsTheDomainClause — граница названа честно.
//
// Пустой callerDomain (домен неизвестен) привязку к домену НЕ включает, и это
// поведение задачей не менялось: его держат набор отношений, сужение субъекта
// публикации и запретительный набор типов. Без этой пробы следующий читатель
// принял бы словарь за полную защиту от чужого типа.
func TestUnknownCallerDomainStillSkipsTheDomainClause(t *testing.T) {
	if err := proxytuple.ValidateTuple("", "user:usr1", "owner", "vpc_network:net1"); err != nil {
		t.Fatalf("пустой домен вызывающего перестал пропускать привязку к домену: %v", err)
	}
	if err := proxytuple.ValidateTuple("", "user:usr1", "owner", "iam_role:rol1"); !errors.Is(
		err, proxytuple.ErrRefused) {
		t.Fatalf("запретительный набор перестал держать границу при пустом домене: %v", err)
	}
}
