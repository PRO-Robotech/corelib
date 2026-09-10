// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package proxytuple holds the ONE declaration of what a resource-owning module
// may write into the authorization model through kaname's FGA proxy —
// RegisterResource / UnregisterResource. (A third RPC, WriteCreatorTuple, shared
// this rule and was retired with zero callers — #788.)
//
// WHY THIS LIVES IN THE SHARED FOUNDATION AND NOT UNDER THE OWNER'S `internal/`.
// The rule has two sides that must never disagree: kaname decides whether to
// ACCEPT a delivered tuple, and five consumers decide what to EMIT. While the rule
// lived under `services/iam/internal/`, Go's visibility rule forbade a consumer
// from importing it, so every consumer knew the rule only as prose — six files
// repeated it in comments, and the single probe that asserted the receiving side's
// contract carried a HAND-WRITTEN COPY of the set because importing was impossible.
// A copy that cannot be compiled against the original drifts silently, and the cost
// of that drift is not a failing test: a relation the owner refuses is refused on
// EVERY delivery, the queue classifies the refusal as retryable, and the row wedges
// its partition for the whole retry window (data-integrity.md §«Межсервисное
// намерение — контракт ПРИНИМАЮЩЕЙ стороны»). Moving the rule here makes the copy
// unnecessary: the owner imports it into its accept-check, consumers import it into
// their intent builders, and the tree gate reads the same declaration.
//
// WHAT THE RULE DECIDES — A TRIPLE, NOT A RELATION. Acceptance is a verdict about
// «subject, relation, object type» together. A check that knows only the relation
// set is WRONG in the permissive direction as well as the strict one: public read
// is expressed by the pair `user:* #v_get`, which no hierarchical relation covers
// and for which no separate «public» flag exists — the tuple IS the visibility. Do
// not reintroduce a bare relation-membership gate on the write path; ValidateTuple
// is the whole rule.
package proxytuple

import (
	"errors"
	"sort"
	"strings"

	"github.com/PRO-Robotech/corelib/platformmodules"
)

// ErrRefused is the single verdict this rule produces: the tuple is not one this
// module may write. It carries NO reason — which clause refused is deliberately not
// observable to the caller (fail-closed, no oracle) — and it is transport-free, so
// the rule stays importable by a domain layer that may not depend on gRPC
// (architecture.md dependency rule). The owner maps it to PermissionDenied at its
// transport boundary, in ONE place, and a test there locks the code and the text.
var ErrRefused = errors.New("proxy tuple refused")

// Relation is one relation name of the authorization model as it appears on the
// wire of a proxy write. It is a distinct type rather than a bare string so a
// consumer's intent builder names the SAME constant the owner's accept-check
// evaluates; `string(RelationProject)` is a constant expression, so a consumer
// keeps its own untyped constant without re-typing the value.
type Relation string

// The owner-hierarchy relations a module may write through the proxy. ONLY
// ownership / parent links: the resource belongs to a scope (`project`, `parent`)
// or was created by somebody (`owner`). Privilege relations
// (`system_admin`/`admin`/`editor`/`viewer`/`v_*`/`fga_writer`/`use`) are absent on
// purpose — those are authored by the AccessBinding flow, where a grant is
// enumerable, scoped and revocable, never by a module speaking for itself.
//
// `account` USED TO BE HERE AND IS NOT, and its absence is load-bearing rather
// than an oversight. No module owns a resource whose containment pointer is an
// account: iam writes its own account links directly, on its own object types,
// without the proxy. An accepted relation nobody emits cannot be observed either
// working or broken — nothing reaches it — while the next reader takes it for a
// live capability of the product and builds a resource on it. Held by
// TestEveryAcceptedRelationHasAnEmitter, which reddens on any entry that loses its
// producer, and by TestProxyRegistrationTriplesAreAcceptedByOwner, which reddens
// with a coordinate the moment somebody emits a relation that is not here — so
// putting the tier back when a resource actually needs it is a deliberate edit.
const (
	RelationProject Relation = "project"
	RelationParent  Relation = "parent"
	RelationOwner   Relation = "owner"
)

// hierarchicalRelations — the set above, in the form the rule evaluates.
var hierarchicalRelations = map[Relation]struct{}{
	RelationProject: {},
	RelationParent:  {},
	RelationOwner:   {},
}

