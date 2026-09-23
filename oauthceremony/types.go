// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import "time"

// ── Словари ─────────────────────────────────────────────────────────────────

// GrantKind — способ получения токена (RFC 6749 §1.3, RFC 7523).
type GrantKind string

// Способы, которые церемония обслуживает. Перечень ЗАКРЫТ: значение вне его
// отвергается в Exchange, а не уезжает в движок в надежде, что он разберётся.
//
// Утверждения по RFC 7523 (`urn:ietf:params:oauth:grant-type:jwt-bearer`)
// здесь НЕТ, и это решение, а не пропуск: этот способ требует своего порта
// ключей доверенного выпускающего, которого фундамент пока не объявляет.
// Объявить слово без обработчика значило бы обещать поведение, которого нет:
// запрос с таким `grant_type` получал бы «неизвестный запрос» вместо честного
// «способ не поддерживается». Способ заводится отдельной фазой вместе со
// своим портом.
//
// `client_credentials` здесь тоже НЕТ: машинные клиенты службы доступа
// получают токен своей выдачей, а не церемонией (решение по эпику, К3).
// Слово вне словаря отвергается в Exchange до движка, и обработчика этого
// вида в провязке церемонии нет.
const (
	GrantAuthorizationCode GrantKind = "authorization_code"
	GrantRefreshToken      GrantKind = "refresh_token"
)

// GrantKinds возвращает словарь целиком — тому, кто строит метаданные
// сервера обнаружения или проверяет запись клиента.
func GrantKinds() []GrantKind {
	return []GrantKind{GrantAuthorizationCode, GrantRefreshToken}
}

// Declared отвечает, входит ли способ в словарь. Пустое значение НЕ входит:
// «способ не назван» означало бы «любой».
func (g GrantKind) Declared() bool {
	for _, k := range GrantKinds() {
		if k == g {
			return true
		}
	}
	return false
}

// ResponseKind — тип ответа точки авторизации (RFC 6749 §3.1.1).
type ResponseKind string

// Типы ответа, которые церемония обслуживает.
//
// ResponseKindToken (неявный поток) СОЗНАТЕЛЬНО ОТСУТСТВУЕТ: OAuth 2.1
// исключает его, и заводить его здесь значило бы завести поверхность, которую
// нечем защитить.
const (
	ResponseKindCode ResponseKind = "code"
)

// ResponseDelivery — способ доставки ответа точки авторизации; спецификация
// `OAuth 2.0 Multiple Response Type Encoding Practices`.
type ResponseDelivery string

// Способы доставки. DeliveryDefault означает «выбирает движок по типу
// ответа» и отличается от «доставка не названа» тем, что назван ЯВНО.
const (
	DeliveryDefault  ResponseDelivery = ""
	DeliveryQuery    ResponseDelivery = "query"
	DeliveryFragment ResponseDelivery = "fragment"
	DeliveryFormPost ResponseDelivery = "form_post"
)

// TokenKind — вид артефакта, выпускаемого церемонией.
type TokenKind string

// Виды артефактов.
const (
	TokenKindAccess            TokenKind = "access_token"
	TokenKindRefresh           TokenKind = "refresh_token"
	TokenKindAuthorizationCode TokenKind = "authorization_code"
	TokenKindIdentity          TokenKind = "id_token"
	// TokenKindUnspecified — вид не назван. Отдельно от пустой строки в
	// значении поля: в интроспекции «вид определить не удалось» —
	// законный исход, и он обязан быть выразим.
	TokenKindUnspecified TokenKind = ""
)

// ClientAuthMethod — способ, которым клиент доказывает себя точке токена
// (RFC 6749 §2.3).
//
// Перечень ЗАКРЫТ: секрет либо ничего. Утверждения клиента (RFC 7523 §2.2)
// церемония не обслуживает — ни способом здесь, ни полем в Additional: движок
// отвергает такое утверждение (см. ClientRegistration).
type ClientAuthMethod string

