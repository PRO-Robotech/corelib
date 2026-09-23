// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
	enginehandler "github.com/PRO-Robotech/corelib/internal/oauth2/handler/oauth2"
	engineproofkey "github.com/PRO-Robotech/corelib/internal/oauth2/handler/pkce"
	enginehmac "github.com/PRO-Robotech/corelib/internal/oauth2/token/hmac"
)

// ── Настройки ───────────────────────────────────────────────────────────────

// RefreshTokenIssuance — когда выдавать токен обновления.
type RefreshTokenIssuance uint8

// Правила выдачи токена обновления.
const (
	// RefreshTokenIssuanceUnspecified — правило не названо. Отвергается
	// в New: молчаливый выбор здесь решал бы за службу, живёт ли у неё
	// бессрочный доступ.
	RefreshTokenIssuanceUnspecified RefreshTokenIssuance = iota
	// RefreshTokenIssuanceOnScope — токен обновления выдаётся, только
	// если выдана одна из RefreshTokenScopes.
	RefreshTokenIssuanceOnScope
	// RefreshTokenIssuanceAlways — токен обновления выдаётся при каждом
	// обмене, где он применим.
	RefreshTokenIssuanceAlways
)

// Config — настройки церемонии.
//
// Ни одно поле не имеет молчаливого умолчания: New отвергает набор, в котором
// поле не названо, называя поле поимённо. Умолчание, подставленное молча, —
// решение, принятое за того, кто его не принимал; здесь такие решения
// касаются сроков жизни токенов и строгости сопоставления областей.
type Config struct {
	// Issuer — кем выпущены артефакты. Обязан быть абсолютным адресом
	// (`https://iam.example.net`). Из него выводятся адреса точек,
	// которые церемония подставляет в синтезированные запросы.
	Issuer string

	// SigningSecret — ключ подписи артефактов. Не короче 32 байт: подпись
	// HMAC-SHA256 на более коротком ключе не даёт заявленной стойкости.
	SigningSecret []byte

	// RotatedSigningSecrets — прежние ключи подписи на время оборота.
	// Ими артефакты ПРОВЕРЯЮТСЯ, но не подписываются.
	RotatedSigningSecrets [][]byte

	// AccessTokenLifespan, RefreshTokenLifespan, AuthorizationCodeLifespan
	// — сроки жизни артефактов. Все три обязаны быть положительными.
	AccessTokenLifespan       time.Duration
	RefreshTokenLifespan      time.Duration
	AuthorizationCodeLifespan time.Duration

	// ScopeMatching — правило сопоставления областей.
	ScopeMatching ScopeMatching

	// RefreshTokenIssuance — когда выдавать токен обновления.
	RefreshTokenIssuance RefreshTokenIssuance

	// RefreshTokenScopes — области, дающие токен обновления. Обязателен
	// и непуст при RefreshTokenIssuanceOnScope; обязан быть пуст при
	// RefreshTokenIssuanceAlways, иначе у правила было бы два места.
	RefreshTokenScopes []string

	// RequireProofKey — требовать PKCE (RFC 7636) от ВСЕХ клиентов.
	RequireProofKey bool

	// RequireProofKeyForPublicClients — требовать PKCE от публичных
	// клиентов. Действует независимо от RequireProofKey.
	RequireProofKeyForPublicClients bool

	// SecretHashCost — цена хеширования секрета клиента. Обязана быть в
	// пределах [10, 15]: ниже — подбор дёшев, выше — сверка секрета
	// становится прибором для отказа в обслуживании.
	SecretHashCost int

	// MinParameterEntropy — минимальная длина `state` и `nonce` в
	// символах. Обязана быть не меньше 8 (RFC 6749 §10.10 требует не
	// менее 128 бит для непредсказуемых значений; 8 символов — нижняя
	// граница, ниже которой параметр перестаёт быть защитой от CSRF).
	MinParameterEntropy int

	// PortTimeout — срок ОДНОГО вызова порта хранения.
	PortTimeout time.Duration

	// OperationTimeout — срок ВСЕЙ операции церемонии. Обязан быть не
	// меньше PortTimeout: иначе первый же вызов порта не уложился бы в
	// операцию, и срок порта не значил бы ничего.
	OperationTimeout time.Duration

	// NewGrantID — крючок чеканки идентификатора гранта. Обязателен.
	//
	// Идентификатор гранта — ключ семейства: по нему служба отзывает все
	// артефакты одной выдачи и под ним ведёт свою запись семейства, форму
	// которой держит ограничение её схемы. Поэтому его выбирает СЛУЖБА:
	// движок, получив запрос без идентификатора, чеканит свой при первом
	// чтении, и ключ, которого служба не выбирала, лёг бы во все её записи.
	// Приставки платформы — предмет пакета `ids`, а не этого.
	//
	// Церемония зовёт крючок ровно один раз на грант — в CompleteAuthorization,
	// после своих проверок выдачи и до выпуска кода — и ставит идентификатор
	// запросу движка раньше, чем тот его прочтёт. Разбор запроса, отказ в
	// согласии, обмен кода, оборот токена обновления, интроспекция и отзыв
	// крючка не зовут: идентификатор едет из записи хранилища.
	//
	// Вызову назначается срок Config.PortTimeout, как вызову порта. Отказ
	// крючка — отказ выдачи (случай разбирается, как отказ порта), и записи
	// кода в хранилище не появляется. Пустой идентификатор — нарушение
	// контракта (ErrPortContract): на пустом движок начеканил бы свой.
	// Уникальность — забота службы: церемония её не сверяет, её держит ключ
	// записи семейства в хранилище службы.
	NewGrantID func(ctx context.Context) (string, error)
}

