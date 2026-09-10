// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package catalogderive builds a service's in-process authz.RPCMap from the
// per-RPC authorization annotations carried by the proto descriptors already
// linked into its binary.
//
// # Why the map is derived and not written
//
// Two authorization decisions are taken on every public RPC: the api-gateway
// checks the generated permission catalog, and the owning service re-checks its
// own map. Until now the second was written by hand, so the two could name
// different relations on different objects — and then the effective requirement
// was their intersection, which is recorded in no document anyone grants against.
// A whole service could also carry no map at all, in which case the catalog's
// promise about that service was unbacked and no comparison could see it.
//
// Deriving removes the second declaration rather than adding a third checker:
// both artefacts now come from ONE source, the `corelib.authz.v1` method
// options, so "the service asks something else" stops being expressible.
//
// # Why the annotations and not the generated JSON
//
// The catalog JSON is generated FROM these annotations. Reading the JSON at
// service start would need a third embedded copy of it (the gateway and iam each
// carry one), and copies drift. The descriptors are already in the binary: a
// service links the generated stubs for the RPCs it serves, and those stubs carry
// the options verbatim. Deriving from them costs no new asset, and it makes the
// tree-wide comparison "catalog == annotations" a real cross-check of the
// generator rather than a tautology.
//
// # What fails at start rather than at request time
//
// The Go compiler cannot check an annotation: `from_request_field` is a string,
// and a typo in it names a field that does not exist. Derive resolves every field
// descriptor once, when the map is built, so such a row refuses to start the
// process instead of failing the Check of whoever calls that RPC first. The same
// posture covers a proto package that is not linked, a duplicate method key, and
// a method carrying no authorization annotation at all.
package catalogderive

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	authzv1 "github.com/PRO-Robotech/corelib/api/corelib/authz/v1"
	"github.com/PRO-Robotech/corelib/authz"
)

const (
	// ExemptPermission — the literal a method carries instead of a permission
	// string when it takes no per-RPC Check at all.
	ExemptPermission = "<exempt>"

	// ClusterObjectType / ClusterSingletonID — the deployment-wide singleton the
	// cluster-anchored rows resolve to. Exactly one cluster row exists
	// (iam domain, Cluster.id is pinned to this literal), so an annotation that
	// anchors on `cluster` names that object and nothing else.
	ClusterObjectType  = "cluster"
	ClusterSingletonID = "cluster_root"

	// WildcardField — `from_request_field: "*"` says the scope is not carried by
	// the request at all: either the deployment-wide cluster singleton, or an
	// object the caller does not name (which the relation store then refuses as
	// unscoped — deliberately, see wildcardExtractor).
	WildcardField = "*"
)

// Derive builds the RPCMap for the named proto packages — every method of every
// service declared in them, in the exact key form grpc-go passes to an
// interceptor ("/kacho.cloud.storage.v1.VolumeService/Get").
//
// A service names the packages whose services it registers: its own domain plus
// `kacho.cloud.operation` for the LRO envelope. That list is the service's
// identity, not a second statement of its permissions — the permissions come from
// the annotations, one per method, with no place left for the two to disagree.
//
// Every failure mode is fail-closed and named at the call site, because all of
// them produce the same runtime symptom otherwise: a method missing from the map
// is refused by the interceptor as unmapped, which reads to the caller as a
// permission problem and to the operator as nothing at all.
func Derive(protoPackages ...string) (authz.RPCMap, error) {
	if len(protoPackages) == 0 {
		return nil, fmt.Errorf("catalogderive: no proto package named; the derived map would be " +
			"empty and every RPC of this service would be refused as unmapped")
	}

	out := authz.RPCMap{}
	origin := map[string]string{} // method -> package that produced it

	for _, pkg := range protoPackages {
		methods := 0
		var rangeErr error
		protoregistry.GlobalFiles.RangeFilesByPackage(protoreflect.FullName(pkg),
			func(fd protoreflect.FileDescriptor) bool {
				for i := 0; i < fd.Services().Len(); i++ {
					sd := fd.Services().Get(i)
					for j := 0; j < sd.Methods().Len(); j++ {
						md := sd.Methods().Get(j)
						key := "/" + string(sd.FullName()) + "/" + string(md.Name())
						methods++
						if prev, dup := origin[key]; dup {
							rangeErr = fmt.Errorf("catalogderive: method %s is declared by both %q and %q",
								key, prev, pkg)
							return false
						}
						entry, err := entryFor(md)
						if err != nil {
							rangeErr = fmt.Errorf("catalogderive: %s: %w", key, err)
							return false
						}
						out[key] = entry
						origin[key] = pkg
					}
				}
				return true
			})
		if rangeErr != nil {
			return nil, rangeErr
		}
		if methods == 0 {
			return nil, fmt.Errorf("catalogderive: proto package %q declares no RPC in this binary — "+
				"either the name is wrong or its generated stubs are not linked; a silently empty "+
				"map refuses every call it was meant to describe", pkg)
		}
	}
	return out, nil
}