// Способы доказательства клиента.
const (
	// ClientAuthNone — публичный клиент, секрета нет.
	ClientAuthNone ClientAuthMethod = "none"
	// ClientAuthBasic — секрет в заголовке Authorization: Basic.
	ClientAuthBasic ClientAuthMethod = "client_secret_basic"
	// ClientAuthPost — секрет в теле запроса.
	ClientAuthPost ClientAuthMethod = "client_secret_post"
)

// ScopeMatching — правило сопоставления запрошенной области с разрешённой.
type ScopeMatching uint8

// Правила сопоставления.
const (
	// ScopeMatchingUnspecified — правило не названо. Отвергается в New:
	// молчаливый выбор здесь означал бы выбор политики безопасности за
	// того, кто её не назвал.
	ScopeMatchingUnspecified ScopeMatching = iota
	// ScopeMatchingExact — совпадение строка-в-строку.
	ScopeMatchingExact
	// ScopeMatchingWildcard — совпадение с образцом `a.b.*`.
	ScopeMatchingWildcard
)

// ── Запись клиента ──────────────────────────────────────────────────────────

// ClientRegistration — запись клиента в НАШИХ терминах: данные, а не
// поведение.
//
// # Почему структура, а не интерфейс
//
// Движок объявляет клиента интерфейсом из семи методов и расширяет его ещё
// тремя интерфейсами, которые он проверяет утверждением типа. Реализуй службa
// такой интерфейс — и состав её обязанностей менялся бы при обновлении
// апстрима молча: новый необязательный интерфейс просто перестал бы
// подхватываться. Структура делает состав ЯВНЫМ: новое поле видно в диффе.
//
// # Чего в записи НЕТ
//
// Набора ключей клиента, адресов объектов запроса и закреплённого способа
// доказательства. Всё это нужно утверждениям клиента и объектам запроса
// OpenID Connect, а их церемония не обслуживает: конфиденциальный клиент
// доказывает себя секретом — заголовком либо телом запроса (RFC 6749 §2.3.1),
// публичный не доказывает себя вовсе и обязан нести PKCE.
type ClientRegistration struct {
	// ClientID — публичный идентификатор клиента.
	ClientID string

	// HashedSecret — хеш секрета. ИМЕННО ХЕШ: чистый секрет в фундамент
	// не передаётся никогда, сверку исполняет движок хешером церемонии.
	HashedSecret []byte

	// RotatedHashedSecrets — хеши прежних секретов на время оборота.
	RotatedHashedSecrets [][]byte

	// RedirectURIs — точный перечень разрешённых адресов возврата.
	RedirectURIs []string

	// GrantKinds — способы получения токена, разрешённые клиенту.
	GrantKinds []GrantKind

	// ResponseKinds — разрешённые СОЧЕТАНИЯ типов ответа. Каждый элемент —
	// одно сочетание; составное сочетание записывается через пробел
	// ("code id_token"), как того требует RFC 6749 §3.1.1.
	ResponseKinds []string

	// Scopes — области, которые клиенту дозволено запрашивать.
	Scopes []string

	// Audiences — получатели, которых клиенту дозволено запрашивать.
	Audiences []string

	// Public — клиент не может хранить секрет (приложение в браузере,
	// мобильное приложение). Для такого клиента обязателен PKCE.
	Public bool

	// ResponseDeliveries — разрешённые способы доставки ответа. Пустой
	// перечень означает «только тот, что движок выберет по умолчанию».
	ResponseDeliveries []ResponseDelivery
}

// ── Записи хранения ─────────────────────────────────────────────────────────

