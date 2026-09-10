// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

// hide_existence.go — the NOT_FOUND text used when a read is refused on an object
// the caller may not know about.
//
// When the check reports that the object exists but is not the caller's, the
// interceptor answers NOT_FOUND instead of PERMISSION_DENIED, so that "not yours"
// cannot be told from "not there". That protection is only as good as the TEXT:
// if the refusal reads differently from the owning service's genuine miss, the
// caller separates the two cases by reading the message, and the whole point of
// answering NOT_FOUND is lost.
//
// Each format is therefore copied VERBATIM from the owning service's repo-layer
// NotFound — the wire text a real miss produces (every service's error mapper
// strips only its sentinel prefix and passes the message through). The single %s
// is the caller-supplied id, echoed back: the caller already knows it.
//
// The same table exists on the api-gateway, which refuses the same class of read
// one hop earlier; both must agree with the owning service, and the drift guard
// in hide_existence_parity_test.go fails when they stop agreeing.

import (
	"fmt"
	"strings"
)

// hideExistenceNotFoundFormats maps an authorization object type to the Kachō
// contract-tone NotFound of the service that owns it.
//
// Sources (repo layer of the owning service):
//
//	iam       services/iam/internal/repo/kaname/pg/{account,project,user_pool,group,service_account,access_binding}_repo.go
//	compute   services/compute/internal/repo/instance_repo.go
//	vpc       services/vpc/internal/repo/kacho/pg/*.go (+ repo/helpers/sg.go)
//	nlb       services/nlb/internal/repo/kacho/pg/{load_balancer,listener,target_group}_repo.go
//	registry  services/registry/internal/repo/kacho/pg/errmap.go (wrapPgErr composes
//	          "<Resource> %s not found" from the resource name its caller passes)
//
// A new object-scoped resource adds its line here with the text taken from its
// repo layer — never invented, or the refusal stops matching the miss. A retired
// one takes its line WITH it: a row no catalog entry can reach still says the
// platform answers in an owner's voice, and that claim outlived three resources
// once already (see TestHideExistenceTablesCarryNoUnreachableType, which walks
// this table against the catalog rather than against the list above).
var hideExistenceNotFoundFormats = map[string]string{
	// iam
	"account":             "Account %s not found",
	"project":             "Project %s not found",
	"iam_user":            "User %s not found",
	"iam_group":           "Group %s not found",
	"iam_service_account": "ServiceAccount %s not found",
	"iam_access_binding":  "AccessBinding %s not found",
	// compute — Disk / Image / Snapshot are NOT here: that duplicate of block
	// storage was retired (kacho-storage owns those), and their rows went with it.
	"compute_instance": "Instance %s not found",
	// Текст СКОПИРОВАН с ответа владельца, а не сочинён: скрытие работает
	// только пока отказ по праву неотличим от настоящего промаха. Любое
	// расхождение — примета, по которой отличают «нет доступа» от «нет
	// объекта», то есть ровно то, что скрытие закрывает.
	"compute_guest_access_key": "GuestAccessKey %s not found",
	"compute_placement_group":  "PlacementGroup %s not found",
	// vpc
	"vpc_network":     "Network %s not found",
	"vpc_subnet":      "Subnet %s not found",
	"vpc_address":     "Address %s not found",
	"vpc_route_table": "Route table %s not found",
	// NOTE: the vpc security-group text carries a debug rendering of the id
	// ("SecurityGroup.Id(value=%s)") rather than the plain contract tone. It is
	// reproduced verbatim ON PURPOSE — byte-identity with the backend is what
	// closes the oracle — and may only change together with
	// services/vpc/internal/repo/helpers/sg.go.
	"vpc_security_group":    "Security group SecurityGroup.Id(value=%s) not found",
	"vpc_gateway":           "Gateway %s not found",
	"vpc_network_interface": "Network interface %s not found",
	// vpc CidrGroup — текст взят у слоя хранения
	// (services/vpc/internal/repo/kacho/pg/cidr_group.go), а не сочинён здесь:
	// отказ обязан читаться дословно как настоящий промах владельца.
	"vpc_cidr_group": "CidrGroup %s not found",
	// storage — services/storage/internal/repo/pg/{volume,snapshot,image}_repo.go.
	// Reachable since storage's object-self reads moved off the tier onto `v_get`:
	// a read refused here must read exactly like the owner's own miss.
	"storage_volume":   "Volume %s not found",
	"storage_snapshot": "Snapshot %s not found",
	"storage_image":    "Image %s not found",
	// nlb
	"nlb_network_load_balancer": "NetworkLoadBalancer %s not found",
	"nlb_listener":              "Listener %s not found",
	"nlb_target_group":          "TargetGroup %s not found",
	// registry
	"registry_registry": "Registry %s not found",
}

