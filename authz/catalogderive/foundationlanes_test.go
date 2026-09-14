// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive_test

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/PRO-Robotech/corelib/authz/catalogderive"

	// Контракты фундамента линкуются НАМЕРЕННО: перечень полос спрашивает о
	// СВОИХ методах, и без их дескрипторов проба мерила бы пустой реестр.
	_ "github.com/PRO-Robotech/corelib/api/corelib/operation"
	_ "github.com/PRO-Robotech/corelib/api/corelib/subscription"
)

// TestFoundationLanesAllStillHaveASubject — самоистечение перечня полос.
//
// # Осей у самоистечения ДВЕ, и они находят РАЗНОЕ
//
//  1. метод записи в двоичном НЕ РЕЗОЛВИТСЯ — контракт переименован, снят либо
//     не слинкован. Запись пережила предмет и молча наследует следующую слепую
//     зону;
//  2. метод записи ВЕРНУЛ СЕБЕ разметку в контракте — два объявления об одном
//     предмете. Это опаснее первого: оба объявления выглядят исправными, а
//     расходятся молча, и расходится то, которое не читают.
//
// # Перепись печатается ВСЕГДА
//
// «Записей 3, резолвнуто 3» и «записей 3, резолвнуто 2» — разные вердикты, и
// без второго числа отказ по оси 1 неотличим от усечённого обхода.
func TestFoundationLanesAllStillHaveASubject(t *testing.T) {
	methods := catalogderive.FoundationLaneMethods()
	if len(methods) == 0 {
		t.Fatal("перечень полос пуст — предикат снятия ничего не читает, и проба вакуумна")
	}

	resolved := 0
	for _, full := range methods {
		md, err := methodDescriptor(full)
		if err != nil {
			t.Errorf("запись перечня %q больше не имеет предмета: %v.\n"+
				"Запись, которой нечего объявлять, — находка, а не наследство: снимите её "+
				"вместе с методом либо поправьте имя", full, err)
			continue
		}
		resolved++

		// Ось 2: разметка в контракте ВЕРНУЛАСЬ.
		//
		// Спрашивается ИСХОД чтения, а не наличие опций в тексте: `AnnotationsOf`
		// сам отвергает метод, несущий оба объявления, и его отказ — это и есть
		// предмет проверки. Проверка по тексту контракта читала бы другой
		// источник и разошлась бы с читателем.
		if _, err := catalogderive.AnnotationsOf(md); err != nil {
			t.Errorf("запись перечня %q и разметка контракта объявляют одно и то же: %v", full, err)
		}
	}

	t.Logf("перепись: записей перечня %d, резолвнуто методов %d", len(methods), resolved)
	if resolved != len(methods) {
		t.Fatalf("резолвнуто %d из %d — вердикт вынесен по УСЕЧЁННОМУ обходу", resolved, len(methods))
	}
}

// TestFoundationLaneReachesTheMap — полоса перечня доезжает до карты прав.
//
// Проба существует затем, чтобы «запись объявлена» не подменяло «запись
// применена»: перечень, который никто не читает, зеленел бы у гейта выше и не
// давал бы методу ни строки.
func TestFoundationLaneReachesTheMap(t *testing.T) {
	m, err := catalogderive.Derive("corelib.operation", "corelib.subscription")
	if err != nil {
		t.Fatalf("карта не выводится: %v", err)
	}

	get, ok := m["/corelib.operation.OperationService/Get"]
	if !ok {
		t.Fatal("опрос операции отсутствует в карте — метод получил бы отказ как НЕОПИСАННЫЙ")
	}
	if !get.Public {
		t.Errorf("опрос операции обязан быть полосой без вопроса к модели прав; получено %+v", get)
	}

	sub, ok := m["/corelib.subscription.InternalSubscriptionService/Subscribe"]
	if !ok {
		t.Fatal("подписка отсутствует в карте")
	}
	if !sub.ScopeFiltered {
		t.Errorf("подписка обязана быть пообъектно сужаемой; получено %+v", sub)
	}
	if sub.Permission != "platform.subscription.subscribe" {
		t.Errorf("право подписки = %q, ждали platform.subscription.subscribe", sub.Permission)
	}
	t.Logf("перепись: методов в карте %d", len(m))
}

