// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive_test

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/PRO-Robotech/corelib/authz/catalogderive"
)

// probevocabulary_test.go — СЛОВАРЬ РАЗМЕТКИ, объявленный самой пробой.
//
// # Зачем он
//
// Словарь принадлежит службе доступа, а фундамент — лист графа модулей. Проба,
// импортирующая порождённую заглушку словаря типизированно, завела бы ребро
// `corelib → kaname` — то есть КОЛЬЦО, и завела бы его невидимо: ребро из
// `_test.go` в граф импортов модуля входит, `go mod tidy` его СОХРАНЯЕТ, а
// `go build ./...` о нём молчит.
//
// Проверено опытом, а не выведено: зависимость, пришедшая только из `_test.go`,
// пережила `go mod tidy` и осталась строкой `require`.
//
// # Почему объявить словарь пробе МОЖНО, а прод-коду нельзя
//
// Прод-код обязан читать ТОТ словарь, который приехал с доменом, — иначе он
// судил бы разметку по своей копии. Проба же проверяет не словарь, а ЧТЕНИЕ
// словаря: что резолв по имени находит расширения, что отсутствующее
// расширение-сообщение не роняет процесс, что полоса выводится верно. Для этого
// нужен словарь С ТЕМИ ЖЕ ИМЕНАМИ И НОМЕРАМИ, а не тот же файл.
//
// # Самоистечение громкое
//
// Верни кто-нибудь типизированный импорт заглушки словаря — регистрация в
// глобальном реестре столкнётся по имени файла и по номерам расширений, и
// процесс упадёт ПАНИКОЙ при инициализации. Это ровно тот отказ, который
// сторожит переезд контракта, и здесь он работает на нас.

// Номера расширений словаря. Совпадают с объявлением службы НЕ случайно: проба
// подаёт читателю разметку в том же виде, в каком её подаёт домен, и
// разошедшийся номер сделал бы пробу зелёной на входе, которого не бывает.
const (
	extNumPermission       = 50001
	extNumRequiredRelation = 50002
	extNumScopeExtractor   = 50003
	extNumRequiredACRMin   = 50004
	extNumHideExistence    = 50005
	extNumScopeFiltered    = 50006
	extNumExemptReason     = 50007
)

// vocabFile — имя файла словаря в реестре.
//
// Оно ОТЛИЧАЕТСЯ от настоящего (`kaname/authz/v1/authz_options.proto`)
// намеренно: столкнись оно с настоящим — и проба, случайно слинковавшая
// заглушку службы, падала бы паникой регистрации ВМЕСТО того, чтобы назвать
// предмет. Имя пакета при этом совпадает, потому что по пакету идёт резолв.
const vocabFile = "corelib/authz/probe/vocabulary.proto"

// probeVocabulary — резолвнутые типы расширений словаря пробы.
type probeVocabulary struct {
	permission       protoreflect.ExtensionType
	requiredRelation protoreflect.ExtensionType
	scopeExtractor   protoreflect.ExtensionType
	requiredACRMin   protoreflect.ExtensionType
	hideExistence    protoreflect.ExtensionType
	scopeFiltered    protoreflect.ExtensionType
	exemptReason     protoreflect.ExtensionType
	scopeExtractorMD protoreflect.MessageDescriptor
}

var vocab probeVocabulary

// ext — объявление одного расширения `google.protobuf.MethodOptions`.
func ext(name string, number int32, kind descriptorpb.FieldDescriptorProto_Type, typeName string) *descriptorpb.FieldDescriptorProto {
	f := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(number),
		Type:     kind.Enum(),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Extendee: proto.String(".google.protobuf.MethodOptions"),
	}
	if typeName != "" {
		f.TypeName = proto.String(typeName)
	}
	return f
}

// registerProbeVocabulary строит и регистрирует словарь разметки.
func registerProbeVocabulary() error {
	pkg := catalogderive.VocabularyPackage
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	boolean := descriptorpb.FieldDescriptorProto_TYPE_BOOL
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE

	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String(vocabFile),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("ScopeExtractor"),
			Field: []*descriptorpb.FieldDescriptorProto{
				strField("object_type", 1),
				strField("from_request_field", 2),
				strField("object_type_from_request_field", 3),
			},
		}},
		Extension: []*descriptorpb.FieldDescriptorProto{
			ext("permission", extNumPermission, str, ""),
			ext("required_relation", extNumRequiredRelation, str, ""),
			ext("scope_extractor", extNumScopeExtractor, msg, "."+pkg+".ScopeExtractor"),
			ext("required_acr_min", extNumRequiredACRMin, str, ""),
			ext("hide_existence", extNumHideExistence, boolean, ""),
			ext("scope_filtered", extNumScopeFiltered, boolean, ""),
			ext("exempt_reason", extNumExemptReason, str, ""),
		},
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		return fmt.Errorf("сборка словаря: %w", err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		return fmt.Errorf("регистрация словаря: %w", err)
	}

	vocab.scopeExtractorMD = fd.Messages().ByName("ScopeExtractor")
	if vocab.scopeExtractorMD == nil {
		return fmt.Errorf("в словаре нет сообщения ScopeExtractor")
	}
	if err := protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(vocab.scopeExtractorMD)); err != nil {
		return fmt.Errorf("регистрация типа ScopeExtractor: %w", err)
	}

	// Типы расширений регистрируются ОТДЕЛЬНО от файла: читатель спрашивает их
	// у `GlobalTypes`, и без этой половины резолв по имени отвечает «не
	// найдено» — то есть проба меряла бы отсутствие словаря, а не его чтение.
	bind := map[string]*protoreflect.ExtensionType{
		"permission":        &vocab.permission,
		"required_relation": &vocab.requiredRelation,
		"scope_extractor":   &vocab.scopeExtractor,
		"required_acr_min":  &vocab.requiredACRMin,
		"hide_existence":    &vocab.hideExistence,
		"scope_filtered":    &vocab.scopeFiltered,
		"exempt_reason":     &vocab.exemptReason,
	}
	for i := 0; i < fd.Extensions().Len(); i++ {
		xd := fd.Extensions().Get(i)
		xt := dynamicpb.NewExtensionType(xd)
		if err := protoregistry.GlobalTypes.RegisterExtension(xt); err != nil {
			return fmt.Errorf("регистрация расширения %s: %w", xd.FullName(), err)
		}
		dst, ok := bind[string(xd.Name())]
		if !ok {
			return fmt.Errorf("расширение %s объявлено, но проба его не связывает", xd.FullName())
		}
		*dst = xt
	}
	for name, dst := range bind {
		if *dst == nil {
			return fmt.Errorf("расширение %q связано пробой, но в словаре не объявлено", name)
		}
	}
	return nil
}

// setScope — записать `scope_extractor` динамическим сообщением.
//
// Значение строится `dynamicpb`, а не порождённым типом: порождённого у словаря
// пробы нет вовсе, и это то самое свойство, ради которого читатель разбирает
// сообщение ПО ИМЕНИ ПОЛЯ, а не приведением к типу.
func setScope(o *descriptorpb.MethodOptions, objectType, fromField, typeFromField string) {
	m := dynamicpb.NewMessage(vocab.scopeExtractorMD)
	set := func(name, value string) {
		if value == "" {
			return
		}
		fd := vocab.scopeExtractorMD.Fields().ByName(protoreflect.Name(name))
		m.Set(fd, protoreflect.ValueOfString(value))
	}
	set("object_type", objectType)
	set("from_request_field", fromField)
	set("object_type_from_request_field", typeFromField)
	proto.SetExtension(o, vocab.scopeExtractor, m)
}
