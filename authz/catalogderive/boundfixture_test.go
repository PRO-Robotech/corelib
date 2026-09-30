// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive_test

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	authzv1 "github.com/PRO-Robotech/corelib/api/corelib/authz/v1"
)

// boundfixture_test.go — нейтральные дескрипторы формы ScopeBound (З14).
//
// # Почему отдельные пакеты, а не методы в `probePackage`
//
// Отвергаемая аннотация роняет вывод ВСЕГО пакета, в котором стоит. Положи её
// в `probePackage` — и каждая проба соседних полос покраснела бы на чужом
// предмете. Поэтому у каждого исхода свой пакет: законная форма — в
// `boundPackage`, а каждая незаконная — в своём, и проба называет ровно тот,
// чей исход утверждает.
//
// Законная форма и отвергаемые отличаются ОДНИМ фактом — лишним полем
// аннотации; тип объекта, отношение и вход метода у них одни и те же.

const (
	// boundPackage — законная форма: тип объекта и `bound_to_server`, ничего
	// из запроса. Два метода одного типа — как `Claim` и `Ack` у ленты.
	boundPackage = "corelib.authz.probe.bound.v1"
	boundClaim   = "/corelib.authz.probe.bound.v1.InternalProbeFeedService/Claim"
	boundAck     = "/corelib.authz.probe.bound.v1.InternalProbeFeedService/Ack"

	// boundWithFieldPackage — `bound_to_server` вместе с `from_request_field`.
	boundWithFieldPackage = "corelib.authz.probe.boundfield.v1"
	boundWithFieldMethod  = "/corelib.authz.probe.boundfield.v1.InternalProbeFeedService/Claim"

	// boundWithTypeFieldPackage — `bound_to_server` вместе с
	// `object_type_from_request_field`.
	boundWithTypeFieldPackage = "corelib.authz.probe.boundtypefield.v1"
	boundWithTypeFieldMethod  = "/corelib.authz.probe.boundtypefield.v1.InternalProbeFeedService/Claim"

	// boundExemptPackage — `bound_to_server` на строке `<exempt>`: проверки нет,
	// а аннотация утверждает объект, о котором спросят.
	boundExemptPackage = "corelib.authz.probe.boundexempt.v1"
	boundExemptMethod  = "/corelib.authz.probe.boundexempt.v1.InternalProbeFeedService/Claim"

	// probeFeedType — тип объекта, к экземпляру которого привязывается сервер.
	probeFeedType = "probe_feed"
)

// boundOptions — аннотации метода формы ScopeBound; spoil портит ровно одно поле.
func boundOptions(permission, relation string, spoil func(*authzv1.ScopeExtractor)) *descriptorpb.MethodOptions {
	o := &descriptorpb.MethodOptions{}
	proto.SetExtension(o, authzv1.E_Permission, permission)
	proto.SetExtension(o, authzv1.E_RequiredRelation, relation)
	se := &authzv1.ScopeExtractor{ObjectType: probeFeedType, BoundToServer: true}
	if spoil != nil {
		spoil(se)
	}
	proto.SetExtension(o, authzv1.E_ScopeExtractor, se)
	return o
}

func registerBoundFiles() error {
	files := []struct {
		pkg     string
		methods map[string]*descriptorpb.MethodOptions
	}{
		{boundPackage, map[string]*descriptorpb.MethodOptions{
			"Claim": boundOptions("probe.feed.claim", "reader", nil),
			"Ack":   boundOptions("probe.feed.ack", "reader", nil),
		}},
		{boundWithFieldPackage, map[string]*descriptorpb.MethodOptions{
			"Claim": boundOptions("probe.feed.claim", "reader", func(se *authzv1.ScopeExtractor) {
				se.FromRequestField = "feed_id"
			}),
		}},
		{boundWithTypeFieldPackage, map[string]*descriptorpb.MethodOptions{
			"Claim": boundOptions("probe.feed.claim", "reader", func(se *authzv1.ScopeExtractor) {
				se.ObjectTypeFromRequestField = "feed_type"
			}),
		}},
		{boundExemptPackage, map[string]*descriptorpb.MethodOptions{
			"Claim": boundOptions(exemptPermission, "", nil),
		}},
	}
	for _, f := range files {
		if err := registerBoundFile(f.pkg, f.methods); err != nil {
			return fmt.Errorf("%s: %w", f.pkg, err)
		}
	}
	return nil
}

// exemptPermission — литерал строки без проверки; выписан здесь, а не взят из
// пакета, потому что фикстура строится в TestMain до любого утверждения о нём.
const exemptPermission = "<exempt>"

func registerBoundFile(pkg string, methods map[string]*descriptorpb.MethodOptions) error {
	svc := &descriptorpb.ServiceDescriptorProto{Name: proto.String("InternalProbeFeedService")}
	for _, name := range []string{"Claim", "Ack"} {
		opts, ok := methods[name]
		if !ok {
			continue
		}
		svc.Method = append(svc.Method, &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(name),
			InputType:  proto.String("." + pkg + ".FeedRequest"),
			OutputType: proto.String("." + pkg + ".FeedReply"),
			Options:    opts,
		})
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("corelib/authz/probe/" + pkg + ".proto"),
		Package:    proto.String(pkg),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			// Поля запроса есть, чтобы отвергаемая аннотация называла СУЩЕСТВУЮЩЕЕ
			// поле: отказ обязан прийти от формы, а не от опечатки в имени поля.
			{Name: proto.String("FeedRequest"), Field: []*descriptorpb.FieldDescriptorProto{
				strField("feed_id", 1), strField("feed_type", 2),
			}},
			{Name: proto.String("FeedReply")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{svc},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		return fmt.Errorf("сборка файла: %w", err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		return fmt.Errorf("регистрация файла: %w", err)
	}
	for i := 0; i < fd.Messages().Len(); i++ {
		md := fd.Messages().Get(i)
		if err := protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(md)); err != nil {
			return fmt.Errorf("регистрация типа %s: %w", md.FullName(), err)
		}
	}
	return nil
}