// OwnerNotFoundFormat отдаёт формат промаха владельца для типа объекта модели —
// ровно тот, которым звено решения о доступе ответит на скрывающем отказе.
//
// Существует ради ОДНОГО читателя: носитель контура (`pkg/servicehost`) сверяет
// с ним форму, объявленную сервисом в своём дескрипторе. Пока сверки не было,
// про один и тот же текст утверждали в двух местах — здесь и в дескрипторе, — и
// расхождение между ними осталось бы незамеченным ровно потому, что отвечает
// всегда ЭТА таблица: дескриптор объявил бы одно, вызывающий увидел бы другое,
// и «объявлено» перестало бы что-либо значить.
//
// ok ложен, если у типа нет текста владельца: тогда отказ отвечает нейтральным
// «not found», отличимым от настоящего промаха, — и это находка на стороне
// носителя, а не умолчание здесь.
func OwnerNotFoundFormat(objectType string) (string, bool) {
	f, ok := hideExistenceNotFoundFormats[objectType]
	return f, ok
}

// hideExistenceMessage is the message returned when a read is refused on an
// object the caller may not know exists.
//
// For a known object type with a concrete id it is the owning service's own miss,
// byte for byte. Otherwise it is the neutral "not found".
//
// The fallback is honestly NOT byte-identical to a real miss — a real miss names
// the resource and echoes the id. It is the least-informative remainder for the
// two cases where identity is impossible: an object type with no owning-service
// text to copy, and an absent/wildcard id (there is nothing to echo, and
// "<Resource>  not found" would be its own distinguishable, malformed string). It
// deliberately does NOT echo the authorization object type: that token appears
// nowhere on the public surface, so emitting it would both leak the internal
// dictionary and — being unlike any backend text — be the very tell this function
// exists to remove.
func hideExistenceMessage(objectType, objectID string) string {
	if text := ownerNotFoundText(objectType, objectID); text != "" {
		return text
	}
	return "not found"
}

// ownerNotFoundText returns the owning service's own miss for this object, or ""
// when there is nothing to copy — an object type with no owning-service text, or
// a call that named no concrete id (a wildcard/absent id would render
// "<Resource>  not found", a malformed string that resembles no backend answer
// and is therefore its own tell).
func ownerNotFoundText(objectType, objectID string) string {
	f, ok := hideExistenceNotFoundFormats[objectType]
	if !ok || objectID == "" || objectID == "*" {
		return ""
	}
	return fmt.Sprintf(f, objectID)
}

// HidesExistenceOnDeny reports whether a deny on this RPC must be answered with
// the owning service's NotFound rather than PermissionDenied.
//
// It is the SERVICE side of the same rule the api-gateway applies one hop earlier
// (CatalogEntry.HidesExistenceOnDeny), and it is deliberately written to the same
// shape, because the two answers are compared by whoever called: an edge that
// hides while the owner refuses is exactly the oracle hiding exists to close. The
// two deciders disagree in practice — each keeps its own positive-verdict cache
// with its own window — so a call the edge lets through can still be refused here.
//
// Resolution order:
//
//  1. explicit RPCEntry.HideExistence — for a MUTATION the edge marks the same
//     way (registry Update/Delete carry `hide_existence` in the catalog);
//  2. otherwise the shape of a per-object read: a unary `/Get` gated on the
//     verb-bearing `v_get`.
//
// Both are additionally gated on there being an owner text to speak with: an
// object type absent from hideExistenceNotFoundFormats keeps its PermissionDenied,
// since a neutral "not found" would be as distinguishable as the 403 it replaced.
// The repo-wide gate TestServiceGateHidesExistenceWhereTheEdgeDoes walks every
// service map against the catalog and fails when the two sides stop agreeing.
func HidesExistenceOnDeny(fullMethod string, entry RPCEntry, objectType string) bool {
	if _, ok := hideExistenceNotFoundFormats[objectType]; !ok {
		return false
	}
	if entry.HideExistence {
		return true
	}
	return strings.HasSuffix(fullMethod, "/Get") && entry.Relation == "v_get"
}
