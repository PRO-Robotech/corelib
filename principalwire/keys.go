// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package principalwire — ЕДИНСТВЕННОЕ объявление пространства имён личности на
// проводе (приёмка KAN-WIRE-1, сценарии KAN-W2-05 / KAN-W2-06, предмет `ПР-2`).
//
// # Что здесь объявлено и почему в одном месте
//
// Личность арендатора едет от края к слушателю службы набором ключей под общей
// приставкой. У этого набора было ДВА независимых объявления — своё у края и
// своё у фундамента, — и общей константы между ними не существовало. Значит
// переименование одной стороны СОБИРАЛОСЬ ЧИСТО: приёмник, не найдя своих
// ключей, читал это как «личности нет» и шёл дальше. Отказом это не кончалось,
// потому что отсутствие личности в этом тракте отказом не является намеренно —
// фоновые пути ходят без неё.
//
// Здесь объявление одно, поэтому расхождение двух написаний стало
// невыразимым: переименовать «одну сторону» больше нечего. Носители
// (`gateway/internal/principalmeta` у края, `pkg/grpcsrv` у фундамента) берут
// имена отсюда и своих литералов не держат — это проверяет гейт дерева
// `internal/repohygiene` `TestIdentityWireNamespaceIsDeclaredOnce`.
//
// # Три поверхностные формы ОДНОГО логического ключа
//
//   - `Header*` — канонический HTTP-заголовок (к нему приводит `http.Header`);
//   - `HeaderGRPCMeta*` — он же под мостовой приставкой: входящий HTTP-заголовок
//     `Grpc-Metadata-Foo` приезжает слушателю метаданным `foo`;
//   - `Meta*` — нижнерегистровый ключ метаданных gRPC, который читает слушатель.
//
// Пары «заголовок ↔ метаданное» держатся вместе не соглашением, а пробой
// `TestKeyFormsAreOneKeyInThreeSurfaces`: метаданное обязано быть нижним
// регистром своего заголовка, а заголовок — каноническим.
//
// # Почему пакет лежит в фундаменте, а не у края
//
// Направление зависимостей одно: край импортирует фундамент, обратного пути нет
// by construction (каталог края закрыт `internal`). Объявление, лежащее у края,
// фундаменту недоступно — а читает ключи именно он.
//
// # Чего здесь НЕТ намеренно
//
// Здесь нет ПОЛИТИКИ: что край вычищает у клиента, что кладёт аннотатор, что
// пропускает мост — решения края, и живут они у края. Здесь — только имена и
// закрытый каталог свойств, о которых обеим сторонам надо договориться.
package principalwire

import "strings"

// Namespace — приставка КАЖДОГО ключа личности и контекста удостоверения на
// проводе. Всё под ней принадлежит краю по определению: значение выводится из
// проверенного удостоверения, и клиент не вправе его прислать.
//
// BridgePrefix — приставка моста REST→gRPC: входящий заголовок
// `Grpc-Metadata-Foo` приезжает слушателю метаданным `foo`. Обе поверхностные
// формы одного ключа обязаны судиться одинаково любой политикой над
// пространством.
// ImportPath — путь импорта ЭТОГО пакета.
//
// Стоит здесь затем, чтобы проверки дерева опознавали читателя ключа по ПОЛНОМУ
// пути импорта, а не по имени пакета: псевдоним (`pw "…/pkg/principalwire"`)
// пишется так же коротко, и разбор по имени обходился бы одной буквой в
// объявлении импорта.
const ImportPath = "github.com/PRO-Robotech/corelib/principalwire"

const (
	Namespace    = "x-kacho-"
	BridgePrefix = "grpc-metadata-"
)

// Подсемейства пространства. Приставка — то, чем ключуется вычистка чужого
// ввода: перечень известных имён устарел бы в день заведения одиннадцатого.
const (
	MetaPrincipalPrefix = "x-kacho-principal-"
	MetaTokenPrefix     = "x-kacho-token-" // #nosec G101 -- приставка имени ключа метаданных, не удостоверение
)

