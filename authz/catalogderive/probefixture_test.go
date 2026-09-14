// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	authzv1 "github.com/PRO-Robotech/corelib/api/corelib/authz/v1"
)

// probefixture_test.go — НЕЙТРАЛЬНЫЙ дескриптор, на котором проверяется вывод
// карты прав (задача #2532, класс 1).
//
// # Зачем он, если пробы работали
//
// Пробы этого пакета брали настоящие доменные контракты ПЛАТФОРМЫ — хранилище,
// реестр, сеть — и тем заводили ребро `фундамент → платформа` в трёх файлах
// ведомости границы. Ребро живёт только в пробах, но пробы входят в граф
// зависимостей модуля: после разъезда объявление фундамента потребовало бы
// платформу, то есть перевернуло бы целевую раскладку (`corelib ← kaname ← kacho`).
// Измерено: `go mod tidy` на собранном фундаменте дописывал
// `require github.com/PRO-Robotech/kacho` псевдоверсией.
//
// # Почему НЕЙТРАЛЬНЫЙ, а не «свой домен»
//
// Предмет проб — не домен, а СЛОВАРЬ АННОТАЦИЙ: как читается relation, как
// подставляется синглтон кластера, как разрешается составной путь области. Словарь
// живёт в фундаменте (`corelib.authz.v1`) и ничьей доменной принадлежности не
// имеет. Значит дескриптор, на котором его проверяют, тоже не обязан её иметь — и
// не должен: доменный контракт приносит с собой чужие решения (какой RPC
// кластерный, какая полоса у чтения каталога), и проба краснела на их изменении,
// не на своём предмете. Так и было: одна из проб уже переезжала с `List` на
// `Create`, когда полосу чтения каталога типов дисков исправили.
//
// # Что дескриптор несёт и чего НЕ несёт
//
// Он несёт по одному представителю каждой полосы вывода и НИ ОДНОГО лишнего
// метода: `MethodCount` сверяется с длиной карты, поэтому метод без предмета
// сделал бы пробу тотальности слабее.
//
// Он НЕ несёт `go_package`: путь импорта у него не образуется — стабов ему никто
// не порождает, тип сообщения регистрируется динамическим.
//
// # Имя пакета выбрано так, чтобы столкнуться было НЕЧЕМ
//
// `corelib.authz.probe.v1` в дереве не встречается ни разу, и регистрация в
// глобальном реестре не может перебить настоящий дескриптор by construction.

// probePackage — имя пакета нейтрального дескриптора.
const probePackage = "corelib.authz.probe.v1"

// Методы дескриптора. Полное имя выписывается один раз здесь и не собирается
// склейкой в пробах: склейка расходится с дескриптором молча.
const (
	probeGet          = "/corelib.authz.probe.v1.ThingService/Get"
	probeCreate       = "/corelib.authz.probe.v1.ThingService/Create"
	probeDelete       = "/corelib.authz.probe.v1.ThingService/Delete"
	probeSingleton    = "/corelib.authz.probe.v1.InternalThingService/CreateSingleton"
	probeScopeFilter  = "/corelib.authz.probe.v1.InternalThingService/ListShares"
	probeHidden       = "/corelib.authz.probe.v1.HiddenThingService/Get"
	probeDottedCreate = "/corelib.authz.probe.v1.OwnedThingService/CreateOwned"
)

// probeThingType — пообъектный тип, на котором стоит полоса пообъектного якоря.
const probeThingType = "probe_thing"