// SessionRecord — состояние сеанса, переживающее запрос.
type SessionRecord struct {
	// Subject — устойчивый идентификатор того, от чьего имени выдан грант.
	Subject string

	// Username — человекочитаемое имя. Необязательно; уезжает в ответ
	// интроспекции.
	Username string

	// ExpiresAt — срок годности по каждому виду артефакта, записанный
	// церемонией в миг выпуска. Служба возвращает карту такой, какой её
	// получила: ключ, который церемония записала, пропасть не может — без
	// срока токена обновления движок считает токен бессрочным. Нулевое
	// время церемония не пишет, и ноль в записи из хранилища — нарушение
	// контракта порта (ErrPortContract), а не «ключа нет».
	ExpiresAt map[TokenKind]time.Time

	// NotAfter — граница годности СЕМЕЙСТВА гранта по видам артефактов:
	// ни один артефакт вида, выпущенный по этому гранту, — ни первый, ни
	// выпущенный оборотом, — не живёт дольше. Отсутствие ключа — границы
	// нет. Служба хранит границу вместе с сеансом и отдаёт её в каждой
	// выборке: без неё оборот назначил бы срок из настроек поверх и
	// продлил бы семейство за границу. Нулевое время границей не бывает
	// (выдача его отвергает), и ноль в записи из хранилища — нарушение
	// контракта порта (ErrPortContract), а не «ключа нет»: нулевая граница
	// сжала бы к себе срок токена обновления, а нулевой срок движок читает
	// как «без срока».
	NotAfter map[TokenKind]time.Time

	// Claims — дополнительные утверждения, которые служба кладёт в сеанс.
	// Переживают сериализацию через JSON, поэтому значения обязаны быть
	// представимы в JSON.
	Claims map[string]any
}

// GrantRecord — грант в том виде, в каком он лежит в хранилище службы.
//
// Это ЕДИНСТВЕННАЯ форма, в которой грант пересекает границу порта: ни код
// авторизации, ни токен обновления не возят с собой ничего, кроме этой
// записи и своей подписи.
type GrantRecord struct {
	// GrantID — идентификатор гранта. По нему отзываются ВСЕ артефакты
	// одного гранта (RFC 7009 §2.1, замечание о реализации).
	//
	// Его чеканит служба крючком Config.NewGrantID при выдаче кода; записи
	// токенов наследуют его от записи кода. Запись, которую хранилище отдаёт
	// живой, обязана его нести: пустой — нарушение контракта порта
	// (ErrPortContract), а не повод движку начеканить свой.
	GrantID string

	// ClientID — клиент, которому выдан грант.
	ClientID string

	// IssuedAt — когда грант возник.
	IssuedAt time.Time

	// RequestedScopes / GrantedScopes — что просили и что дали. Хранятся
	// ОБА: отказ в области — это разница между ними, и она обязана быть
	// восстановима из записи, а не вычисляема заново.
	RequestedScopes []string
	GrantedScopes   []string

	// RequestedAudiences / GrantedAudiences — то же про получателей.
	RequestedAudiences []string
	GrantedAudiences   []string

	// Form — протокольные поля запроса, породившего грант. Нужны движку
	// для PKCE и для сверки адреса возврата при обмене кода.
	Form map[string][]string

	// Session — состояние сеанса.
	Session SessionRecord
}

// ── Запрос авторизации ──────────────────────────────────────────────────────

// AuthorizationRequest — запрос к точке авторизации в наших терминах.
//
// # Почему поля, а не сырая карта
//
// Сырая карта сделала бы контракт нечитаемым, а опечатку в имени поля —
// невидимой. Поэтому поля RFC названы, а всё прочее живёт в Additional.
//
// # Почему значение выразимо ровно одним способом
//
// Ключ, дублирующий именованное поле, в Additional ЗАПРЕЩЁН и даёт
// ErrCeremonyMisuse. Иначе у одного значения было бы два места, и они
// разошлись бы — молча и в сторону меньшей строгости.
type AuthorizationRequest struct {
	// ClientID — `client_id`.
	ClientID string

	// RedirectURI — `redirect_uri`. Пусто означает «взять единственный
	// зарегистрированный»; при нескольких зарегистрированных пустое
	// значение — отказ, и его выносит движок.
	RedirectURI string

	// ResponseKinds — `response_type`, разобранный по пробелу.
	ResponseKinds []ResponseKind

	// Scopes — `scope`, разобранный по пробелу.
	Scopes []string

	// Audiences — `audience`.
	Audiences []string

	// State — `state`. Непрозрачное значение клиента против CSRF.
	State string

	// Delivery — `response_mode`.
	Delivery ResponseDelivery

	// Additional — прочие протокольные поля: `code_challenge`,
	// `code_challenge_method`, `nonce`, `prompt`, `request_uri`, поля
	// расширений. Дублировать именованные поля запрещено.
	Additional map[string][]string
}

