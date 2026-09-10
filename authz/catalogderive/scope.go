// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/PRO-Robotech/corelib/authz"
)

// ScopeObjectType recovers the FGA object type an RPCEntry's extractor declares,
// by invoking it against a zero-valued instance of the method's request message
// resolved from the global proto registry. ok=false when the request type is not
// registered, when the entry carries no extractor, or when the extractor is
// request-dependent (it errors or panics on an empty request) — in which case
// there is no single declared type to compare.
func ScopeObjectType(fullMethod string, entry authz.RPCEntry) (objectType string, ok bool) {
	if entry.Extract == nil {
		return "", false
	}
	req, ok := zeroRequest(fullMethod)
	if !ok {
		return "", false
	}
	defer func() {
		// A scope-conditional extractor may dereference fields that only exist on
		// a populated request. That is not a divergence, it is "no static answer".
		if r := recover(); r != nil {
			objectType, ok = "", false
		}
	}()
	ot, _, err := entry.Extract(req)
	if err != nil || ot == "" {
		return "", false
	}
	return ot, true
}

// zeroRequest resolves the request message of fullMethod ("/pkg.Service/Method")
// from the global proto registry and returns a zero-valued instance of the
// GENERATED Go type — the same concrete type the service's extractor asserts on.
func zeroRequest(fullMethod string) (any, bool) {
	trimmed := strings.TrimPrefix(fullMethod, "/")
	slash := strings.LastIndex(trimmed, "/")
	if slash < 0 {
		return nil, false
	}
	svcName, methodName := trimmed[:slash], trimmed[slash+1:]

	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svcName))
	if err != nil {
		return nil, false
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, false
	}
	md := sd.Methods().ByName(protoreflect.Name(methodName))
	if md == nil {
		return nil, false
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
	if err != nil {
		return nil, false
	}
	return mt.New().Interface(), true
}

// ObjectScopedTypes returns the per-resource FGA object types a map anchors on,
// excluding the hierarchy anchors (`project`, `account`, `cluster`).
//
// The distinction is the one existence-hiding turns on: a deny on a per-resource
// object must be answered with the owner's NotFound, because the caller named
// that object and a PermissionDenied would confirm it exists. A deny on a
// hierarchy anchor confirms nothing of the sort — the caller named a collection,
// and its existence is not the secret.
//
// Deriving the set rather than listing it removes the way it used to go stale: a
// new resource type reaches the map through its annotations, and a hand-written
// list is the one place nobody remembers to extend — which shows up as a resource
// whose denial silently stops hiding.
func ObjectScopedTypes(m authz.RPCMap) map[string]struct{} {
	out := map[string]struct{}{}
	for method, e := range m {
		ot, ok := ScopeObjectType(method, e)
		if !ok {
			continue
		}
		switch ot {
		case "project", "account", ClusterObjectType:
			continue
		}
		out[ot] = struct{}{}
	}
	return out
}