// Канонические имена HTTP-заголовков.
const (
	HeaderPrincipalType    = "X-Kacho-Principal-Type"
	HeaderPrincipalID      = "X-Kacho-Principal-Id"
	HeaderPrincipalDisplay = "X-Kacho-Principal-Display-Name"
	HeaderTokenACR         = "X-Kacho-Token-Acr"    // #nosec G101 -- имя HTTP-заголовка (утверждение `acr`), не удостоверение
	HeaderTokenJti         = "X-Kacho-Token-Jti"    // #nosec G101 -- имя HTTP-заголовка (утверждение `jti`), не удостоверение
	HeaderTokenScope       = "X-Kacho-Token-Scope"  // #nosec G101 -- имя HTTP-заголовка (утверждение `scope`), не удостоверение
	HeaderTokenExp         = "X-Kacho-Token-Exp"    // #nosec G101 -- имя HTTP-заголовка (утверждение `exp`), не удостоверение
	HeaderTokenAMR         = "X-Kacho-Token-Amr"    // #nosec G101 -- имя HTTP-заголовка (перечень способов подтверждения), не удостоверение
	HeaderTokenMfaAt       = "X-Kacho-Token-Mfa-At" // #nosec G101 -- имя HTTP-заголовка (момент подтверждения), не удостоверение

	// HeaderTokenBasicCredentialID — идентификатор СТРОКИ базового
	// удостоверения, а не сама строка: значение секрета этот заголовок не несёт.
	HeaderTokenBasicCredentialID = "X-Kacho-Token-Basic-Credential-Id" // #nosec G101 -- идентификатор строки, не удостоверение
)

// Мостовые формы тех же заголовков. Собираются приставкой, а не переписываются
// руками: вторая запись имени разошлась бы с первой молча.
const (
	HeaderGRPCMetaPrincipalType    = "Grpc-Metadata-" + HeaderPrincipalType
	HeaderGRPCMetaPrincipalID      = "Grpc-Metadata-" + HeaderPrincipalID
	HeaderGRPCMetaPrincipalDisplay = "Grpc-Metadata-" + HeaderPrincipalDisplay
	HeaderGRPCMetaTokenACR         = "Grpc-Metadata-" + HeaderTokenACR
	HeaderGRPCMetaTokenJti         = "Grpc-Metadata-" + HeaderTokenJti
	HeaderGRPCMetaTokenScope       = "Grpc-Metadata-" + HeaderTokenScope
)

// Нижнерегистровые ключи метаданных gRPC: ровно то, что читает слушатель
// (`metadata.MD` приводит имя к нижнему регистру).
const (
	MetaPrincipalType    = "x-kacho-principal-type"
	MetaPrincipalID      = "x-kacho-principal-id"
	MetaPrincipalDisplay = "x-kacho-principal-display-name"

	// MetaPrincipalDisplayBin — ДВОИЧНАЯ форма отображаемого имени.
	//
	// Значение обычного ключа метаданных ограничено печатаемой латиницей, и имя,
	// записанное кириллицей, роняет ВЕСЬ вызов, не дойдя до обработчика. Продукт
	// русскоязычный, поэтому имя едет формой, допускающей произвольные байты.
	// Тип и идентификатор остаются обычными ключами: они латиница by construction.
	MetaPrincipalDisplayBin = "x-kacho-principal-display-name-bin"

	MetaTokenACR   = "x-kacho-token-acr"    // #nosec G101 -- имя ключа метаданных, не удостоверение
	MetaTokenJti   = "x-kacho-token-jti"    // #nosec G101 -- имя ключа метаданных, не удостоверение
	MetaTokenScope = "x-kacho-token-scope"  // #nosec G101 -- имя ключа метаданных, не удостоверение
	MetaTokenAMR   = "x-kacho-token-amr"    // #nosec G101 -- имя ключа метаданных, не удостоверение
	MetaTokenMfaAt = "x-kacho-token-mfa-at" // #nosec G101 -- имя ключа метаданных, не удостоверение

	// MetaTokenBasicCredentialID — нижнерегистровая форма
	// [HeaderTokenBasicCredentialID]. Объявлена не ради потребителя за краем —
	// его нет, — а ради ЗАПРЕТА: набор ключей края ведётся этими именами, и
	// ключ, которого в нём нет, мост пропустил бы молча.
	MetaTokenBasicCredentialID = "x-kacho-token-basic-credential-id" // #nosec G101 -- идентификатор строки, не удостоверение
)