// Hierarchical reports whether r is one of the owner-hierarchy relations. It is
// NOT the accept rule — a tuple also has to satisfy the object-type and domain
// constraints of ValidateTuple, and a legitimate public-read pair is not
// hierarchical at all. Exported for the census of the tree gate and for the removal
// direction (IsProxyWritable), never as a write-path gate on its own.
func Hierarchical(r Relation) bool {
	_, ok := hierarchicalRelations[r]
	return ok
}

// HierarchicalRelations returns the accepted owner-hierarchy relations, sorted the
// way they are declared. Consumers of the census (gates, reports) get a copy.
func HierarchicalRelations() []Relation {
	return []Relation{RelationProject, RelationParent, RelationOwner}
}

// PublicReadRelation / PublicReadSubject — the only NON-hierarchical pair the proxy
// accepts: «anybody reads this module's resource».
//
// Publicness is expressed by the tuple itself: kacho-registry writes
// `user:* #v_get @registry_repository:<reg>/<repo>` and that tuple IS the
// visibility an anonymous pull resolves — there is no separate flag. Without the
// pair the intent would be emitted and refused whole, and a public repository would
// not exist under any configuration.
//
// The narrowing is by SUBJECT, not by relation: the wildcard `user:*` names NO
// individual recipient, so a module still cannot hand read access to a particular
// user or service account — that stays with AccessBinding, where it is enumerable,
// scoped and revocable. Every other constraint (object domain, forbidden types)
// applies unchanged.
const (
	PublicReadRelation Relation = "v_get"
	// Тип назван явно: в группе констант вторая строка без типа унаследовала бы
	// не тип соседа, а нетипизированную константу — и `PublicReadSubject` стал бы
	// строкой там, где рядом стоит `Relation`. Читается как «оба одного типа»,
	// проверяется компилятором как разные.
	PublicReadSubject string = "user:*"
)

// publicReadObjectTypes — the CLOSED list of object types for which publicness is a
// product capability of the resource. Subject narrowing alone already prevents
// granting read to a NAMED recipient, but without this list a module could make any
// of its own resources world-readable — and «anybody reads my network» is not a
// capability of the product. A type is added here deliberately, together with the
// feature that makes publicness part of that resource's contract.
var publicReadObjectTypes = map[string]struct{}{
	// registry: a public repository is an anonymous `docker pull`; the existence of
	// the tuple IS visibility=PUBLIC (there is no flag the data plane reads).
	"registry_repository": {},
}

// PublicReadObjectTypes returns the closed list above, for censuses and gates.
func PublicReadObjectTypes() []string { return []string{"registry_repository"} }

