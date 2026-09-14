// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive

import (
	"errors"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// vocabulary.go — СЛОВАРЬ разметки доступа, резолвимый ПО ИМЕНИ.
//
// # Почему по имени, а не типизированным импортом
//
// Словарь принадлежит службе доступа (решение владельца 2026-09-13). Фундамент —
// лист графа модулей, и типизированный импорт словаря завёл бы ребро
// `corelib → kaname`, то есть кольцо. Резолв по имени ребра сборки НЕ заводит:
// имя — строка, а не импорт.
//
// # Откуда тогда берётся регистрация
//
// СО СТОРОНЫ ДОМЕНА, и это свойство протокола, а не договорённость. Заглушка
// КАЖДОГО размеченного контракта несёт слепой импорт своего словаря — его
// кладёт туда генератор, потому что дескриптор объявляет зависимость. Значит
// всякий процесс, служащий хоть один размеченный RPC, имеет словарь в реестре
// BY CONSTRUCTION, и спрашивать его по имени можно безопасно.
//
// # Отсутствие словаря — ИМЕНОВАННЫЙ отказ, а не пустая разметка
//
// Это несущее, и оно проверено опытом, а не выведено. `Annotations.Exempt()`
// читает пустую разметку как «проверки нет»: незалинкованный словарь объявил бы
// ОСВОБОЖДЁННЫМ каждый метод процесса. Поэтому отсутствие словаря отказывает
// при ПОСТРОЕНИИ карты, называя причину, а не отдаёт нули.
//
// Приём взят не со стороны: тот же резолв по имени уже применяется двумя
// функциями ниже в этом же пакете (`scope.go`, `zeroRequest`).

// VocabularyPackage — proto-пакет словаря разметки доступа.
//
// Объявлен ОДИН раз и здесь: имена расширений ниже собираются от него, поэтому
// переименование словаря правится в одном месте, а не в семи.
const VocabularyPackage = "kaname.authz.v1"

// vocabularyPackage — имя, ПО КОТОРОМУ идёт резолв.
//
// Переменная, а не константа, ровно по одной причине: отказ «словаря нет»
// обязан быть доказан ИНЪЕКЦИЕЙ в настоящий путь чтения, а не проверкой его
// копии. Копия доказывала бы работоспособность копии. Подменяет её только
// крючок из `export_test.go`, то есть в поставляемое двоичное он не попадает
// ни при какой сборке.
var vocabularyPackage = VocabularyPackage

// Имена расширений словаря. Выписаны полями, а не собраны склейкой в месте
// чтения: склейка расходится с объявлением молча.
const (
	extPermission       = "permission"
	extRequiredRelation = "required_relation"
	extScopeExtractor   = "scope_extractor"
	extHideExistence    = "hide_existence"
	extScopeFiltered    = "scope_filtered"
)

// Поля сообщения `ScopeExtractor`. Читаются по имени поля через protoreflect, а
// НЕ приведением к порождённому типу: словарь может прийти в реестр и
// динамическим дескриптором (так его подаёт фикстура проб этого пакета), и
// приведение к типу тогда не сработает вовсе.
const (
	fieldObjectType                 = "object_type"
	fieldFromRequestField           = "from_request_field"
	fieldObjectTypeFromRequestField = "object_type_from_request_field"
)

// ErrVocabularyAbsent — словаря разметки доступа нет в реестре этого двоичного.
//
// Отдельная ошибка, а не строка: вызывающий вправе отличить «словарь не
// приехал» (свойство сборки) от «разметка неверна» (свойство контракта). Первое
// чинится ребром сборки, второе — правкой контракта, и посылать по второму
// адресу за первым значило бы посылать чинить не туда.
var ErrVocabularyAbsent = errors.New("catalogderive: словарь разметки доступа " +
	VocabularyPackage + " не зарегистрирован в этом двоичном")

// vocabulary — резолвнутые типы расширений словаря.
type vocabulary struct {
	permission       protoreflect.ExtensionType
	requiredRelation protoreflect.ExtensionType
	scopeExtractor   protoreflect.ExtensionType
	hideExistence    protoreflect.ExtensionType
	scopeFiltered    protoreflect.ExtensionType
}

// loadVocabulary резолвит словарь ОДИН раз на процесс.
//
// `OnceValues`, а не `Once` с переменной: ошибка обязана доехать до КАЖДОГО
// вызывающего, а не только до первого. Первый бы её обработал, остальные
// увидели бы пустой словарь — то есть ровно то всеразрешение, которое этот файл
// и закрывает.
var loadVocabulary = newVocabularyLoader()

func newVocabularyLoader() func() (*vocabulary, error) {
	return sync.OnceValues(loadVocabularyOnce)
}

func loadVocabularyOnce() (*vocabulary, error) {
	find := func(name string) (protoreflect.ExtensionType, error) {
		full := protoreflect.FullName(vocabularyPackage + "." + name)
		xt, err := protoregistry.GlobalTypes.FindExtensionByName(full)
		if err != nil {
			return nil, fmt.Errorf("%w: не резолвится расширение %s: %w", ErrVocabularyAbsent, full, err)
		}
		return xt, nil
	}

	var v vocabulary
	for _, step := range []struct {
		name string
		dst  *protoreflect.ExtensionType
	}{
		{extPermission, &v.permission},
		{extRequiredRelation, &v.requiredRelation},
		{extScopeExtractor, &v.scopeExtractor},
		{extHideExistence, &v.hideExistence},
		{extScopeFiltered, &v.scopeFiltered},
	} {
		xt, err := find(step.name)
		if err != nil {
			return nil, err
		}
		*step.dst = xt
	}
	return &v, nil
}

// readString / readBool / readScope — чтение одного расширения.
//
// `proto.HasExtension` ПЕРЕД чтением обязателен, и это не осторожность.
// Отсутствующее расширение-СООБЩЕНИЕ, прочитанное у динамического словаря,
// даёт ПАНИКУ внутри протобуфа (сверка типа в `extensionType.InterfaceOf`), а
// не нулевое значение. На шести скалярных расширениях дефект молчит и
// проявляется ровно на `scope_extractor` — то есть нашёлся бы не разбором, а
// падением проб.
func readString(opts *descriptorpb.MethodOptions, xt protoreflect.ExtensionType) string {
	if !proto.HasExtension(opts, xt) {
		return ""
	}
	s, _ := proto.GetExtension(opts, xt).(string)
	return s
}

func readBool(opts *descriptorpb.MethodOptions, xt protoreflect.ExtensionType) bool {
	if !proto.HasExtension(opts, xt) {
		return false
	}
	b, _ := proto.GetExtension(opts, xt).(bool)
	return b
}

// readScope читает поля `ScopeExtractor` ЧЕРЕЗ protoreflect.
//
// Приведения к порождённому типу здесь нет намеренно: словарь приходит из
// реестра, и его сообщение бывает как порождённым, так и динамическим. Чтение
// по ИМЕНИ ПОЛЯ работает в обоих случаях; приведение — только в одном, и второй
// молча дал бы пустую область, то есть проверку против `type:` с пустым
// идентификатором.
func readScope(opts *descriptorpb.MethodOptions, xt protoreflect.ExtensionType) (objectType, fromField, typeFromField string) {
	if !proto.HasExtension(opts, xt) {
		return "", "", ""
	}
	v := opts.ProtoReflect().Get(xt.TypeDescriptor())
	msg := v.Message()
	if msg == nil || !msg.IsValid() {
		return "", "", ""
	}
	get := func(name string) string {
		fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return ""
		}
		return msg.Get(fd).String()
	}
	return get(fieldObjectType), get(fieldFromRequestField), get(fieldObjectTypeFromRequestField)
}