// MustDerive is Derive for a composition root, which has no second course of
// action: every failure it can report is a property of the BINARY (a stub that is
// not linked, an annotation naming a field that does not exist, an RPC carrying no
// annotation at all), identical on every start and unaffected by any request. A
// service that continued past it would serve with a map missing the very methods
// the operator thinks are gated, and each of them would then be refused as
// unmapped — a refusal that names neither the method nor the omission.
//
// So it refuses to start, loudly, naming the offending method. The message is the
// operator-facing refusal text and is meant to be read from a crash-looping pod.
func MustDerive(protoPackages ...string) authz.RPCMap {
	m, err := Derive(protoPackages...)
	if err != nil {
		panic("kacho authz: refusing to start — the per-RPC permission map cannot be derived from " +
			"the proto annotations linked into this binary: " + err.Error())
	}
	return m
}

// MethodCount reports how many RPCs the named proto package declares in this
// binary. It exists so a caller can tell "no findings" from "nothing was read"
// without reaching into the registry itself.
func MethodCount(protoPackage string) int {
	n := 0
	protoregistry.GlobalFiles.RangeFilesByPackage(protoreflect.FullName(protoPackage),
		func(fd protoreflect.FileDescriptor) bool {
			for i := 0; i < fd.Services().Len(); i++ {
				n += fd.Services().Get(i).Methods().Len()
			}
			return true
		})
	return n
}

// Annotations is one method's authorization annotations, read off its descriptor.
// Exported so a gate can compare the annotations against the generated catalog
// without re-implementing the extension reads.
type Annotations struct {
	Permission                 string
	RequiredRelation           string
	ScopeObjectType            string
	ScopeFromRequestField      string
	ScopeObjectTypeFromRequest string
	HideExistence              bool
	ScopeFiltered              bool
}

// Exempt reports the lane where the edge runs no per-RPC Check at all.
func (a Annotations) Exempt() bool {
	return a.Permission == ExemptPermission || (a.RequiredRelation == "" && !a.ScopeFiltered)
}

// AnnotationsOf reads the authz options off a method descriptor.
func AnnotationsOf(md protoreflect.MethodDescriptor) Annotations {
	opts, _ := md.Options().(*descriptorpb.MethodOptions)
	if opts == nil {
		return Annotations{}
	}
	a := Annotations{
		Permission:       proto.GetExtension(opts, authzv1.E_Permission).(string),
		RequiredRelation: proto.GetExtension(opts, authzv1.E_RequiredRelation).(string),
		HideExistence:    proto.GetExtension(opts, authzv1.E_HideExistence).(bool),
		ScopeFiltered:    proto.GetExtension(opts, authzv1.E_ScopeFiltered).(bool),
	}
	if se, ok := proto.GetExtension(opts, authzv1.E_ScopeExtractor).(*authzv1.ScopeExtractor); ok && se != nil {
		a.ScopeObjectType = se.GetObjectType()
		a.ScopeFromRequestField = se.GetFromRequestField()
		a.ScopeObjectTypeFromRequest = se.GetObjectTypeFromRequestField()
	}
	return a
}

// RangeAnnotated walks every RPC of the named proto packages and calls fn with
// the gRPC full method and its annotations. Used by the gates that compare the
// annotations against the generated catalog.
func RangeAnnotated(protoPackages []string, fn func(fullMethod string, md protoreflect.MethodDescriptor, a Annotations)) {
	for _, pkg := range protoPackages {
		protoregistry.GlobalFiles.RangeFilesByPackage(protoreflect.FullName(pkg),
			func(fd protoreflect.FileDescriptor) bool {
				for i := 0; i < fd.Services().Len(); i++ {
					sd := fd.Services().Get(i)
					for j := 0; j < sd.Methods().Len(); j++ {
						md := sd.Methods().Get(j)
						fn("/"+string(sd.FullName())+"/"+string(md.Name()), md, AnnotationsOf(md))
					}
				}
				return true
			})
	}
}