// ForbiddenObjectTypes returns the closed forbidden set, sorted. Derived from the
// map the write path evaluates rather than written out a second time: a
// hand-written copy of a set cannot be compiled against the original and drifts
// silently — which is the whole reason the rule was moved into this package.
// Exported for the tree gate that requires every entry to name a type the model
// actually declares.
func ForbiddenObjectTypes() []string {
	out := make([]string, 0, len(forbiddenObjectTypes))
	for t := range forbiddenObjectTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// forbiddenObjectTypes — object types that are never a module's resource and
// therefore can never be the object of a proxy tuple: the platform singleton
// `cluster`, the shared hierarchy and subject types, and the resource types of the
// iam domain. Forbidden even when the caller's domain is unknown, so that these
// objects are unreachable through the proxy under every configuration — the empty
// caller domain switches the domain binding off, and this set is then the only
// clause standing between a module and an object of somebody else's domain.
//
// EVERY ENTRY NAMES A TYPE THE MODEL DECLARES, AND EVERY NON-MODULE TYPE OF THE
// MODEL IS NAMED HERE. Both halves are held by the tree gate
// TestForbiddenProxyObjectTypesAgreeWithTheModel, and each half closes a silence of
// its own. An entry the model does not declare excludes NOTHING — the input it
// would refuse is not representable — while the list reads as if the type were
// covered; that was `role` against the model's `iam_role` (#1883), a dead entry
// that produced neither a red run nor a refusal for the whole of its life. A
// non-module type with no entry is reachable in exactly the mode the set exists
// for; that was every other resource type of the iam domain — six of them,
// `iam_fgaproxy` among them (its own reasoning is kept at the entry).
//
// `role` USED TO BE HERE AND IS NOT: the model declares `iam_role`, and a bare
// `role` type it never declared. The entry excluded nothing and the list read as
// if the type were covered — the gate above now refuses that shape outright.
//
// `iam` is deliberately absent from the emitter census (proxyConsumerDomains): iam
// writes its own links directly, without the proxy. That is what makes its types
// the very class this set has to close.
var forbiddenObjectTypes = map[string]struct{}{
	// платформенный синглтон и общие предки иерархии
	"cluster": {},
	"account": {},
	"project": {},
	// типы субъектов
	"user":            {},
	"service_account": {},
	"group":           {},
	// ресурсные типы домена iam
	"iam_user":            {},
	"iam_service_account": {},
	"iam_group":           {},
	"iam_role":            {},
	"iam_access_binding":  {},
	// iam_fgaproxy — служебная вершина, к которой привязано само право модуля
	// писать факты. Ресурсом модуля она не является ни при какой конфигурации, а
	// без записи здесь оставалась достижимой в двух посадках: у вызывающего с
	// коротким именем `iam` (приставка совпадает) и при неизвестном домене, где
	// связывание по домену отключено. Тип объявлен моделью НЕГРАНТУЕМЫМ, то есть
	// живой строки в каталоге ресурсов у него нет — регистрация им отвергалась бы
	// принимающей стороной на каждой доставке. Держится
	// TestProxyAdmittedObjectTypesAreInTheCatalog: правило приёма не вправе
	// допускать тип, которого каталог не знает.
	"iam_fgaproxy": {},
}

// IsPublicReadGrant reports whether the pair is «anybody reads this resource»
// (`user:* #v_get`).
//
// It differs from a hierarchical intent in kind: it does not describe the state of
// the resource (no parent scope, no labels) — it only opens reading. The applying
// side must therefore treat it as a PURE tuple and leave the resource projection
// alone: a register would otherwise blank the parent scope and an unregister would
// delete the projection row of a live resource. Exported so the policy and the
// apply path share ONE predicate.
func IsPublicReadGrant(subject, relation string) bool {
	return Relation(strings.TrimSpace(relation)) == PublicReadRelation &&
		strings.TrimSpace(subject) == PublicReadSubject
}

// IsProxyWritable — could THIS relation have been written by a module through the
// proxy?
//
// The same closed set ValidateTuple decides «accept this write» with, asked in the
// other direction: «is this one of ours» when the object is torn down. One
// predicate for both directions — otherwise removal drifts away from acceptance,
// and it drifts silently: a set that was accepted but not removed is access left
// behind.
//
// Relations outside the set are deliberately not claimed here: per-object verbs are
// derived by the reconciler from grants, and removing those is its work, not this
// path's. A second place deciding the same question is a race and a divergence, not
// a safety net.
func IsProxyWritable(subject, relation string) bool {
	if IsPublicReadGrant(subject, relation) {
		return true
	}
	return Hierarchical(Relation(strings.TrimSpace(relation)))
}

// publicReadAllowed — a publication is accepted only for a type from the closed
// list. The type check is separate from IsPublicReadGrant on purpose: the latter
// answers «is this intent a publication?» (the apply path asks the same, so as not
// to touch the resource projection), the list answers «is publicness due to this
// resource at all?».
func publicReadAllowed(subject, relation, objType string) bool {
	if !IsPublicReadGrant(subject, relation) {
		return false
	}
	_, ok := publicReadObjectTypes[objType]
	return ok
}

// TypeOwner — ПОРТ: чей это тип объекта. Владельца подаёт ВЫЗЫВАЮЩИЙ.
//
// Порт, а не таблица здесь: полный перечень типов живёт в закрытой таблице iam
// (`services/iam/internal/authzmap`), за границей видимости этого пакета, и
// вторая его копия разошлась бы с первой МОЛЧА. Пакет импортируют все семь
// служб, поэтому «перенести правило туда, где перечень виден» невыразимо;
// вызывающий у ValidateTuple в прод-коде ровно ОДИН, и он в iam — значит подать
// словарь может он.
//
// Ответ — МОДУЛЬ КАТАЛОГА, а не короткое имя службы: у балансировщика они
// различны (`nlb` / `loadbalancer`), и сравнение по имени службы не совпало бы
// для него НИКОГДА, отняв три живых типа. Наблюдаемо это было бы как «ресурс
// создан, доступа нет».
//
// ok=false означает «платформа этого типа не знает» — и это НЕ отказ: см.
// ValidateTuple, ветвь возврата к приставке.
type TypeOwner interface {
	CatalogModuleOfObjectType(objType string) (string, bool)
}

// Option — необязательная настройка правила.
//
// Вариативная форма, а не второй аргумент: подпись ValidateTuple читают семь
// служб и пять наборов проб, и её смена ради необязательной величины сделала бы
// ломающим изменением то, что ломающим не является.
type Option func(*policy)

// policy — то, чем правило располагает сверх самого кортежа.
type policy struct{ owner TypeOwner }

// WithTypeOwner подаёт правилу словарь владения типом.
//
// Не подан — владение судится ПРИСТАВКОЙ, как и раньше. Сделав словарь
// обязательным, мы отвергли бы всё у всякого вызывающего, его не завёдшего, —
// причём отказом ОПАКОВЫМ by design, то есть с худшей возможной диагностикой.
func WithTypeOwner(o TypeOwner) Option {
	return func(p *policy) { p.owner = o }
}

// objectDomainForCaller — приставка типов объекта, которые модулю позволено
// писать. Берётся у СЛОВАРЯ имён модулей, а не выводится из имени вызывающего.
//
// ПОЧЕМУ НЕ СОГЛАШЕНИЕ ОБ ИМЕНОВАНИИ. Здесь стояло «домен равен имени службы, а
// исключения живут в карте», и карта была ПУСТА — то есть «чей это тип» отвечала
// приставка имени. Полоса при этом превентивна, и это измерено: приставка
// совпадает с коротким именем у всех пяти сегодняшних эмитентов, поэтому ни один
// вердикт не менялся. Предмет был в другом: совпадение оставалось УСЛОВИЕМ
// работы — тип, чьё имя в модели не начинается с имени его модуля, был невыразим
// (#1885). Наивная же починка «имя службы == модуль каталога» отняла бы у
// балансировщика три живых типа: у него различны ТРИ написания (`nlb` /
// `loadbalancer` / `nlb_listener`), и словарь объявляет все три порознь именно
// затем, чтобы эту ошибку нельзя было выразить.
//
// ДВА «нет» РАЗЛИЧАЮТСЯ, и оба отвергают: ok=false — модуля словарь не знает
// вовсе; пустая строка — модуль объявлен и собственных типов объекта у него нет.
// Схлопнуть их в одно значило бы вернуть проверку приставкой через заднюю дверь:
// сравнение с пустым доменом молча отвергает всё, но по НЕ ТОЙ причине, и первый
// же тип такого домена стал бы достижим без единого решения.
//
// ЭТО ПОЛОВИНА ПРАВИЛА, А НЕ ВСЁ ОНО. Приставка судит владение там, где словаря
// нет либо он типа не знает; там, где знает, — судит СЛОВАРЬ, и судит по строке
// (см. TypeOwner и ValidateTuple). Полный перечень типов живёт в закрытой таблице
// iam, за границей видимости пакета, и вторая его копия разошлась бы с первой
// молча — поэтому здесь ПОРТ, а таблицу подаёт вызывающий.
//
// ПЕРЕЕЗД ПАКЕТА ЭТОГО НЕ РЕШИЛ БЫ — измерено, а не предположено. Пакет импортируют
// ВСЕ семь служб (`git grep -ln 'pkg/authz/proxytuple' -- '*.go' | sed 's#/[^/]*$##'
// | sort -u` → 21 каталог, из них шесть служб сверх iam), поэтому «перенести его
// туда, где перечень виден» невыразимо: перечень виден только внутри
// `services/iam/`. Вызывающий у `ValidateTuple` в прод-коде ровно ОДИН, и он в iam
// (`services/iam/internal/apps/kaname/api/internal_iam/handler.go`, предикат:
// `git grep -n 'ValidateTuple(' -- '*.go' ':!*_test.go'`), — он словарь и подаёт.
//
// ПОРЯДОК БЫЛ НАЗВАН ЗАРАНЕЕ И СОБЛЮДЁН. Здесь стояло «порт сегодня не заводится»,
// и довод был про ПОРЯДОК, а не про возможность: сделать РУКОПИСНУЮ таблицу
// несущей на пути запроса значило бы требовать правки Go в iam прежде, чем модуль
// сможет зарегистрировать свой новый ресурс, — при отказе ОПАКОВОМ by design
// (ниже: «без утечки того, какая оговорка отвергла»), то есть с худшей возможной
// диагностикой. Таблица перестала быть рукописной (#1930, #1092): её порождает
// манифест того же модуля, поэтому объявление ресурса и право на его регистрацию
// приезжают ОДНИМ изменением. Препятствие снято — и порт заведён (#1885).
//
// ЧТО ОСТАЛОСЬ ПРИСТАВКЕ. Тип, которого закрытая таблица не знает, судится ею
// по-прежнему: отвергнуть его значило бы вернуть тот самый опаковый отказ на
// вход, у которого вызывающему нечего чинить. Граница держится пробой
// (`type_owner_test.go`, «неизвестный словарю тип»), а не этой строкой.
func objectDomainForCaller(callerDomain string) (string, bool) {
	domain, known := platformmodules.ObjectDomainOfService(callerDomain)
	if !known || domain == "" {
		return "", false
	}
	return domain, true
}

// ValidateTuple constrains the proxy write path to least privilege: a module writes
// an owner-hierarchy tuple (plus the publication for anonymous reading — see
// PublicReadRelation) ONLY onto an object of its own domain. callerDomain is the
// service short name from the verified mTLS SAN (vpc/compute/nlb/registry); empty
// (domain unknown) disables the domain binding, while the relation set, the subject
// narrowing of a publication and the forbidden object types apply always. Any
// violation → PermissionDenied, fail-closed, without leaking which clause refused.
func ValidateTuple(callerDomain, subject, relation, object string, opts ...Option) error {
	var p policy
	for _, opt := range opts {
		opt(&p)
	}
	subject = strings.TrimSpace(subject)
	rel := Relation(strings.TrimSpace(relation))
	object = strings.TrimSpace(object)
	if rel == "" || object == "" {
		return ErrRefused
	}
	hierarchical := Hierarchical(rel)
	if !hierarchical && !IsPublicReadGrant(subject, string(rel)) {
		return ErrRefused
	}
	colon := strings.IndexByte(object, ':')
	if colon <= 0 || colon == len(object)-1 {
		return ErrRefused
	}
	objType := object[:colon]
	if _, bad := forbiddenObjectTypes[objType]; bad {
		return ErrRefused
	}
	// A publication is allowed only for a type publicness is due to (closed list).
	// Hierarchical relations do not take this branch: they are already bound to the
	// caller's domain below.
	if !hierarchical && !publicReadAllowed(subject, string(rel), objType) {
		return ErrRefused
	}
	// Domain binding: the object must belong to the caller's domain (vpc→`vpc_*`,
	// compute→`compute_*`, nlb→`nlb_*`), and WHICH domain that is comes from the
	// module-name vocabulary — see objectDomainForCaller. An empty callerDomain
	// skips this clause, while the forbidden set and the relation set above still
	// hold the boundary against cluster / iam / privilege objects.
	if callerDomain != "" {
		// Словарь ЗАМЕЩАЕТ приставку там, где он тип знает: иначе тип, чьё имя в
		// модели не начинается с домена его модуля, остался бы невыразим, а
		// «чей это тип» продолжало бы отвечать соглашение об именовании.
		if p.owner != nil {
			if module, known := p.owner.CatalogModuleOfObjectType(objType); known {
				mine, ok := platformmodules.CatalogModuleOfService(callerDomain)
				if !ok || module != mine {
					return ErrRefused
				}
				return nil
			}
		}
		// Тип, которого словарь не знает, судится приставкой — по-прежнему.
		// Отвергнуть его значило бы требовать правки Go в iam прежде, чем модуль
		// сможет зарегистрировать свой новый ресурс, и отказ был бы ОПАКОВЫМ: у
		// вызывающего нет ни причины, ни того, что чинить. Граница названа, а не
		// умолчана, и держится пробой (`type_owner_test.go`).
		domain, ok := objectDomainForCaller(callerDomain)
		if !ok || !strings.HasPrefix(objType, domain+"_") {
			return ErrRefused
		}
	}
	return nil
}