// Ceremony — церемония OAuth 2.0 платформы.
//
// Значение НЕИЗМЕНЯЕМО после New и пригодно для одновременного использования
// из многих исполнителей: собственного изменяемого состояния у него нет, а
// методы настроек движка (engine.Config) на пути запроса в настройки не пишут —
// в тех пределах, в каких это держат пробы. Две пробы держат каждая свою часть:
//
//   - TestEngineSettingsBuiltByNewAreOnlyReadOnTheRequestPath вызывает каждый
//     метод настроек, собранных New, и ловит ЗАМЕНУ значения поля — ленивое
//     заполнение пустого поля в том числе;
//   - TestEngineSettingsMethodsWriteNoFieldContent разбирает исходник методов
//     настроек и ловит запись в СОДЕРЖИМОЕ поля — элемент карты или среза,
//     значение под указателем, — которой первая проба не видит, но только в
//     тех формах записи, которые разбор знает. Это ограждение известных форм,
//     а не доказательство отсутствия записи: разбор без типов полным не
//     бывает, и держимые им формы перечислены в шапке
//     engineconfig_static_internal_test.go.
//
// Чего не судит ни одна из них: запись в поле того же значения, что там уже
// лежит; запись в содержимое в формах слепой зоны разбора (перечень
// blindZoneForms, не замкнутый) — замыкание, отдающее содержимое; часть
// локального значения, получившая содержимое присваиванием; канал, через
// который прошло содержимое; получатель отданного содержимого, то есть код
// движка, пишущий в то, что вернул ему геттер; прочее состояние движка —
// обработчики, стратегию подписи. У форм слепой зоны держателя нет ни на одном
// пути запроса: статическая проба на них молчит — её ноль находок и
// неразобранных стоит и при такой записи в дереве (это утверждает
// TestNamedBlindZoneFormsStaySilent), — а перепись по тождеству содержимого не
// видит. -race проба TestConcurrentExchangesOnAFreshCeremonyShareNoEngineState
// судит не форму записи, а только гонку: запись и чтение одного места из
// одновременных обменов, не упорядоченные синхронизацией, — на пути обмена и
// лишь когда они случились в её прогоне. Исполненная её прогоном запись этим
// ещё не поймана: запись, которую ни один другой обмен не делит с ней без
// синхронизации, для детектора не гонка, сколько бы раз она ни исполнилась.
type Ceremony struct {
	provider engine.OAuth2Provider
	cfg      Config

	// bridge — мост к портам службы. Церемонии он нужен помимо движка ровно
	// для одного: отозвать семейство повторённого кода или токена обновления
	// ВНЕ единицы работы движка (см. revokeReplayedFamily).
	bridge *storageBridge

	authorizeURL string
	tokenURL     string
}

// New собирает церемонию.
//
// Отвергает всё, что не названо, ДО первого запроса: проверка настроек в
// рабочем пути означала бы, что негодная сборка живёт до первого обращения и
// падает на пользователе.
func New(cfg Config, ports Ports) (*Ceremony, error) {
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	if err := validatePorts(ports); err != nil {
		return nil, err
	}

	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || !issuer.IsAbs() || issuer.Host == "" {
		return nil, misuse("Config.Issuer must be an absolute URL with a host, for example https://iam.example.net")
	}

	authorizeURL := issuer.JoinPath("oauth2", "authorize").String()
	tokenURL := issuer.JoinPath("oauth2", "token").String()

	engineCfg := &engine.Config{
		AccessTokenLifespan:            cfg.AccessTokenLifespan,
		RefreshTokenLifespan:           cfg.RefreshTokenLifespan,
		AuthorizeCodeLifespan:          cfg.AuthorizationCodeLifespan,
		GlobalSecret:                   cfg.SigningSecret,
		RotatedGlobalSecrets:           cfg.RotatedSigningSecrets,
		HashCost:                       cfg.SecretHashCost,
		MinParameterEntropy:            cfg.MinParameterEntropy,
		ScopeStrategy:                  scopeStrategyOf(cfg.ScopeMatching),
		AudienceMatchingStrategy:       engine.DefaultAudienceMatchingStrategy,
		EnforcePKCE:                    cfg.RequireProofKey,
		EnforcePKCEForPublicClients:    cfg.RequireProofKeyForPublicClients,
		EnablePKCEPlainChallengeMethod: false,
		SendDebugMessagesToClients:     false,
		AccessTokenIssuer:              cfg.Issuer,
		IDTokenIssuer:                  cfg.Issuer,
		TokenURL:                       tokenURL,
		RefreshTokenScopes:             refreshTokenScopesOf(cfg),
		// Поля запроса авторизации, доезжающие до записи кода. Сверх
		// умолчания движка (`code`, `redirect_uri`) — `code_challenge`: по
		// нему мост узнаёт, что код привязан к PKCE, и пропавшая запись PKCE
		// у такого кода — повтор, а не «данных PKCE нет» (см.
		// storageBridge.GetPKCERequestSession).
		SanitationWhiteList: []string{"code", "redirect_uri", "code_challenge"},
	}
	// Хешер секрета клиента назван ЗДЕСЬ, а не оставлен движку. Геттер движка
	// заполняет неназванное поле ЛЕНИВО — первым вызовом и без синхронизации,
	// — и первые одновременные обмены писали бы его из одного запроса под
	// чтением из другого: гонка данных. Значение — то же, что завёл бы движок:
	// bcrypt, цена — SecretHashCost. Два других лениво заполняемых поля,
	// ScopeStrategy и AudienceMatchingStrategy, названы выше; четвёртое,
	// JWKSFetcherStrategy, не названо намеренно — путь запроса церемонии его
	// геттера не достигает. Предикат всех трёх утверждений — проба
	// TestEngineSettingsBuiltByNewAreOnlyReadOnTheRequestPath.
	engineCfg.ClientSecretsHasher = &engine.BCrypt{Config: engineCfg}

	bridge, store := newStorageBridge(ports, cfg.PortTimeout)
	coreStore, ok := store.(enginehandler.CoreStorage)
	if !ok {
		// Недостижимо: соответствие моста закреплено утверждениями
		// времени сборки ниже по файлу. Ветка существует, чтобы
		// утверждение типа не могло паниковать в рабочем пути.
		return nil, misuse("the storage bridge does not satisfy the engine storage contract")
	}
	proofKeyStore, ok := store.(engineproofkey.PKCERequestStorage)
	if !ok {
		return nil, misuse("the storage bridge does not satisfy the proof-key storage contract")
	}
	revocationStore, ok := store.(enginehandler.TokenRevocationStorage)
	if !ok {
		return nil, misuse("the storage bridge does not satisfy the revocation storage contract")
	}
	clientStore, ok := store.(engine.Storage)
	if !ok {
		return nil, misuse("the storage bridge does not satisfy the client storage contract")
	}

	strategy := enginehandler.NewHMACSHAStrategyUnPrefixed(
		&enginehmac.HMACStrategy{Config: engineCfg}, engineCfg)

	explicitGrant := &enginehandler.AuthorizeExplicitGrantHandler{
		AccessTokenStrategy:    strategy,
		RefreshTokenStrategy:   strategy,
		AuthorizeCodeStrategy:  strategy,
		CoreStorage:            coreStore,
		TokenRevocationStorage: revocationStore,
		Config:                 engineCfg,
	}
	refreshGrant := &enginehandler.RefreshTokenGrantHandler{
		AccessTokenStrategy:    strategy,
		RefreshTokenStrategy:   strategy,
		TokenRevocationStorage: revocationStore,
		Config:                 engineCfg,
	}
	proofKey := &engineproofkey.Handler{
		AuthorizeCodeStrategy: strategy,
		Storage:               proofKeyStore,
		Config:                engineCfg,
	}
	introspector := &enginehandler.CoreValidator{
		CoreStrategy: strategy,
		CoreStorage:  coreStore,
		Config:       engineCfg,
	}
	revoker := &enginehandler.TokenRevocationHandler{
		TokenRevocationStorage: revocationStore,
		AccessTokenStrategy:    strategy,
		RefreshTokenStrategy:   strategy,
	}

	// ПОРЯДОК ЗНАЧИМ: доказательство владения ключом сверяется ПОСЛЕ того,
	// как код выпущен, — обработчик PKCE читает уже выданный код из
	// ответа. Поставь его первым, и он не нашёл бы кода и отказал бы
	// «обработчик PKCE обязан быть загружен после обработчика кода».
	engineCfg.AuthorizeEndpointHandlers.Append(explicitGrant)
	engineCfg.AuthorizeEndpointHandlers.Append(proofKey)

	engineCfg.TokenEndpointHandlers.Append(explicitGrant)
	engineCfg.TokenEndpointHandlers.Append(refreshGrant)
	engineCfg.TokenEndpointHandlers.Append(proofKey)

	engineCfg.TokenIntrospectionHandlers.Append(introspector)
	engineCfg.RevocationHandlers.Append(revoker)

	return &Ceremony{
		provider:     engine.NewOAuth2Provider(clientStore, engineCfg),
		cfg:          cfg,
		bridge:       bridge,
		authorizeURL: authorizeURL,
		tokenURL:     tokenURL,
	}, nil
}