// AuthorizationGrant — решение службы о том, что выдать. Приходит ПОСЛЕ
// того, как человек опознан и согласие получено; церемония ни того, ни
// другого не исполняет и исполнять не должна.
type AuthorizationGrant struct {
	// Subject — от чьего имени выдаётся грант. Пусто — отказ.
	Subject string

	// Username — человекочитаемое имя. Необязательно.
	Username string

	// GrantedScopes — области, которые служба РЕШИЛА выдать. Выдача может
	// сузить запрос, но не расширить: область, не покрытая запрошенными по
	// правилу Config.ScopeMatching, отвергается ЦЕРЕМОНИЕЙ
	// (ErrCeremonyMisuse) до выпуска кода. Движок выданное не сверяет.
	GrantedScopes []string

	// GrantedAudiences — получатели, которых служба решила выдать. Тоже не
	// шире запроса: получатель, которого нет среди запрошенных (точное
	// совпадение), отвергается церемонией (ErrCeremonyMisuse).
	GrantedAudiences []string

	// Claims — дополнительные утверждения в сеанс.
	Claims map[string]any

	// ExpiresAt — ГРАНИЦА годности по видам артефактов для всего семейства
	// гранта: код, первая пара и каждая пара оборота живут не дольше. Срок
	// каждого артефакта — меньшее из границы и срока из настроек церемонии,
	// отсчитанного от его выпуска. Вид без записи — срок из настроек.
	// Нулевое время — не граница и отвергается церемонией по имени вида
	// (ErrCeremonyMisuse) до выпуска кода: движок читает нулевой срок
	// токена обновления как «без срока», и граница-ноль сделала бы семейство
	// бессрочным.
	ExpiresAt map[TokenKind]time.Time
}

// AuthorizationResult — ответ точки авторизации, собранный ДВИЖКОМ по RFC и
// переложенный в наши термины.
//
// Церемония НЕ ПИШЕТ в сеть: она возвращает то, что следует написать. Кто и
// как это отправит — дело поверхности.
type AuthorizationResult struct {
	// Status — состояние ответа HTTP, которое следует выставить.
	Status int

	// Headers — заголовки, которые следует выставить. Для доставки
	// query и fragment сюда входит Location.
	Headers map[string][]string

	// Body — тело ответа. Непусто только для доставки form_post: там
	// ответ — самоотправляющаяся форма HTML, и собирает её движок.
	Body []byte

	// RedirectURI — адрес возврата целиком, включая параметры. Пусто для
	// доставки form_post.
	RedirectURI string

	// Parameters — параметры ответа, разобранные из адреса возврата:
	// `code`, `state`, `scope`, либо `error`+`error_description` на
	// пути отказа.
	Parameters map[string][]string

	// Delivery — способ доставки, которым ответ собран. Никогда не
	// DeliveryDefault: к этому мигу выбор уже сделан.
	Delivery ResponseDelivery
}

// ── Точка токена ────────────────────────────────────────────────────────────