// TestFoundationLanesInjection — способность гейта выше УПАСТЬ, доказанная
// инъекцией по КАЖДОЙ оси, с законным близнецом.
//
// Инъекция подаётся ФУНКЦИИ, а не дереву: предмет проверки — резолв записи и
// чтение разметки, и оба выразимы на синтетическом входе. Дерево при этом
// остаётся нетронутым, поэтому инъекция не может уронить соседний контроль
// (`testing.md` §«Гейт на класс», п. 2в).
func TestFoundationLanesInjection(t *testing.T) {
	// Ось 1, ДЕФЕКТ: имя, которого в двоичном нет.
	if _, err := methodDescriptor("/corelib.operation.OperationService/NoSuchMethod"); err == nil {
		t.Error("ось 1 слепа: несуществующий метод резолвится — гейт не упал бы на записи без предмета")
	}
	// Ось 1, ЗАКОННЫЙ БЛИЗНЕЦ: настоящее имя резолвится.
	if _, err := methodDescriptor("/corelib.operation.OperationService/Get"); err != nil {
		t.Errorf("ось 1 ложно срабатывает: настоящий метод не резолвится: %v", err)
	}

	// Ось 2, ДЕФЕКТ: метод перечня, которому вернули разметку в контракте.
	//
	// Нейтральный дескриптор даёт ровно это: у его метода разметка ЕСТЬ, и
	// запись перечня ему подставляется здесь же — то есть воспроизводится
	// «два объявления об одном предмете», не трогая настоящих контрактов.
	md, err := methodDescriptor(probeGet)
	if err != nil {
		t.Fatalf("нейтральный дескриптор не резолвится: %v", err)
	}
	if _, ok := catalogderive.FoundationLaneOf(probeGet); ok {
		t.Fatal("предпосылка инъекции нарушена: у метода пробы уже есть запись перечня")
	}
	restore := catalogderive.SetFoundationLaneForTest(probeGet, catalogderive.FoundationLane{
		Permission: catalogderive.ExemptPermission, ExemptReason: "ИНЪЕКЦИЯ",
		Why: "инъекция оси 2",
	})
	_, err = catalogderive.AnnotationsOf(md)
	restore()
	if err == nil {
		t.Error("ось 2 слепа: метод несёт И разметку, И запись перечня, а чтение это приняло — " +
			"два объявления об одном предмете разошлись бы молча")
	} else if !strings.Contains(err.Error(), "перечня") {
		t.Errorf("ось 2 назвала предмет неверно: %v", err)
	}

	// Ось 2, ЗАКОННЫЙ БЛИЗНЕЦ: тот же метод БЕЗ записи перечня читается молча.
	if _, err := catalogderive.AnnotationsOf(md); err != nil {
		t.Errorf("ось 2 ложно срабатывает: метод с одной лишь разметкой отвергнут: %v", err)
	}
}

// methodDescriptor резолвит дескриптор метода по полному имени с ведущей косой.
func methodDescriptor(full string) (protoreflect.MethodDescriptor, error) {
	trimmed := strings.TrimPrefix(full, "/")
	slash := strings.LastIndex(trimmed, "/")
	if slash < 0 {
		return nil, errBadMethodName(full)
	}
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(trimmed[:slash]))
	if err != nil {
		return nil, err
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, errBadMethodName(full)
	}
	md := sd.Methods().ByName(protoreflect.Name(trimmed[slash+1:]))
	if md == nil {
		return nil, errBadMethodName(full)
	}
	return md, nil
}

type errBadMethodName string

func (e errBadMethodName) Error() string {
	return "метод " + string(e) + " не резолвится в реестре этого двоичного"
}

// TestVocabularyAbsentRefusesInsteadOfExempting — ОТСУТСТВИЕ словаря даёт
// именованный отказ, а не пустую разметку.
//
// # Почему это несущее, а не защитная ветка
//
// `Annotations.Exempt()` читает пустую разметку как «проверки нет». Значит
// читатель, вернувший нули вместо отказа, объявил бы ОСВОБОЖДЁННЫМ каждый метод
// процесса — и объявил бы молча, зелёным стартом. Разница между «отказ» и «нули»
// здесь и есть разница между fail-closed и всеразрешением.
//
// # Полоса перечня при этом ОСТАЁТСЯ читаемой
//
// И это не послабление: методу фундамента словарь не нужен вовсе — его полоса
// объявлена перечнем, а не разметкой. Процесс, слинковавший ТОЛЬКО контракты
// фундамента, законно поднимается без словаря; процесс, служащий доменный RPC,
// без словаря не поднимается. Проба утверждает обе стороны.
func TestVocabularyAbsentRefusesInsteadOfExempting(t *testing.T) {
	restore := catalogderive.UseVocabularyPackageForTest("corelib.authz.nosuch.v1")
	defer restore()

	// ДЕФЕКТ: метод ВНЕ перечня, словарь не резолвится → именованный отказ.
	md, err := methodDescriptor(probeGet)
	if err != nil {
		t.Fatalf("нейтральный дескриптор не резолвится: %v", err)
	}
	a, err := catalogderive.AnnotationsOf(md)
	if err == nil {
		t.Fatalf("словаря нет, а разметка прочитана как %+v — Exempt()=%v. Пустая разметка "+
			"означала бы «проверки нет» на КАЖДОМ методе процесса", a, a.Exempt())
	}
	if !errors.Is(err, catalogderive.ErrVocabularyAbsent) {
		t.Errorf("отказ не называет предмет: %v", err)
	}

	// ЗАКОННЫЙ БЛИЗНЕЦ: метод ИЗ перечня читается и без словаря.
	fmd, err := methodDescriptor("/corelib.operation.OperationService/Get")
	if err != nil {
		t.Fatalf("контракт фундамента не резолвится: %v", err)
	}
	fa, err := catalogderive.AnnotationsOf(fmd)
	if err != nil {
		t.Errorf("метод перечня отвергнут без словаря, хотя словарь ему не нужен: %v", err)
	}
	if fa.Permission != catalogderive.ExemptPermission {
		t.Errorf("полоса метода перечня = %q, ждали %q", fa.Permission, catalogderive.ExemptPermission)
	}
	t.Logf("перепись: без словаря — метод вне перечня отвергнут, метод перечня прочитан")
}