// Утверждения времени сборки. Они — предикат того, что утверждения типа в New
// не могут не сойтись: расхождение перестаёт быть отказом в рабочем пути и
// становится отказом сборки.
var (
	_ engine.Storage                       = (*storageBridge)(nil)
	_ enginehandler.CoreStorage            = (*storageBridge)(nil)
	_ enginehandler.TokenRevocationStorage = (*storageBridge)(nil)
	_ engineproofkey.PKCERequestStorage    = (*storageBridge)(nil)
	_ engine.Storage                       = (*transactionalStorageBridge)(nil)
	_ enginehandler.CoreStorage            = (*transactionalStorageBridge)(nil)
	_ enginehandler.TokenRevocationStorage = (*transactionalStorageBridge)(nil)
	_ engineproofkey.PKCERequestStorage    = (*transactionalStorageBridge)(nil)
)

func validateConfig(cfg *Config) error {
	const (
		minSigningSecret = 32
		minHashCost      = 10
		maxHashCost      = 15
		minEntropy       = 8
	)
	switch {
	case strings.TrimSpace(cfg.Issuer) == "":
		return misuse("Config.Issuer is not named")
	case len(cfg.SigningSecret) < minSigningSecret:
		return misuse("Config.SigningSecret is shorter than 32 bytes")
	case cfg.AccessTokenLifespan <= 0:
		return misuse("Config.AccessTokenLifespan is not a positive duration")
	case cfg.RefreshTokenLifespan <= 0:
		return misuse("Config.RefreshTokenLifespan is not a positive duration")
	case cfg.AuthorizationCodeLifespan <= 0:
		return misuse("Config.AuthorizationCodeLifespan is not a positive duration")
	case cfg.ScopeMatching == ScopeMatchingUnspecified:
		return misuse("Config.ScopeMatching is not named")
	case cfg.ScopeMatching != ScopeMatchingExact && cfg.ScopeMatching != ScopeMatchingWildcard:
		return misuse("Config.ScopeMatching is not one of the declared rules")
	case cfg.RefreshTokenIssuance == RefreshTokenIssuanceUnspecified:
		return misuse("Config.RefreshTokenIssuance is not named")
	case cfg.RefreshTokenIssuance == RefreshTokenIssuanceOnScope && len(cfg.RefreshTokenScopes) == 0:
		return misuse("Config.RefreshTokenScopes is empty while Config.RefreshTokenIssuance is RefreshTokenIssuanceOnScope")
	case cfg.RefreshTokenIssuance == RefreshTokenIssuanceAlways && len(cfg.RefreshTokenScopes) != 0:
		return misuse("Config.RefreshTokenScopes is set while Config.RefreshTokenIssuance is RefreshTokenIssuanceAlways")
	case cfg.SecretHashCost < minHashCost || cfg.SecretHashCost > maxHashCost:
		return misuse("Config.SecretHashCost is outside the range [10, 15]")
	case cfg.MinParameterEntropy < minEntropy:
		return misuse("Config.MinParameterEntropy is below 8 characters")
	case cfg.PortTimeout <= 0:
		return misuse("Config.PortTimeout is not a positive duration")
	case cfg.OperationTimeout <= 0:
		return misuse("Config.OperationTimeout is not a positive duration")
	case cfg.OperationTimeout < cfg.PortTimeout:
		return misuse("Config.OperationTimeout is shorter than Config.PortTimeout")
	case cfg.NewGrantID == nil:
		return misuse("Config.NewGrantID is not named; the grant identifier is minted by the service, not by the engine")
	}
	return nil
}

func validatePorts(ports Ports) error {
	switch {
	case ports.Clients == nil:
		return misuse("Ports.Clients is not named")
	case ports.AuthorizationCodes == nil:
		return misuse("Ports.AuthorizationCodes is not named")
	case ports.AccessTokens == nil:
		return misuse("Ports.AccessTokens is not named")
	case ports.RefreshTokens == nil:
		return misuse("Ports.RefreshTokens is not named")
	case ports.Grants == nil:
		return misuse("Ports.Grants is not named")
	case ports.ProofKeys == nil:
		return misuse("Ports.ProofKeys is not named")
	}
	return nil
}

