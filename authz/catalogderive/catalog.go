// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// catalog.go — форма строки сгенерированного каталога прав, разделяемая с
// теми, кто его читает.
//
// Каталог — ВЫХОД генератора, а карта сервиса теперь выводится из его ВХОДА
// (аннотаций). Читать его нужно ровно для одного: чтобы сверить выход со
// входом. Пока обе стороны сходятся, «каталог, который читает оператор» и
// «правило, которое исполняет сервис» — одно и то же утверждение.
//
// ЧТЕНИЯ ЗДЕСЬ НЕТ (PRO-Robotech/kacho#2211). Прежде пакет нёс `LoadCatalog`,
// поднимавшийся от `dir` до go.mod и читавший `CatalogPath` под найденным
// корнем; комментарий утверждал, что «dir только выбирает старт подъёма,
// поэтому им нельзя назвать другой файл» — гарантию, которой подъём не даёт:
// он находит ПЕРВЫЙ go.mod выше `dir`, а чей это модуль — не спрашивает.
// Значение `dir` вне модуля дало бы корень чужого дерева и каталог, выведенный
// из него. Снято, а не починено: у `LoadCatalog`/`moduleRoot` не было ни
// одного вызывающего — ни в прод-коде, ни в тестах, ни в этом модуле, ни в
// `services/iam` (замер: `git grep -n '\bLoadCatalog('` находит только само
// объявление). Единственный живой читатель каталога
// (`internal/repohygiene/catalogparity_test.go`) резолвит корень своим
// `repoRoot(t)` и читает файл сам, эту функцию не зовя. Предмета, которому
// нужна была бы гарантия, не было — значит нечего было ни чинить, ни
// подтверждать.

package catalogderive

// Здесь стояла `CatalogPath` — координата порождённого каталога прав В ДЕРЕВЕ
// ПЛАТФОРМЫ, объявленная прод-кодом фундамента (задача #2532, класс 3).
//
// Снята вместе с предметом, а не спрятана. Замер на день снятия: вызывающих у
// неё **ноль** (`git grep -c 'catalogderive\.CatalogPath' -- '*.go'`), а тот же
// путь несут своими литералами **15** файлов — то есть «единым источником» она
// не была ни для кого и переживала своё основание молча.
//
// Почему это класс, а не мелочь: путь лежит в ЗНАЧЕНИИ, поэтому граф импортов
// его не показывает by construction. После разъезда фундамент дерева платформы
// не видит, и координата стала бы указывать в никуда — но ни сборка, ни пробы
// об этом не сказали бы ни слова.

// Entry is the subset of a catalog row this comparison needs.
type Entry struct {
	FQN              string `json:"fqn"`
	Permission       string `json:"permission"`
	RequiredRelation string `json:"required_relation"`
	ScopeExtractor   struct {
		ObjectType                 string `json:"object_type"`
		FromRequestField           string `json:"from_request_field"`
		ObjectTypeFromRequestField string `json:"object_type_from_request_field"`
	} `json:"scope_extractor"`
	// ScopeFiltered — the catalog's own declaration that the OWNING SERVICE
	// authorizes this call over the data it answers with, so the edge
	// authenticates and runs no per-RPC Check. It is the catalog-side counterpart
	// of authz.RPCEntry.ScopeFiltered, and Compare requires the two to agree.
	ScopeFiltered bool `json:"scope_filtered"`
	// HideExistence — the catalog's explicit mark that a deny on this method is
	// answered with the owning service's NotFound. Mirrors
	// authz.RPCEntry.HideExistence; see catalogHidesExistence for the derived
	// (unmarked) majority.
	HideExistence bool `json:"hide_existence"`
}