func TestMain(m *testing.M) {
	if err := registerProbeFile(); err != nil {
		fmt.Fprintf(os.Stderr, "нейтральный дескриптор не построен: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// scoped — аннотации обычной пообъектной строки.
func scoped(permission, relation, objectType, field string) *descriptorpb.MethodOptions {
	o := &descriptorpb.MethodOptions{}
	proto.SetExtension(o, authzv1.E_Permission, permission)
	proto.SetExtension(o, authzv1.E_RequiredRelation, relation)
	proto.SetExtension(o, authzv1.E_ScopeExtractor, &authzv1.ScopeExtractor{
		ObjectType:       objectType,
		FromRequestField: field,
	})
	return o
}

// strField — строковое поле сообщения.
func strField(name string, number int32) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:   proto.String(name),
		Number: proto.Int32(number),
		Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
	}
}

func registerProbeFile() error {
	hidden := scoped("probe.things.hidden", "v_get", probeThingType, "thing_id")
	proto.SetExtension(hidden, authzv1.E_HideExistence, true)

	filtered := &descriptorpb.MethodOptions{}
	proto.SetExtension(filtered, authzv1.E_Permission, "probe.shares.list")
	proto.SetExtension(filtered, authzv1.E_ScopeFiltered, true)

	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("corelib/authz/probe/v1/probe.proto"),
		Package:    proto.String(probePackage),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("GetThingRequest"), Field: []*descriptorpb.FieldDescriptorProto{strField("thing_id", 1)}},
			{Name: proto.String("CreateThingRequest"), Field: []*descriptorpb.FieldDescriptorProto{strField("holder_id", 1)}},
			{Name: proto.String("CreateSingletonRequest")},
			{Name: proto.String("ListSharesRequest")},
			{Name: proto.String("HiddenThingRequest"), Field: []*descriptorpb.FieldDescriptorProto{strField("thing_id", 1)}},
			{Name: proto.String("CreateOwnedThingRequest"), Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("inner"),
				Number:   proto.Int32(1),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				TypeName: proto.String("." + probePackage + ".CreateThingRequest"),
			}}},
			{Name: proto.String("ProbeReply")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("ThingService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					method("Get", "GetThingRequest", scoped("probe.things.get", "v_get", probeThingType, "thing_id")),
					method("Create", "CreateThingRequest", scoped("probe.things.create", "editor", "project", "holder_id")),
					method("Delete", "GetThingRequest", scoped("probe.things.delete", "v_delete", probeThingType, "thing_id")),
				},
			},
			{
				Name: proto.String("InternalThingService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					method("CreateSingleton", "CreateSingletonRequest",
						scoped("probe.singletons.create", "admin", "cluster", "*")),
					method("ListShares", "ListSharesRequest", filtered),
				},
			},
			{
				Name:   proto.String("HiddenThingService"),
				Method: []*descriptorpb.MethodDescriptorProto{method("Get", "HiddenThingRequest", hidden)},
			},
			{
				Name: proto.String("OwnedThingService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					method("CreateOwned", "CreateOwnedThingRequest",
						scoped("probe.owned.create", "editor", "project", "inner.holder_id")),
				},
			},
		},
	}

	// Резолвер нужен из-за объявленной зависимости на дескрипторы протобуфа:
	// без него сборка файла отказывает, а объявить зависимость обязательно —
	// аннотации расширяют `MethodOptions` из неё.
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		return fmt.Errorf("сборка файла: %w", err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		return fmt.Errorf("регистрация файла: %w", err)
	}
	// Типы сообщений регистрируются ОТДЕЛЬНО: их спрашивает `GlobalTypes`, и без
	// этой половины резолв входа метода отвечает «не найдено».
	for i := 0; i < fd.Messages().Len(); i++ {
		md := fd.Messages().Get(i)
		if err := protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(md)); err != nil {
			return fmt.Errorf("регистрация типа %s: %w", md.FullName(), err)
		}
	}
	return nil
}

func method(name, input string, opts *descriptorpb.MethodOptions) *descriptorpb.MethodDescriptorProto {
	return &descriptorpb.MethodDescriptorProto{
		Name:       proto.String(name),
		InputType:  proto.String("." + probePackage + "." + input),
		OutputType: proto.String("." + probePackage + ".ProbeReply"),
		Options:    opts,
	}
}

// newProbeRequest — динамическое сообщение нейтрального дескриптора.
//
// Пробы подают экстрактору именно его: `catalogderive` читает запрос через
// protoreflect и сверяет полное имя дескриптора, поэтому порождённый тип ему не
// требуется. Это не послабление — это то самое свойство, из-за которого доменные
// стабы здесь были не нужны с самого начала (`reflectRequest` в derive.go).
//
// Поля задаются ПУТЁМ (`inner.holder_id`), а не вложенными картами: путь и есть
// то, чем аннотация называет область, и запись пробы совпадает с записью
// аннотации буква в букву.
func newProbeRequest(t *testing.T, message string, fields map[string]string) proto.Message {
	t.Helper()

	md, err := protoregistry.GlobalFiles.FindDescriptorByName(
		protoreflect.FullName(probePackage + "." + message))
	if err != nil {
		t.Fatalf("сообщение %s не зарегистрировано: %v", message, err)
	}
	desc, ok := md.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("%s зарегистрировано не как сообщение", message)
	}

	msg := dynamicpb.NewMessage(desc)
	for path, value := range fields {
		var cur protoreflect.Message = msg
		curDesc := desc
		segs := strings.Split(path, ".")
		for i, seg := range segs {
			fd := curDesc.Fields().ByName(protoreflect.Name(seg))
			if fd == nil {
				t.Fatalf("у %s нет поля %q (путь %q)", curDesc.FullName(), seg, path)
			}
			if i == len(segs)-1 {
				cur.Set(fd, protoreflect.ValueOfString(value))
				break
			}
			cur, curDesc = cur.Mutable(fd).Message(), fd.Message()
		}
	}
	return msg
}