func scopeStrategyOf(m ScopeMatching) engine.ScopeStrategy {
	if m == ScopeMatchingWildcard {
		return engine.WildcardScopeStrategy
	}
	return engine.ExactScopeStrategy
}

// refreshTokenScopesOf переводит правило выдачи в язык движка.
//
// У движка это ОДНО поле с двумя молчаливыми смыслами: nil — «умолчание
// апстрима», пустой непустой срез — «выдавать всегда». Различие между nil и
// пустым срезом — ровно тот класс, который здесь не заводят: снаружи правило
// названо перечислением, а сжатие в одно поле живёт тут, на виду.
func refreshTokenScopesOf(cfg Config) []string {
	if cfg.RefreshTokenIssuance == RefreshTokenIssuanceAlways {
		return []string{}
	}
	return copyStrings(cfg.RefreshTokenScopes)
}

// ── Намерение авторизации ───────────────────────────────────────────────────

// AuthorizationIntent — разобранный и проверенный запрос авторизации,
// ожидающий решения службы.
//
// # Почему у него нет ни одного экспортированного поля
//
// Внутри лежит разобранный запрос ДВИЖКА. Сделай поле экспортированным — и
// имя движка уехало бы в подпись, а потребитель оказался бы обязан его знать.
// Наружу смотрят только методы, и все они отвечают нашими типами.
//
// Значение передаётся по значению и годится ровно одной церемонии — той, что
// его выдала. Чужое намерение отвергается (ErrCeremonyMisuse).
type AuthorizationIntent struct {
	requester engine.AuthorizeRequester
	issuedBy  *Ceremony
}

// Issued отвечает, выдано ли намерение церемонией. Ложь означает нулевое
// значение типа.
func (i AuthorizationIntent) Issued() bool { return i.requester != nil && i.issuedBy != nil }

// ClientID — клиент, запросивший авторизацию.
func (i AuthorizationIntent) ClientID() string {
	if !i.Issued() {
		return ""
	}
	if c := i.requester.GetClient(); c != nil {
		return c.GetID()
	}
	return ""
}

// RequestedScopes — запрошенные области.
func (i AuthorizationIntent) RequestedScopes() []string {
	if !i.Issued() {
		return nil
	}
	return copyStrings(i.requester.GetRequestedScopes())
}

// RequestedAudiences — запрошенные получатели.
func (i AuthorizationIntent) RequestedAudiences() []string {
	if !i.Issued() {
		return nil
	}
	return copyStrings(i.requester.GetRequestedAudience())
}

// State — непрозрачное значение клиента.
func (i AuthorizationIntent) State() string {
	if !i.Issued() {
		return ""
	}
	return i.requester.GetState()
}

// RedirectURI — проверенный адрес возврата. Пусто, если адрес не прошёл
// проверку: по такому адресу отвечать запрещено (RFC 6749 §4.1.2.1).
func (i AuthorizationIntent) RedirectURI() string {
	if !i.Issued() || !i.requester.IsRedirectURIValid() {
		return ""
	}
	if u := i.requester.GetRedirectURI(); u != nil {
		return u.String()
	}
	return ""
}

// ResponseKinds — запрошенные типы ответа.
func (i AuthorizationIntent) ResponseKinds() []ResponseKind {
	if !i.Issued() {
		return nil
	}
	raw := i.requester.GetResponseTypes()
	out := make([]ResponseKind, 0, len(raw))
	for _, r := range raw {
		out = append(out, ResponseKind(r))
	}
	return out
}

// Delivery — выбранный способ доставки ответа.
func (i AuthorizationIntent) Delivery() ResponseDelivery {
	if !i.Issued() {
		return DeliveryDefault
	}
	return ResponseDelivery(i.requester.GetResponseMode())
}

// Parameter отдаёт протокольное поле запроса по имени: `nonce`, `prompt`,
// `code_challenge` и прочие, которые служба показывает человеку или кладёт в
// свой журнал согласия.
func (i AuthorizationIntent) Parameter(name string) string {
	if !i.Issued() {
		return ""
	}
	return i.requester.GetRequestForm().Get(name)
}

// ── Точка авторизации ───────────────────────────────────────────────────────