// TokenRequest — запрос к точке токена.
type TokenRequest struct {
	// Grant — `grant_type`. Обязательно, и обязано входить в словарь.
	Grant GrantKind

	// ClientID — `client_id`.
	ClientID string

	// ClientSecret — секрет клиента В ЧИСТОМ ВИДЕ, как его прислал
	// клиент. Живёт ровно до конца вызова: ни в GrantRecord, ни в
	// ProtocolError, ни в журнале он не оседает.
	ClientSecret string

	// AuthMethod — каким способом доказывать клиента. Пустое значение
	// означает ClientAuthNone при пустом секрете и ClientAuthBasic при
	// непустом; иного умолчания здесь быть не может, и оно названо, а не
	// подразумевается.
	AuthMethod ClientAuthMethod

	// Code — `code`. Только для GrantAuthorizationCode.
	Code string

	// RedirectURI — `redirect_uri`. Обязателен, если был в запросе
	// авторизации (RFC 6749 §4.1.3).
	RedirectURI string

	// CodeVerifier — `code_verifier` (PKCE, RFC 7636).
	CodeVerifier string

	// RefreshToken — `refresh_token`. Только для GrantRefreshToken.
	RefreshToken string

	// Scopes — `scope`. Сужение относительно гранта.
	Scopes []string

	// Audiences — `audience`.
	Audiences []string

	// Additional — прочие протокольные поля: поля расширений. Утверждение
	// клиента (`client_assertion`) сюда класть бесполезно — движок его
	// отвергает (см. ClientAuthMethod).
	Additional map[string][]string
}

// TokenResult — ответ точки токена (RFC 6749 §5.1).
type TokenResult struct {
	// AccessToken — токен доступа.
	AccessToken string

	// TokenType — тип токена доступа; для церемонии всегда "bearer".
	TokenType string

	// ExpiresIn — сколько токену доступа осталось жить.
	ExpiresIn time.Duration

	// RefreshToken — токен обновления. Пусто, если не выдавался.
	RefreshToken string

	// IdentityToken — токен личности (OIDC). Пусто, если не выдавался.
	IdentityToken string

	// Scopes — выданные области.
	Scopes []string

	// Additional — прочие поля ответа, положенные обработчиками
	// расширений. Именованные поля выше сюда НЕ дублируются.
	Additional map[string]any
}

// ── Интроспекция ────────────────────────────────────────────────────────────

// IntrospectionRequest — запрос интроспекции (RFC 7662 §2.1).
type IntrospectionRequest struct {
	// Token — предъявленный артефакт.
	Token string

	// KindHint — подсказка о виде артефакта. TokenKindUnspecified
	// означает «подсказки нет», и это законное значение.
	KindHint TokenKind

	// Scopes — области, наличие которых требуется подтвердить.
	Scopes []string

	// ClientID / ClientSecret / AuthMethod — чем доказывает себя тот, кто
	// спрашивает. Интроспекция без доказательства запрещена RFC 7662 §2.1.
	ClientID     string
	ClientSecret string
	AuthMethod   ClientAuthMethod
}

// IntrospectionResult — ответ интроспекции (RFC 7662 §2.2).
//
// Active=false — ЗАКОННЫЙ ИСХОД, а не отказ: так отвечают и на выдуманный
// артефакт, и на погашенный. Прочие поля при Active=false пусты по
// требованию RFC — иначе интроспекция стала бы прибором для перебора.
type IntrospectionResult struct {
	Active bool

	// Kind — вид артефакта, если его удалось определить.
	Kind TokenKind

	// AccessTokenType — тип токена доступа ("bearer"). Пусто для токена
	// обновления.
	AccessTokenType string

	// GrantID — идентификатор гранта, породившего артефакт: тот, что выдал
	// крючок Config.NewGrantID.
	GrantID string

	ClientID  string
	Subject   string
	Username  string
	Scopes    []string
	Audiences []string

	// IssuedAt — когда возник грант.
	IssuedAt time.Time

	// ExpiresAt — когда артефакт перестаёт быть годным. Нулевое время
	// означает «срок не назначен», а не «уже истёк».
	ExpiresAt time.Time

	// Claims — дополнительные утверждения сеанса.
	Claims map[string]any
}

// ── Отзыв ───────────────────────────────────────────────────────────────────

// RevocationRequest — запрос отзыва (RFC 7009 §2.1).
type RevocationRequest struct {
	// Token — отзываемый артефакт.
	Token string

	// KindHint — подсказка о виде.
	KindHint TokenKind

	ClientID     string
	ClientSecret string
	AuthMethod   ClientAuthMethod
}