// entryFor turns one method's annotations into the RPCEntry the interceptor
// consumes. The three lanes are mutually exclusive and exhaustive; there is no
// fourth, and a method that fits none of them is an error rather than a default,
// because every default here is a decision about access taken by nobody.
func entryFor(md protoreflect.MethodDescriptor) (authz.RPCEntry, error) {
	a := AnnotationsOf(md)

	switch {
	case a.Permission == "" && a.RequiredRelation == "" && !a.ScopeFiltered:
		return authz.RPCEntry{}, fmt.Errorf("carries no authorization annotation; annotate it in " +
			"proto (permission / required_relation / scope_filtered) — an unannotated RPC is " +
			"refused as unmapped at request time, which names neither the method nor the omission")

	case a.ScopeFiltered:
		// The owning service narrows the answer over the data it returns, so
		// there is no single object to ask about in advance. The lane REQUIRES a
		// principal: dropping it to Public would let an unnamed caller reach a
		// path that has no second gate behind it.
		if a.RequiredRelation != "" || a.ScopeObjectType != "" {
			return authz.RPCEntry{}, fmt.Errorf("scope_filtered names a relation (%q) or a scope (%q); "+
				"the edge does not check them, so naming them states a narrowing that does not happen",
				a.RequiredRelation, a.ScopeObjectType)
		}
		return authz.RPCEntry{ScopeFiltered: true, Permission: a.Permission}, nil

	case a.Exempt():
		// No per-RPC Check. `Permission` is deliberately left empty: `<exempt>` is
		// a marker, not a permission string, and carrying it would make the entry
		// name an action no catalogue grants.
		if a.RequiredRelation != "" {
			return authz.RPCEntry{}, fmt.Errorf("exempt row also names relation %q", a.RequiredRelation)
		}
		return authz.RPCEntry{Public: true}, nil

	default:
		extract, err := buildExtractor(md, a)
		if err != nil {
			return authz.RPCEntry{}, err
		}
		return authz.RPCEntry{
			Relation:      a.RequiredRelation,
			Extract:       extract,
			HideExistence: a.HideExistence,
			Permission:    a.Permission,
		}, nil
	}
}

// buildExtractor resolves the request fields the annotation names, ONCE, and
// closes over the resolved descriptors.
//
// Resolving here rather than per call is the point: `from_request_field` is a
// string the compiler never sees, so a rename of the proto field leaves an
// annotation pointing at nothing. Resolved per call that reads as an empty scope
// id — a Check against `type:` — and the caller gets a denial indistinguishable
// from a real one. Resolved here it refuses to build the map, and the process
// does not start.
func buildExtractor(md protoreflect.MethodDescriptor, a Annotations) (authz.ObjectExtractor, error) {
	if a.ScopeObjectType == "" {
		return nil, fmt.Errorf("relation %q is checked against no scope: scope_extractor.object_type "+
			"is empty", a.RequiredRelation)
	}
	if a.ScopeFromRequestField == "" {
		return nil, fmt.Errorf("scope_extractor.from_request_field is empty; use %q when the scope is "+
			"not carried by the request", WildcardField)
	}

	input := md.Input()

	var typeField fieldPath
	if a.ScopeObjectTypeFromRequest != "" {
		fd, err := stringField(input, a.ScopeObjectTypeFromRequest, "object_type_from_request_field")
		if err != nil {
			return nil, err
		}
		typeField = fd
	}

	if a.ScopeFromRequestField == WildcardField {
		return wildcardExtractor(a.ScopeObjectType), nil
	}

	idField, err := stringField(input, a.ScopeFromRequestField, "from_request_field")
	if err != nil {
		return nil, err
	}

	staticType := a.ScopeObjectType
	wantMsg := input.FullName()
	return func(req any) (string, string, error) {
		m, err := reflectRequest(req, wantMsg)
		if err != nil {
			return "", "", err
		}
		objectType := staticType
		if typeField != nil {
			if v := strings.TrimSpace(typeField.get(m)); v != "" {
				objectType = v
			}
		}
		return objectType, idField.get(m), nil
	}, nil
}