// Authorize разбирает и проверяет запрос авторизации.
//
// # Что он НЕ делает
//
// Не опознаёт человека, не спрашивает согласия, не пишет в сеть. Он отвечает
// намерением, которое служба показывает человеку и затем закрывает либо
// CompleteAuthorization, либо DenyAuthorization.
//
// # Почему намерение возвращается И ПРИ ОТКАЗЕ
//
// Отказ точки авторизации по RFC 6749 §4.1.2.1 уезжает клиенту
// ПЕРЕНАПРАВЛЕНИЕМ на его адрес возврата, а не телом ответа. Чтобы собрать
// перенаправление, нужен разобранный запрос — тот самый, при разборе которого
// случился отказ. Поэтому первый исход годен для DenyAuthorization даже
// тогда, когда второй — непустая ошибка; если же адрес возврата не прошёл
// проверку, RedirectURI пуст, и отвечать клиенту запрещено.
func (c *Ceremony) Authorize(ctx context.Context, req AuthorizationRequest) (AuthorizationIntent, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()
	ctx, notes := withNotes(ctx)

	form, err := authorizeForm(req)
	if err != nil {
		return AuthorizationIntent{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.authorizeURL+"?"+form.Encode(), nil)
	if err != nil {
		return AuthorizationIntent{}, failf(CodeCeremonyMisuse, err,
			"The authorization request could not be encoded.", "", err.Error())
	}

	requester, engineErr := c.provider.NewAuthorizeRequest(ctx, httpReq)
	intent := AuthorizationIntent{requester: requester, issuedBy: c}
	if engineErr != nil {
		return intent, notes.preferRecorded(fromEngine(engineErr))
	}
	return intent, nil
}

// CompleteAuthorization закрывает намерение выдачей.
//
// Собирает ответ РУКАМИ ДВИЖКА — тем же кодом, что собирает его у всех, кто
// движок применяет напрямую. Собрать перенаправление здесь заново значило бы
// завести второе место об одном предмете, и оно разошлось бы с первым на
// первом же расширении (доставка form_post, порядок параметров, кодирование
// фрагмента).
func (c *Ceremony) CompleteAuthorization(ctx context.Context, intent AuthorizationIntent, grant AuthorizationGrant) (AuthorizationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()

	ctx, notes := withNotes(ctx)

	if err := c.checkIntent(intent); err != nil {
		return AuthorizationResult{}, err
	}
	if strings.TrimSpace(grant.Subject) == "" {
		return AuthorizationResult{}, misuse("AuthorizationGrant.Subject is not named; a grant without a subject is not a grant")
	}
	if err := c.checkGrantWithinRequest(intent, grant); err != nil {
		return AuthorizationResult{}, err
	}
	if err := checkGrantBounds(grant.ExpiresAt); err != nil {
		return AuthorizationResult{}, err
	}

	for _, scope := range grant.GrantedScopes {
		intent.requester.GrantScope(scope)
	}
	for _, audience := range grant.GrantedAudiences {
		intent.requester.GrantAudience(audience)
	}

	// Решение службы о сроках — ГРАНИЦА семейства, а не срок первого
	// артефакта: срок каждому артефакту назначает движок в миг выпуска, а
	// сеанс не даёт назначить его позже границы (ceremonySession).
	session := newSession()
	if err := hydrateSession(session, SessionRecord{
		Subject:  grant.Subject,
		Username: grant.Username,
		NotAfter: grant.ExpiresAt,
		Claims:   grant.Claims,
	}); err != nil {
		return AuthorizationResult{}, err
	}

	// Идентификатор гранта — службы (Config.NewGrantID). Он ставится запросу
	// ДО выпуска: впервые движок читает его, сохраняя код и запрос PKCE, и на
	// пустом начеканил бы свой. Чеканка — последним шагом перед выпуском,
	// чтобы выдача, отвергнутая проверками выше, идентификатора не расходовала.
	grantID, err := c.mintGrantID(ctx)
	if err != nil {
		return AuthorizationResult{}, err
	}
	intent.requester.SetID(grantID)

	responder, engineErr := c.provider.NewAuthorizeResponse(ctx, intent.requester, session)
	if engineErr != nil {
		return AuthorizationResult{}, notes.preferRecorded(fromEngine(engineErr))
	}

	sink := newResponseSink()
	c.provider.WriteAuthorizeResponse(ctx, sink, intent.requester, responder)
	return sink.authorizationResult(intent.Delivery()), nil
}

// mintGrantID спрашивает у службы идентификатор нового гранта.
//
// Крючок — вызов службы, как и порт, и срок у него тот же: Config.PortTimeout.
// Его отказ разбирается, как отказ порта (истёкший срок остаётся истёкшим
// сроком, наш случай — нашим случаем, прочее — ошибкой сервера с текстом в
// Debug). Пустой идентификатор — нарушение контракта: движок принял бы его за
// «не назван» и начеканил бы свой.
func (c *Ceremony) mintGrantID(ctx context.Context) (string, error) {
	const op = "Config.NewGrantID"

	ctx, cancel := context.WithTimeout(ctx, c.cfg.PortTimeout)
	defer cancel()

	grantID, err := c.cfg.NewGrantID(ctx)
	if err != nil {
		return "", fromPort(op, err)
	}
	if grantID == "" {
		return "", contractBreach(op, "the hook returned an empty grant identifier; "+
			"the engine would mint one of its own in its place")
	}
	return grantID, nil
}

// DenyAuthorization закрывает намерение отказом.
//
// reason обязан быть НАШЕЙ ошибкой: перевод в отказ движка идёт по значению
// случая, а не по тексту. Чужая ошибка уезжает как «внутренняя ошибка
// сервера» — и это честный исход, а не молчание.
func (c *Ceremony) DenyAuthorization(ctx context.Context, intent AuthorizationIntent, reason error) (AuthorizationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()

	if err := c.checkIntent(intent); err != nil {
		return AuthorizationResult{}, err
	}
	if reason == nil {
		return AuthorizationResult{}, misuse("DenyAuthorization was called without a reason")
	}

	sink := newResponseSink()
	c.provider.WriteAuthorizeError(ctx, sink, intent.requester, toEngine(reason))
	return sink.authorizationResult(intent.Delivery()), nil
}

// checkGrantWithinRequest отвергает выдачу шире запроса.
//
// # Почему здесь, а не в движке
//
// Движок сверяет с записью клиента только ЗАПРОШЕННОЕ (при разборе запроса
// авторизации), а выданное копирует в артефакты без сверки — и при выдаче
// кода, и при обмене. Проверка на обороте токена обновления есть, но лишь
// против записи клиента и лишь с первого оборота: первый токен доступа жил бы
// весь свой срок с правом, которого клиент не просил. Согласие давалось на
// запрошенное; выдача шире — ошибка службы, и отказ ей — ErrCeremonyMisuse,
// до того как хоть что-то выпущено.
//
// Область сверяется по правилу Config.ScopeMatching (запрошенное `tenant.*`
// при правиле с образцом покрывает `tenant.read`); получатель — точным
// совпадением с запрошенным: у получателя правила с образцом нет.
func (c *Ceremony) checkGrantWithinRequest(intent AuthorizationIntent, grant AuthorizationGrant) error {
	requested := intent.RequestedScopes()
	covers := scopeStrategyOf(c.cfg.ScopeMatching)
	for _, scope := range grant.GrantedScopes {
		if !covers(requested, scope) {
			return misuse("AuthorizationGrant.GrantedScopes carries " + strconv.Quote(scope) +
				", which the authorization request did not ask for; a grant may narrow the request, never widen it")
		}
	}
	audiences := intent.RequestedAudiences()
	for _, audience := range grant.GrantedAudiences {
		if !slices.Contains(audiences, audience) {
			return misuse("AuthorizationGrant.GrantedAudiences carries " + strconv.Quote(audience) +
				", which the authorization request did not ask for; a grant may narrow the request, never widen it")
		}
	}
	return nil
}

// checkGrantBounds отвергает границу семейства, равную нулевому времени.
//
// Нулевое время — не граница. Движок читает нулевой срок токена обновления как
// «без срока», и граница-ноль, сжав к себе каждый назначаемый срок, сделала бы
// семейство БЕССРОЧНЫМ — обратное тому, о чём служба просила, назвав границу.
// Вид без границы выражается отсутствием ключа, а не нулём. Отказ называет
// вид; при нескольких нулевых — первый по порядку имени, чтобы текст отказа не
// зависел от порядка обхода карты.
func checkGrantBounds(bounds map[TokenKind]time.Time) error {
	for _, kind := range slices.Sorted(maps.Keys(bounds)) {
		if bounds[kind].IsZero() {
			return misuse("AuthorizationGrant.ExpiresAt[" + strconv.Quote(string(kind)) + "] is the zero time; " +
				"the zero time is no bound (a zero refresh token expiry reads as no expiry at all) — " +
				"leave the kind out to take its lifespan from the settings")
		}
	}
	return nil
}

func (c *Ceremony) checkIntent(intent AuthorizationIntent) error {
	if !intent.Issued() {
		return misuse("the authorization intent is the zero value; it must come from Authorize")
	}
	if intent.issuedBy != c {
		return misuse("the authorization intent was issued by a different ceremony")
	}
	return nil
}

// ── Точка токена ────────────────────────────────────────────────────────────

// Exchange обменивает грант на артефакты (RFC 6749 §4.1.3, §6).
func (c *Ceremony) Exchange(ctx context.Context, req TokenRequest) (TokenResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()

	ctx, notes := withNotes(ctx)

	if !req.Grant.Declared() {
		return TokenResult{}, failf(CodeUnsupportedGrantType, nil,
			"The requested grant type is not one this authorization server serves.",
			"Use one of: "+joinGrantKinds(GrantKinds())+".", "grant="+string(req.Grant))
	}

	form, err := tokenForm(req)
	if err != nil {
		return TokenResult{}, err
	}

	httpReq, err := c.postForm(ctx, form, req.ClientID, req.ClientSecret, req.AuthMethod)
	if err != nil {
		return TokenResult{}, err
	}

	session := newSession()
	var responder engine.AccessResponder
	accessRequest, engineErr := c.provider.NewAccessRequest(ctx, httpReq, session)
	if engineErr == nil {
		responder, engineErr = c.provider.NewAccessResponse(ctx, accessRequest)
	}

	// Повтор кода авторизации (RFC 6749 §4.1.2) и токена обновления (RFC 9700
	// §4.14.2) отзывает семейство гранта — и последовательный, замеченный
	// выборкой, и одновременный, замеченный нулём строк погашения или оборота
	// либо пропавшей записью PKCE. Правило не зависит от того, чем кончил
	// движок: если повтор замечен, выданное этим обменом принадлежит
	// отозванному семейству, и отдать его вызывающему как успех значило бы
	// отдать мёртвые токены. Отказ отзыва — отказ операции.
	if family := notes.replayed(); family.grantID != "" {
		if err := c.revokeReplayedFamily(ctx, family.grantID); err != nil {
			return TokenResult{}, err
		}
		if engineErr == nil {
			return TokenResult{}, family.refusal
		}
	}
	if engineErr != nil {
		return TokenResult{}, notes.preferRecorded(fromEngine(engineErr))
	}
	return tokenResultOf(responder), nil
}

// revokeReplayedFamily отзывает семейство гранта, у которого замечен повтор.
//
// # Почему здесь, а не в мосту и не в движке
//
// На одновременном повторе ноль строк погашения или оборота приходит ВНУТРИ
// единицы работы движка, и движок её откатывает — отзыв, исполненный в ней,
// откатился бы вместе с ней. Здесь единицы работы движка уже нет: каждый вызов порта
// закрепляется сам. На последовательном повторе движок отзывает и сам, но его
// исход после отзыва огрубляется; повторный отзыв здесь законен — ноль строк
// у порта отзыва не отказ — и делает исход ВИДИМЫМ: отказ отзыва возвращается
// отказом операции, а не случаем «повтор», за которым живое семейство.
//
// # Причина отзыва
//
// Причину порту называет ведомость операции, а не параметр: тем же путём её
// получает и отзыв, исполняемый движком, и у одного семейства в одной операции
// не бывает двух причин. В обмене это причина замеченного повтора — кода или
// токена обновления; в отзыве — просьба клиента (см.
// operationNotes.revocationReason).
//
// # Почему контекст отвязан от отмены вызывающего
//
// Повтор уже замечен. Вызывающий, оборвавший соединение, — возможно, тот
// самый второй владелец токена, — не должен иметь возможности оставить
// семейство живым, оборвав запрос в нужный миг. Срок у каждого вызова порта
// свой (Config.PortTimeout), его назначает мост; бессрочного вызова нет.
func (c *Ceremony) revokeReplayedFamily(ctx context.Context, grantID string) error {
	detached := context.WithoutCancel(ctx)
	refreshErr := c.bridge.RevokeRefreshToken(detached, grantID)
	accessErr := c.bridge.RevokeAccessToken(detached, grantID)
	if refreshErr != nil {
		return refreshErr
	}
	return accessErr
}

// ── Интроспекция ────────────────────────────────────────────────────────────

// Introspect отвечает, годен ли предъявленный артефакт (RFC 7662).
//
// # Почему «негоден» — НЕ ошибка
//
// RFC 7662 §2.2 требует отвечать `active: false` и на выдуманный артефакт, и
// на погашенный, и на чужой. Верни мы здесь ошибку — вызывающий был бы
// вынужден отличать «негоден» от «хранилище упало» по тексту, а поверхность
// научилась бы отвечать разными кодами HTTP на годный и негодный артефакт,
// то есть стала бы прибором для перебора.
func (c *Ceremony) Introspect(ctx context.Context, req IntrospectionRequest) (IntrospectionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()

	ctx, notes := withNotes(ctx)

	if strings.TrimSpace(req.Token) == "" {
		return IntrospectionResult{}, misuse("IntrospectionRequest.Token is empty")
	}

	form := url.Values{}
	form.Set("token", req.Token)
	if req.KindHint != TokenKindUnspecified {
		form.Set("token_type_hint", string(req.KindHint))
	}
	if len(req.Scopes) > 0 {
		form.Set("scope", strings.Join(req.Scopes, " "))
	}

	httpReq, err := c.postForm(ctx, form, req.ClientID, req.ClientSecret, req.AuthMethod)
	if err != nil {
		return IntrospectionResult{}, err
	}

	session := newSession()
	responder, engineErr := c.provider.NewIntrospectionRequest(ctx, httpReq, session)
	if engineErr != nil {
		ours := notes.preferRecorded(fromEngine(engineErr))
		// Обёрнутый токен обновления — негоден, и это ответ, а не отказ.
		// Семейства интроспекция не отзывает: она спрашивает о токене, а не
		// пользуется им, и спрашивать о старом токене вправе и сам клиент.
		switch CodeOf(ours) {
		case CodeInactiveToken, CodeRefreshTokenRotated:
			return IntrospectionResult{Active: false}, nil
		}
		return IntrospectionResult{}, ours
	}
	return introspectionResultOf(responder), nil
}

// ── Отзыв ───────────────────────────────────────────────────────────────────

// Revoke снимает артефакт и всё, что выдано по тому же гранту (RFC 7009).
//
// # Почему «нечего снимать» — НЕ ошибка
//
// RFC 7009 §2.2 требует отвечать успехом на отзыв артефакта, которого нет:
// иначе отзыв стал бы прибором для проверки существования артефакта.
func (c *Ceremony) Revoke(ctx context.Context, req RevocationRequest) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()

	ctx, notes := withNotes(ctx)
	// Всё, что отзывается в этой операции, отзывается по просьбе клиента — и
	// движком (живой артефакт), и церемонией (обёрнутый токен обновления ниже).
	notes.requestRevocation(RevocationClientRevoke)

	if strings.TrimSpace(req.Token) == "" {
		return misuse("RevocationRequest.Token is empty")
	}

	form := url.Values{}
	form.Set("token", req.Token)
	if req.KindHint != TokenKindUnspecified {
		form.Set("token_type_hint", string(req.KindHint))
	}

	httpReq, err := c.postForm(ctx, form, req.ClientID, req.ClientSecret, req.AuthMethod)
	if err != nil {
		return err
	}

	engineErr := c.provider.NewRevocationRequest(ctx, httpReq)

	// Отзыв ОБЁРНУТЫМ токеном обновления. Движок, получив «токен неактивен»,
	// ищет артефакт среди токенов доступа, не находит и отвечает успехом, не
	// тронув гранта, — то есть пара, выданная оборотом, пережила бы выход
	// клиента из сеанса. Отзыв токена обновления снимает весь грант (RFC 7009
	// §2.1), поэтому здесь семейство отзывается целиком — но только по
	// предъявлению того клиента, которому грант выдан: проверку «токен выдан
	// спрашивающему» (там же) движок на этом пути не исполняет, и её исполняет
	// церемония. Чужой обёрнутый токен — негодный токен, и отвечают на него
	// успехом без действия (RFC 7009 §2.2).
	if family := notes.replayed(); family.grantID != "" && family.clientID != "" && family.clientID == req.ClientID {
		if err := c.revokeReplayedFamily(ctx, family.grantID); err != nil {
			return err
		}
	}
	if engineErr == nil {
		return nil
	}

	ours := notes.preferRecorded(fromEngine(engineErr))
	switch CodeOf(ours) {
	case CodeUnhandledRequest, CodeGrantNotFound, CodeNotFound, CodeRefreshTokenRotated:
		return nil
	default:
		return ours
	}
}