// Key — одна запись КАТАЛОГА: логический ключ во всех формах, в которых он
// существует, плюс два свойства, о которых обе стороны провода обязаны
// договориться.
type Key struct {
	// Name — имя записи для отказов и переписей. Не поверхностная форма.
	Name string
	// Ident — ИМЯ КОНСТАНТЫ этого пакета, несущей форму Meta.
	//
	// Записано данными затем, что после сведения объявления в одно читатели
	// пишут не литерал, а `principalwire.<Ident>` — и проверка, знающая только
	// литерал, перестала бы видеть читателя вовсе: ни красного, ни зелёного,
	// молчание. Соответствие имени значению держит проба
	// `TestCatalogueIdentsNameRealConstants`: она разбирает исходник этого же
	// пакета, поэтому расхождение невыразимо, а не запрещено соглашением.
	// Пусто, когда формы Meta нет.
	Ident string
	// Meta — нижнерегистровый ключ метаданных. Пусто, когда формы нет.
	Meta string
	// Header — канонический HTTP-заголовок. Пусто, когда формы нет
	// (`…-display-name-bin` живёт только метаданным).
	Header string

	// Fundament — фундамент (`pkg/grpcsrv`) читает этот ключ СВОИМ объявлением.
	//
	// Свойство объявлено ЗДЕСЬ, а держится гейтом дерева: ключ, помеченный
	// читаемым и не заведённый у фундамента, роняет проверку с ИМЕНАМИ ОБОИХ
	// файлов. Ровно тот рассинхрон, который прежде собирался чисто.
	Fundament bool

	// EdgeOnly — край производит ключ ДЛЯ СЕБЯ и за мост не пускает.
	//
	// Запись существует, чтобы односторонность была РЕШЕНИЕМ, а не умолчанием:
	// мост снимает приставку сам, поэтому голая форма пересекает его наравне с
	// мостовой, и «мостовой формы нет» односторонности не даёт.
	EdgeOnly bool
}

// keys — закрытый каталог. Порядок значим только для читаемости отказов.
var keys = []Key{
	{Name: "principal-type", Ident: "MetaPrincipalType", Meta: MetaPrincipalType, Header: HeaderPrincipalType, Fundament: true},
	{Name: "principal-id", Ident: "MetaPrincipalID", Meta: MetaPrincipalID, Header: HeaderPrincipalID, Fundament: true},
	{Name: "principal-display-name", Ident: "MetaPrincipalDisplay", Meta: MetaPrincipalDisplay, Header: HeaderPrincipalDisplay, Fundament: true},
	{Name: "principal-display-name-bin", Ident: "MetaPrincipalDisplayBin", Meta: MetaPrincipalDisplayBin, Fundament: true},
	{Name: "token-acr", Ident: "MetaTokenACR", Meta: MetaTokenACR, Header: HeaderTokenACR, Fundament: true},
	{Name: "token-jti", Ident: "MetaTokenJti", Meta: MetaTokenJti, Header: HeaderTokenJti},
	{Name: "token-scope", Ident: "MetaTokenScope", Meta: MetaTokenScope, Header: HeaderTokenScope},
	{Name: "token-exp", Header: HeaderTokenExp},
	{Name: "token-amr", Ident: "MetaTokenAMR", Meta: MetaTokenAMR, Header: HeaderTokenAMR, EdgeOnly: true},
	{Name: "token-mfa-at", Ident: "MetaTokenMfaAt", Meta: MetaTokenMfaAt, Header: HeaderTokenMfaAt, EdgeOnly: true},
	{Name: "token-basic-credential-id", Ident: "MetaTokenBasicCredentialID", Meta: MetaTokenBasicCredentialID, Header: HeaderTokenBasicCredentialID, EdgeOnly: true},
}

// Keys возвращает копию каталога. Копия, а не срез-хранитель: каталог —
// объявление, и вызывающий, дописавший в него запись, объявлял бы контракт из
// своего пакета.
func Keys() []Key {
	out := make([]Key, len(keys))
	copy(out, keys)
	return out
}

// IsEdgeOnly — остаётся ли ключ на краю (мост его не пропускает).
//
// Имя приходит нормализованным: нижний регистр, без мостовой приставки — так
// его отдаёт [NamespaceKey], и обе поверхностные формы сводятся к одному имени
// ещё до этого вопроса.
func IsEdgeOnly(meta string) bool {
	for _, k := range keys {
		if k.Meta == meta {
			return k.EdgeOnly
		}
	}
	return false
}

// NamespaceKey приводит входящее имя заголовка или ключа метаданных к голой
// нижнерегистровой форме и сообщает, принадлежит ли оно НАШЕМУ пространству.
// Мостовая приставка снимается первой, поэтому `Grpc-Metadata-X-Kacho-Admin` и
// `x-kacho-admin` дают одно и то же ("x-kacho-admin", true).
func NamespaceKey(key string) (string, bool) {
	bare := Bare(key)
	if !strings.HasPrefix(bare, Namespace) {
		return "", false
	}
	return bare, true
}