// wildcardExtractor covers `from_request_field: "*"` — the scope the request does
// not name.
//
// For the `cluster` anchor that is the deployment singleton, and it is
// substituted here for the same reason the edge substitutes it: the relation
// store refuses `cluster:*` as unscoped, so leaving the wildcard in place would
// deny every caller while reading, in both artefacts, like a working check.
//
// For any other object type the wildcard is passed through unchanged, and the
// refusal that follows is the intended answer: the row asks about an object the
// caller never named, and inventing one would authorize against something other
// than what the call acts on.
func wildcardExtractor(objectType string) authz.ObjectExtractor {
	id := WildcardField
	if objectType == ClusterObjectType {
		id = ClusterSingletonID
	}
	return func(any) (string, string, error) { return objectType, id, nil }
}

// fieldPath is a resolved chain of field descriptors ending in a singular
// string. A single-element path is the ordinary case; longer ones exist because
// some requests carry the scope inside an embedded body message rather than at
// the top level.
type fieldPath []protoreflect.FieldDescriptor

// get reads the value at the end of the path. A missing intermediate message
// yields "" — the same answer as a field left unset, which the caller turns into
// a refusal because an empty scope id never satisfies a Check.
func (p fieldPath) get(m protoreflect.Message) string {
	for i, fd := range p {
		if i == len(p)-1 {
			return m.Get(fd).String()
		}
		sub := m.Get(fd).Message()
		if sub == nil || !sub.IsValid() {
			return ""
		}
		m = sub
	}
	return ""
}

// stringField resolves the field the annotation names — either a top-level field
// or a dotted path through embedded messages ("address.project_id").
//
// The dotted form exists for requests that wrap a body message: the scope of
// `InternalAddressService.CreateOwnedAddress` is the project of the address being
// created, and the address arrives as a nested `CreateAddressRequest` because the
// internal path deliberately reuses the public creation body whole. Spelling that
// scope out is what lets the annotation state the check the service performs;
// without it the single source could not express a check that exists, and the
// choice would have been between a false annotation and a dropped check.
func stringField(msg protoreflect.MessageDescriptor, name, annotation string) (fieldPath, error) {
	var out fieldPath
	cur := msg
	segments := strings.Split(name, ".")
	for i, seg := range segments {
		fd := cur.Fields().ByName(protoreflect.Name(seg))
		if fd == nil {
			return nil, fmt.Errorf("scope_extractor.%s names %q, but %s has no field %q (fields: %s)",
				annotation, name, cur.FullName(), seg, fieldNames(cur))
		}
		if fd.IsList() || fd.IsMap() {
			return nil, fmt.Errorf("scope_extractor.%s names %q, whose segment %q on %s is repeated; "+
				"a scope is one object, not many", annotation, name, seg, cur.FullName())
		}
		out = append(out, fd)
		last := i == len(segments)-1
		switch {
		case last && fd.Kind() != protoreflect.StringKind:
			return nil, fmt.Errorf("scope_extractor.%s names %q on %s, which is %s and not a string",
				annotation, name, cur.FullName(), fd.Kind())
		case !last && fd.Kind() != protoreflect.MessageKind:
			return nil, fmt.Errorf("scope_extractor.%s names %q, but segment %q on %s is %s and cannot "+
				"be descended into", annotation, name, seg, cur.FullName(), fd.Kind())
		case !last:
			cur = fd.Message()
		}
	}
	return out, nil
}

func fieldNames(msg protoreflect.MessageDescriptor) string {
	out := make([]string, 0, msg.Fields().Len())
	for i := 0; i < msg.Fields().Len(); i++ {
		out = append(out, string(msg.Fields().Get(i).Name()))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// reflectRequest checks that the request really is the message this method's
// extractor was built for.
//
// A foreign message is refused instead of read: reading it would return the zero
// value of a field that is not there, the Check would run against `type:` with an
// empty id, and the denial would be indistinguishable from a real one. Refusing
// makes the wiring mistake say so.
func reflectRequest(req any, want protoreflect.FullName) (protoreflect.Message, error) {
	pm, ok := req.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("authz scope extract: request is %T, not a proto message", req)
	}
	m := pm.ProtoReflect()
	if got := m.Descriptor().FullName(); got != want {
		return nil, fmt.Errorf("authz scope extract: request is %s, this method takes %s", got, want)
	}
	return m, nil
}