// ── Сборка запросов для движка ──────────────────────────────────────────────

// postForm собирает запрос точки токена вместе с доказательством клиента.
func (c *Ceremony) postForm(ctx context.Context, form url.Values, clientID, clientSecret string, method ClientAuthMethod) (*http.Request, error) {
	if method == "" {
		if clientSecret == "" {
			method = ClientAuthNone
		} else {
			method = ClientAuthBasic
		}
	}

	switch method {
	case ClientAuthNone:
		if clientSecret != "" {
			return nil, misuse("a client secret was supplied while AuthMethod is ClientAuthNone")
		}
		if clientID != "" {
			form.Set("client_id", clientID)
		}
	case ClientAuthPost:
		form.Set("client_id", clientID)
		form.Set("client_secret", clientSecret)
	case ClientAuthBasic:
		// Доказательство уезжает заголовком ниже.
	default:
		return nil, misuse("TokenRequest.AuthMethod is not one of the declared methods")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, failf(CodeCeremonyMisuse, err, "The token request could not be encoded.", "", err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if method == ClientAuthBasic {
		// RFC 6749 §2.3.1: обе части кодируются по
		// application/x-www-form-urlencoded ДО кодирования в Basic.
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	return req, nil
}

// authorizeReservedNames — поля запроса авторизации, у которых есть
// именованное место в AuthorizationRequest. Ключ отсюда в Additional —
// второе место об одном значении, и оно отвергается.
var authorizeReservedNames = []string{
	"client_id", "redirect_uri", "response_type", "scope", "audience", "state", "response_mode",
}

func authorizeForm(req AuthorizationRequest) (url.Values, error) {
	form := url.Values{}
	if req.ClientID != "" {
		form.Set("client_id", req.ClientID)
	}
	if req.RedirectURI != "" {
		form.Set("redirect_uri", req.RedirectURI)
	}
	if len(req.ResponseKinds) > 0 {
		kinds := make([]string, 0, len(req.ResponseKinds))
		for _, k := range req.ResponseKinds {
			kinds = append(kinds, string(k))
		}
		form.Set("response_type", strings.Join(kinds, " "))
	}
	if len(req.Scopes) > 0 {
		form.Set("scope", strings.Join(req.Scopes, " "))
	}
	for _, audience := range req.Audiences {
		form.Add("audience", audience)
	}
	if req.State != "" {
		form.Set("state", req.State)
	}
	if req.Delivery != DeliveryDefault {
		form.Set("response_mode", string(req.Delivery))
	}
	if err := mergeAdditional(form, req.Additional, authorizeReservedNames); err != nil {
		return nil, err
	}
	return form, nil
}

// tokenReservedNames — то же для запроса точки токена. `client_secret` здесь
// тоже: секрет обязан приезжать полем TokenRequest, а не окольным путём.
var tokenReservedNames = []string{
	"grant_type", "client_id", "client_secret", "code", "redirect_uri",
	"code_verifier", "refresh_token", "scope", "audience",
}

func tokenForm(req TokenRequest) (url.Values, error) {
	form := url.Values{}
	form.Set("grant_type", string(req.Grant))
	if req.Code != "" {
		form.Set("code", req.Code)
	}
	if req.RedirectURI != "" {
		form.Set("redirect_uri", req.RedirectURI)
	}
	if req.CodeVerifier != "" {
		form.Set("code_verifier", req.CodeVerifier)
	}
	if req.RefreshToken != "" {
		form.Set("refresh_token", req.RefreshToken)
	}
	if len(req.Scopes) > 0 {
		form.Set("scope", strings.Join(req.Scopes, " "))
	}
	for _, audience := range req.Audiences {
		form.Add("audience", audience)
	}
	if err := mergeAdditional(form, req.Additional, tokenReservedNames); err != nil {
		return nil, err
	}
	return form, nil
}

// mergeAdditional добавляет прочие поля, отвергая дубли именованных.
//
// Сверка идёт по ПЕРЕЧНЮ ИМЁН, а не по тому, заполнено ли поле: иначе
// `state`, оставленный пустым, можно было бы передать через Additional — и у
// одного значения стало бы два места, расходящихся молча.
func mergeAdditional(form url.Values, additional map[string][]string, reserved []string) error {
	if len(additional) == 0 {
		return nil
	}
	taken := make(map[string]struct{}, len(reserved))
	for _, name := range reserved {
		taken[name] = struct{}{}
	}
	for name, values := range additional {
		if _, clash := taken[name]; clash {
			return misuse("Additional carries the reserved parameter " + strconv.Quote(name) +
				"; it has a named field of its own")
		}
		for _, v := range values {
			form.Add(name, v)
		}
	}
	return nil
}

func joinGrantKinds(kinds []GrantKind) string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

// ── Перевод ответов ─────────────────────────────────────────────────────────

func tokenResultOf(responder engine.AccessResponder) TokenResult {
	result := TokenResult{
		AccessToken: responder.GetAccessToken(),
		TokenType:   responder.GetTokenType(),
		Additional:  map[string]any{},
	}
	for key, value := range responder.ToMap() {
		switch key {
		case "access_token", "token_type":
			// Уже названы полями; второй раз не кладём.
		case "expires_in":
			if seconds, ok := value.(int64); ok {
				result.ExpiresIn = time.Duration(seconds) * time.Second
			}
		case "scope":
			if scope, ok := value.(string); ok && scope != "" {
				result.Scopes = strings.Split(scope, " ")
			}
		case "refresh_token":
			if token, ok := value.(string); ok {
				result.RefreshToken = token
			}
		case "id_token":
			if token, ok := value.(string); ok {
				result.IdentityToken = token
			}
		default:
			result.Additional[key] = value
		}
	}
	return result
}

func introspectionResultOf(responder engine.IntrospectionResponder) IntrospectionResult {
	if !responder.IsActive() {
		return IntrospectionResult{Active: false}
	}

	result := IntrospectionResult{
		Active:          true,
		Kind:            tokenKindOf(responder.GetTokenUse()),
		AccessTokenType: responder.GetAccessTokenType(),
	}

	requester := responder.GetAccessRequester()
	if requester == nil {
		return result
	}

	rec := grantFromRequester(requester)
	result.GrantID = rec.GrantID
	result.ClientID = rec.ClientID
	result.Subject = rec.Session.Subject
	result.Username = rec.Session.Username
	result.Scopes = rec.GrantedScopes
	result.Audiences = rec.GrantedAudiences
	result.IssuedAt = rec.IssuedAt
	result.Claims = rec.Session.Claims
	if at, named := rec.Session.ExpiresAt[result.Kind]; named {
		result.ExpiresAt = at
	}
	return result
}

// ── Приёмник ответа движка ──────────────────────────────────────────────────

// responseSink принимает то, что движок пишет в сеть, и НЕ ПУСКАЕТ ЭТО В
// СЕТЬ.
//
// Церемония не владеет соединением и не должна им владеть: поверхность может
// быть HTTP, может быть gRPC-шлюзом, может быть пробой. Приёмник даёт
// воспользоваться сборкой ответа по RFC, не отдавая движку сокет.
type responseSink struct {
	header http.Header
	status int
	body   []byte
}

func newResponseSink() *responseSink {
	return &responseSink{header: http.Header{}, status: http.StatusOK}
}

func (s *responseSink) Header() http.Header { return s.header }

func (s *responseSink) WriteHeader(status int) { s.status = status }

func (s *responseSink) Write(p []byte) (int, error) {
	s.body = append(s.body, p...)
	return len(p), nil
}

// authorizationResult перекладывает принятое в наши термины.
func (s *responseSink) authorizationResult(requested ResponseDelivery) AuthorizationResult {
	result := AuthorizationResult{
		Status:     s.status,
		Headers:    copyValues(s.header),
		Parameters: map[string][]string{},
		Delivery:   requested,
	}
	if len(s.body) > 0 {
		result.Body = append([]byte(nil), s.body...)
	}

	location := s.header.Get("Location")
	if location == "" {
		// Тела без перенаправления не бывает ни у чего, кроме
		// доставки form_post: у неё ответ — самоотправляющаяся форма.
		if len(result.Body) > 0 {
			result.Delivery = DeliveryFormPost
		}
		return result
	}

	result.RedirectURI = location
	parsed, err := url.Parse(location)
	if err != nil {
		// Адрес собрал движок из уже проверенного адреса возврата;
		// неразбираемым он быть не может. Если всё же стал —
		// параметры остаются пустыми, а сам адрес отдан как есть:
		// терять ответ из-за разбора нельзя.
		return result
	}

	if parsed.RawQuery != "" {
		if values, parseErr := url.ParseQuery(parsed.RawQuery); parseErr == nil {
			for k, v := range values {
				result.Parameters[k] = v
			}
			if result.Delivery == DeliveryDefault {
				result.Delivery = DeliveryQuery
			}
		}
	}
	if parsed.Fragment != "" {
		if values, parseErr := url.ParseQuery(parsed.Fragment); parseErr == nil {
			for k, v := range values {
				result.Parameters[k] = v
			}
			if result.Delivery == DeliveryDefault {
				result.Delivery = DeliveryFragment
			}
		}
	}
	return result
}

// Утверждение времени сборки: приёмник обязан оставаться пригодным для
// движка. Расхождение — отказ сборки, а не отказ в рабочем пути.
var _ http.ResponseWriter = (*responseSink)(nil)