// Bare — голая нижнерегистровая форма имени: нижний регистр и снятая мостовая
// приставка. Пространству имён может и не принадлежать.
func Bare(key string) string {
	return strings.TrimPrefix(strings.ToLower(key), BridgePrefix)
}

// IsIdentityShaped — имеет ли голое имя ФОРМУ ключа личности НЕЗАВИСИМО от
// того, чьё пространство имён над ним стоит: `x-<обозначение>-principal-<поле>`
// либо `x-<обозначение>-token-<поле>`.
//
// # Зачем свойство ФОРМЫ, когда есть своё пространство имён
//
// Переименование пространства — ровно тот переход, ради которого написан этот
// пакет, и в окне перехода край и служба собраны из разных деревьев. Приёмник,
// умеющий спросить только про СВОЮ приставку, о чужой не узнаёт ничего:
// пересланная личность для него не приезжает вовсе, и он читает это как
// законную безымянность. Форма — единственный признак, по которому «личность
// приехала под именем, которого я не читаю» отличимо от «личности не было».
//
// Признак нарочно узкий: он требует и подсемейства (`principal-`/`token-`), и
// непустого поля за ним. Соседи по проводу (`x-request-id`, `x-forwarded-for`,
// `x-envoy-…`) под него не подпадают, поэтому отказ не приходит на чужую
// служебную разметку.
func IsIdentityShaped(bare string) bool {
	if !strings.HasPrefix(bare, "x-") {
		return false
	}
	rest := bare[len("x-"):]
	// Обозначение продукта — первый сегмент, непустой.
	i := strings.IndexByte(rest, '-')
	if i <= 0 {
		return false
	}
	rest = rest[i+1:]
	for _, family := range [...]string{"principal-", "token-"} {
		if strings.HasPrefix(rest, family) && len(rest) > len(family) {
			return true
		}
	}
	return false
}

// IsOurs — принадлежит ли голое имя пространству имён ЭТОЙ сборки.
func IsOurs(bare string) bool { return strings.HasPrefix(bare, Namespace) }

// WireNameKind — чем является строковое значение по отношению к пространству
// имён личности. Нужен проверкам дерева, чтобы отказ называл предмет, а не
// «строка похожа на нашу».
type WireNameKind int

// Виды имён пространства.
const (
	// WireNameNone — к пространству имён личности значение не относится.
	WireNameNone WireNameKind = iota
	// WireNameNamespace — само пространство имён.
	WireNameNamespace
	// WireNameFamily — приставка подсемейства (`x-<обозначение>-principal-`,
	// `x-<обозначение>-token-`). На ней ключуется вычистка чужого ввода, поэтому
	// второе её объявление так же опасно, как второе объявление ключа.
	WireNameFamily
	// WireNameKey — ключ формы личности.
	WireNameKey
)

// ClassifyWireName относит строковое значение к пространству имён личности.
//
// # Почему признак СТРУКТУРНЫЙ, а не «начинается с нашей приставки»
//
// Переименование пространства — тот самый переход, ради которого написан этот
// пакет. Признак, знающий только сегодняшнюю приставку, не увидел бы второго
// объявления, сделанного уже под новой, — то есть промолчал бы ровно в тот
// день, когда нужен.
//
// # Чего признак НЕ относит к пространству — названо, а не спрятано
//
// Общая приставка ещё не делает имя ключом личности: `x-kacho-hook-token`
// (общий секрет крючка службы личности), `x-kacho-admin`, `x-kacho-project-id`
// — законные имена вне этого контракта, и запрет объявлять их значил бы
// запрет на соседние подсистемы. Различает подсемейство и непустое поле за ним.
func ClassifyWireName(v string) WireNameKind {
	if v == Namespace {
		return WireNameNamespace
	}
	if familyPrefix(v) {
		return WireNameFamily
	}
	if IsIdentityShaped(v) {
		return WireNameKey
	}
	return WireNameNone
}

// familyPrefix — является ли значение ровно приставкой подсемейства.
func familyPrefix(v string) bool {
	if !strings.HasPrefix(v, "x-") {
		return false
	}
	rest := v[len("x-"):]
	i := strings.IndexByte(rest, '-')
	if i <= 0 {
		return false
	}
	rest = rest[i+1:]
	return rest == "principal-" || rest == "token-"
}
