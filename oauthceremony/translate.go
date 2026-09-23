// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// Этот файл — ЕДИНСТВЕННОЕ место, где наши типы встречаются с типами движка.
// Всё, что выше по стеку, говорит только нашими; всё, что ниже, — только его.
// Граница держится не уговором, а пробой TestNoEngineTypeInExportedSurface.

// ── Перевод отказов ─────────────────────────────────────────────────────────

// engineErrKey — то, ЧЕМ движок различает свои отказы. Пара (поле `error`,
// состояние HTTP): метод Is движка сравнивает именно её, а значит большего у
// него и нет. Брать текст описания в ключ нельзя — апстрим правит тексты.
type engineErrKey struct {
	field  string
	status int
}

// engineFailurePairs — соответствие часовых движка нашим случаям.
//
// Таблица строится ОТ ЗНАЧЕНИЙ движка, а не от переписанных строк: запись, у
// которой пропал referent, перестанет собираться, а не станет мёртвой.
var engineFailurePairs = []struct {
	engineErr *engine.RFC6749Error
	code      FailureCode
}{
	{engine.ErrInvalidRequest, CodeInvalidRequest},
	{engine.ErrInvalidClient, CodeInvalidClient},
	{engine.ErrInvalidGrant, CodeInvalidGrant},
	{engine.ErrUnauthorizedClient, CodeUnauthorizedClient},
	{engine.ErrUnsupportedGrantType, CodeUnsupportedGrantType},
	{engine.ErrUnsupportedResponseType, CodeUnsupportedResponseType},
	{engine.ErrUnsupportedResponseMode, CodeUnsupportedResponseMode},
	{engine.ErrInvalidScope, CodeInvalidScope},
	{engine.ErrAccessDenied, CodeAccessDenied},
	{engine.ErrServerError, CodeServerError},
	{engine.ErrTemporarilyUnavailable, CodeTemporarilyUnavailable},
	{engine.ErrInvalidState, CodeInvalidState},
	{engine.ErrInsufficientEntropy, CodeInsufficientEntropy},
	{engine.ErrMisconfiguration, CodeMisconfiguration},
	{engine.ErrNotFound, CodeNotFound},
	{engine.ErrRequestUnauthorized, CodeRequestUnauthorized},
	{engine.ErrRequestForbidden, CodeRequestForbidden},
	{engine.ErrTokenExpired, CodeTokenExpired},
	{engine.ErrTokenSignatureMismatch, CodeTokenSignatureMismatch},
	{engine.ErrInvalidTokenFormat, CodeInvalidTokenFormat},
	{engine.ErrTokenClaim, CodeTokenClaim},
	{engine.ErrScopeNotGranted, CodeScopeNotGranted},
	{engine.ErrInactiveToken, CodeInactiveToken},
	{engine.ErrLoginRequired, CodeLoginRequired},
	{engine.ErrConsentRequired, CodeConsentRequired},
	{engine.ErrInteractionRequired, CodeInteractionRequired},
	{engine.ErrRequestNotSupported, CodeRequestNotSupported},
	{engine.ErrRequestURINotSupported, CodeRequestURINotSupported},
	{engine.ErrRegistrationNotSupported, CodeRegistrationNotSupported},
	{engine.ErrInvalidRequestURI, CodeInvalidRequestURI},
	{engine.ErrInvalidRequestObject, CodeInvalidRequestObject},
	{engine.ErrSerializationFailure, CodeStorageConflict},
	{engine.ErrUnknownRequest, CodeUnhandledRequest},
}

var (
	// engineToFailure — из движка к нам.
	engineToFailure = map[engineErrKey]FailureCode{}
	// failureToEngine — от нас к движку. Нужно ровно одному месту:
	// отказу в согласии, где ответ по RFC собирает движок.
	failureToEngine = map[FailureCode]*engine.RFC6749Error{}
)

func init() {
	for _, p := range engineFailurePairs {
		engineToFailure[engineErrKey{p.engineErr.ErrorField, p.engineErr.CodeField}] = p.code
		failureToEngine[p.code] = p.engineErr
	}
	// Наши случаи, у движка представления не имеющие. Сжимаются в его
	// ближайший по смыслу отказ — но ТОЛЬКО на выходе наружу; внутри они
	// остаются различимы.
	for code, engineErr := range map[FailureCode]*engine.RFC6749Error{
		CodeGrantNotFound:             engine.ErrInvalidGrant,
		CodeAuthorizationCodeConsumed: engine.ErrInvalidGrant,
		CodeRefreshTokenRotated:       engine.ErrInvalidGrant,
		CodePortContract:              engine.ErrServerError,
		CodePortDeadline:              engine.ErrTemporarilyUnavailable,
		CodePortCanceled:              engine.ErrTemporarilyUnavailable,
		CodeCeremonyMisuse:            engine.ErrServerError,
		CodeUnknown:                   engine.ErrServerError,
		CodeUnspecified:               engine.ErrServerError,
	} {
		failureToEngine[code] = engineErr
	}
}

// fromEngine переводит отказ движка в наш.
//
// Порядок проб НЕ ПРОИЗВОЛЕН:
//
//  1. Наш отказ, ОТДАННЫЙ ПРЯМО, отдаётся обратно как есть.
//  2. Наш отказ, ВЛОЖЕННЫЙ движком в свой, предпочитается его вердикту
//     ТОЛЬКО если он из перечня coarsenable. Разница существенна: «записи
//     нет» (CodeGrantNotFound) — СИГНАЛ, который движок читает сам и
//     превращает в правильный протокольный исход («токен негоден»), и
//     подменять его нашим значило бы ломать протокол. «Код уже погашен» и
//     «порт нарушил контракт» движок читать не умеет и огрубляет, теряя
//     точность, — их предпочитаем мы.
//  3. Часовой погашенного кода — отдельным значением, потому что он у движка
//     НЕ RFC6749Error, а обычная ошибка.
//  4. Часовые контекста — до разбора RFC6749Error: истёкший срок вызова
//     порта обязан оставаться истёкшим сроком, а не «ошибкой сервера».
//  5. И только потом — таблица.
func fromEngine(err error) error {
	if err == nil {
		return nil
	}

	if direct, mine := err.(*ProtocolError); mine {
		return direct
	}

	var nested *ProtocolError
	if errors.As(err, &nested) && coarsenable(nested.Code) {
		return nested
	}

	if errors.Is(err, engine.ErrInvalidatedAuthorizeCode) {
		return failf(CodeAuthorizationCodeConsumed, err,
			"The authorization code was already redeemed.",
			"Every authorization code may be redeemed exactly once.", err.Error())
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return failf(CodePortDeadline, err, textPortDeadline, "", err.Error())
	case errors.Is(err, context.Canceled):
		return failf(CodePortCanceled, err, textPortCanceled, "", err.Error())
	}

	var rfcErr *engine.RFC6749Error
	if errors.As(err, &rfcErr) {
		code, known := engineToFailure[engineErrKey{rfcErr.ErrorField, rfcErr.CodeField}]
		debug := rfcErr.DebugField
		if !known {
			// В Debug кладётся ИМЕННО ТО, чем движок различает свои
			// отказы. Без этого CodeUnknown сообщал бы «таблица
			// неполна», не называя недостающей записи, — и дефект
			// таблицы был бы виден, но неисправим.
			code = CodeUnknown
			debug = "unmapped engine failure: error=" + strconv.Quote(rfcErr.ErrorField) +
				" status=" + strconv.Itoa(rfcErr.CodeField) + "; engine debug=" + strconv.Quote(rfcErr.DebugField)
		}
		return failf(code, err, rfcErr.DescriptionField, rfcErr.HintField, debug)
	}

	return failf(CodeUnknown, err, "The authorization engine returned an unrecognized failure.", "", err.Error())
}

// toEngine переводит наш отказ обратно в отказ движка.
//
// Нужно ровно на пути отказа в согласии: перенаправление с `error=` и
// `state=` собирает движок по RFC 6749 §4.1.2.1, и собирать его второй раз
// здесь значило бы завести второе место об одном предмете.
func toEngine(err error) *engine.RFC6749Error {
	code := CodeOf(err)
	engineErr, known := failureToEngine[code]
	if !known {
		engineErr = engine.ErrServerError
	}

	var ours *ProtocolError
	if errors.As(err, &ours) {
		out := engineErr
		if ours.Description != "" {
			out = out.WithDescription(ours.Description)
		}
		if ours.Hint != "" {
			out = out.WithHint(ours.Hint)
		}
		return out
	}
	return engineErr
}

// engineError сопрягает наш отказ с часовым движка.
//
// Движок различает свои случаи по errors.Is; мы свои — тоже. Значение ниже
// отвечает «да» ОБОИМ, поэтому мост может вернуть движку понятный ему часовой,
// не потеряв наш точный случай: он доживает до fromEngine и уезжает наружу
// вместо «внутренней ошибки».
type engineError struct {
	ours   *ProtocolError
	engine error
}

func (e *engineError) Error() string { return e.engine.Error() }

// Unwrap с множественным исходом (Go 1.20+): errors.Is и errors.As обходят
// ОБЕ ветви.
func (e *engineError) Unwrap() []error { return []error{e.ours, e.engine} }

// ── Перевод клиента ─────────────────────────────────────────────────────────

// clientView — запись клиента в том виде, в каком её спрашивает движок.
//
// Тип НЕ ЭКСПОРТИРУЕТСЯ: он существует ровно на время одного запроса и
// наружу не показывается ни одним элементом пакета.
type clientView struct {
	reg ClientRegistration
}

func (c *clientView) GetID() string              { return c.reg.ClientID }
func (c *clientView) GetHashedSecret() []byte    { return c.reg.HashedSecret }
func (c *clientView) GetRotatedHashes() [][]byte { return c.reg.RotatedHashedSecrets }
func (c *clientView) GetRedirectURIs() []string  { return c.reg.RedirectURIs }
func (c *clientView) IsPublic() bool             { return c.reg.Public }
func (c *clientView) GetAudience() engine.Arguments {
	return engine.Arguments(c.reg.Audiences)
}
func (c *clientView) GetScopes() engine.Arguments { return engine.Arguments(c.reg.Scopes) }

func (c *clientView) GetGrantTypes() engine.Arguments {
	out := make(engine.Arguments, 0, len(c.reg.GrantKinds))
	for _, k := range c.reg.GrantKinds {
		out = append(out, string(k))
	}
	return out
}

func (c *clientView) GetResponseTypes() engine.Arguments {
	return engine.Arguments(c.reg.ResponseKinds)
}

func (c *clientView) GetResponseModes() []engine.ResponseModeType {
	out := make([]engine.ResponseModeType, 0, len(c.reg.ResponseDeliveries))
	for _, d := range c.reg.ResponseDeliveries {
		out = append(out, engine.ResponseModeType(d))
	}
	return out
}

// Представления клиента С ЯВНЫМ СПОСОБОМ ДОКАЗАТЕЛЬСТВА (интерфейс движка
// `OpenIDConnectClient`) здесь НЕТ, и это решение, а не пропуск. Тот интерфейс
// отдаёт набор ключей клиента типом библиотеки JOSE движка, и реализовать его
// значило бы импортировать эту библиотеку вне поддерева — а она заключена в
// поддерево (приёмка F1, `F1-51`; гейт `internal/engineconfinement`). Вместе с
// ним сняты способы, которым он нужен: утверждение клиента (RFC 7523 §2.2),
// объекты запроса (OIDC Core §6) и закрепление способа за записью клиента.
// Движок, не найдя у клиента этого интерфейса, отвергает утверждение клиента
// раньше, чем спросит хранилище о `jti` (предикат — проба
// TestClientAssertionIsRefused), а объект запроса — случаем «не
// поддерживается» (предикат — проба TestRequestObjectIsRefused).

// clientViewOf собирает представление клиента для движка.
func clientViewOf(reg ClientRegistration) engine.Client {
	return &clientView{reg: reg}
}

// ── Перевод гранта ──────────────────────────────────────────────────────────

// grantFromRequester снимает с запроса движка нашу запись гранта.
func grantFromRequester(r engine.Requester) GrantRecord {
	rec := GrantRecord{
		GrantID:            r.GetID(),
		IssuedAt:           r.GetRequestedAt(),
		RequestedScopes:    copyStrings(r.GetRequestedScopes()),
		GrantedScopes:      copyStrings(r.GetGrantedScopes()),
		RequestedAudiences: copyStrings(r.GetRequestedAudience()),
		GrantedAudiences:   copyStrings(r.GetGrantedAudience()),
		Form:               copyValues(r.GetRequestForm()),
	}
	if client := r.GetClient(); client != nil {
		rec.ClientID = client.GetID()
	}
	rec.Session = sessionRecordOf(r.GetSession())
	return rec
}

// requesterFromGrant собирает запрос движка из нашей записи.
//
// Клиент берётся из справочника ЗАНОВО, а не из записи: между выдачей кода и
// его обменом клиента могли снять, сделать публичным или сузить ему права, и
// решение обязано приниматься по нынешней записи, а не по слепку.
func requesterFromGrant(ctx context.Context, clients func(context.Context, string) (ClientRegistration, error), rec GrantRecord, session engine.Session) (engine.Requester, error) {
	reg, err := clients(ctx, rec.ClientID)
	if err != nil {
		return nil, err
	}
	client := clientViewOf(reg)

	// Движок передаёт сеанс НЕ ВСЕГДА: на пути отзыва (RFC 7009) он
	// спрашивает грант, чтобы узнать его идентификатор, и сеанс ему не
	// нужен — туда приезжает nil. Отвергнуть nil значило бы превратить
	// законный отзыв в «внутреннюю ошибку»; собираем свой.
	if session == nil {
		session = newSession()
	}
	if err := hydrateSession(session, rec.Session); err != nil {
		return nil, err
	}

	return &engine.Request{
		ID:                rec.GrantID,
		RequestedAt:       rec.IssuedAt,
		Client:            client,
		RequestedScope:    engine.Arguments(copyStrings(rec.RequestedScopes)),
		GrantedScope:      engine.Arguments(copyStrings(rec.GrantedScopes)),
		RequestedAudience: engine.Arguments(copyStrings(rec.RequestedAudiences)),
		GrantedAudience:   engine.Arguments(copyStrings(rec.GrantedAudiences)),
		Form:              url.Values(copyValues(rec.Form)),
		Session:           session,
	}, nil
}

// ── Перевод сеанса ──────────────────────────────────────────────────────────

// Вид артефакта в языке движка и в нашем — РАЗНЫЕ слова у кода авторизации:
// наш `authorization_code` (RFC 6749), движка — `authorize_code`. Перевод
// приведением строки клал бы наш срок кода под ключ, которого движок не
// читает, а срок движка — под ключ, которого нет среди наших видов: граница
// кода не действовала бы, и никто бы этого не увидел. Поэтому — словарь.
var engineTokenTypes = map[TokenKind]engine.TokenType{
	TokenKindAccess:            engine.AccessToken,
	TokenKindRefresh:           engine.RefreshToken,
	TokenKindAuthorizationCode: engine.AuthorizeCode,
	TokenKindIdentity:          engine.IDToken,
}

// engineTypeOf переводит наш вид в вид движка. Вид вне словаря — вид,
// которого у движка нет, — переносится как есть: терять запись молча нельзя.
func engineTypeOf(kind TokenKind) engine.TokenType {
	if t, known := engineTokenTypes[kind]; known {
		return t
	}
	return engine.TokenType(kind)
}

// tokenKindOf — обратный перевод, по тому же словарю.
func tokenKindOf(t engine.TokenType) TokenKind {
	for kind, engineType := range engineTokenTypes {
		if engineType == t {
			return kind
		}
	}
	return TokenKind(t)
}

// ceremonySession — сеанс движка с ГРАНИЦЕЙ СРОКОВ СЕМЕЙСТВА.
//
// # Зачем свой тип
//
// Движок назначает срок артефакту сам, в миг выпуска: `сейчас + срок из
// настроек` — и при выдаче кода, и при обмене, и на КАЖДОМ обороте токена
// обновления, поверх сеанса семейства (`flow_refresh.go`). Граница, названная
// службой (AuthorizationGrant.ExpiresAt), в этой арифметике не участвует, и
// первый же оборот продлевал бы семейство за неё, а каждый следующий — ещё.
// Здесь граница живёт в самом сеансе, и назначить срок позже неё нельзя ни
// одним путём движка: все они назначают срок через SetExpiresAt.
type ceremonySession struct {
	engine.DefaultSession

	// notAfter — граница годности по видам артефактов для ВСЕГО семейства
	// гранта. Отсутствие ключа — границы нет. Нулевого времени здесь не
	// бывает: его отвергают при выдаче (checkGrantBounds) и при наполнении из
	// хранилища (hydrateSession).
	notAfter map[engine.TokenType]time.Time
}

// SetExpiresAt назначает срок, не позже границы семейства, и НИКОГДА не пишет
// нулевого времени.
//
// Нулевой срок движок читает у токена обновления как «без срока», то есть как
// срок позже любой границы. Поэтому при названной границе нулевой срок
// сжимается к ней, а без границы не пишется вовсе — остаётся прежний срок либо
// его отсутствие, то есть то, что движок и так читает. Граница, равная
// нулевому времени, — не граница: оказавшись в сеансе в обход обеих проверок,
// она не сжимает срок к нулю.
func (s *ceremonySession) SetExpiresAt(key engine.TokenType, exp time.Time) {
	if bound, named := s.notAfter[key]; named && !bound.IsZero() && (exp.IsZero() || exp.After(bound)) {
		exp = bound
	}
	if exp.IsZero() {
		return
	}
	s.DefaultSession.SetExpiresAt(key, exp)
}

// Clone — копия вместе с границей. Унаследованная Clone копировала бы только
// сеанс движка, и оборот, работающий на копии (`flow_refresh.go`), шёл бы
// уже без границы.
func (s *ceremonySession) Clone() engine.Session {
	if s == nil {
		return nil
	}
	inner, ours := s.DefaultSession.Clone().(*engine.DefaultSession)
	if !ours || inner == nil {
		inner = &engine.DefaultSession{}
	}
	out := &ceremonySession{DefaultSession: *inner, notAfter: make(map[engine.TokenType]time.Time, len(s.notAfter))}
	for k, v := range s.notAfter {
		out.notAfter[k] = v
	}
	return out
}

// sessionRecordOf снимает с сеанса движка нашу запись.
func sessionRecordOf(s engine.Session) SessionRecord {
	rec := SessionRecord{
		ExpiresAt: map[TokenKind]time.Time{},
		NotAfter:  map[TokenKind]time.Time{},
		Claims:    map[string]any{},
	}
	if s == nil {
		return rec
	}
	rec.Subject = s.GetSubject()
	rec.Username = s.GetUsername()

	cs, ours := s.(*ceremonySession)
	if !ours {
		return rec
	}
	for kind, at := range cs.ExpiresAt {
		rec.ExpiresAt[tokenKindOf(kind)] = at
	}
	for kind, at := range cs.notAfter {
		rec.NotAfter[tokenKindOf(kind)] = at
	}
	for k, v := range cs.Extra {
		rec.Claims[k] = v
	}
	return rec
}

// hydrateSession наполняет сеанс движка нашей записью.
//
// Сеанс ВСЕГДА собирает церемония (newSession), поэтому чужого типа здесь
// быть не может. Если он всё же пришёл — это дефект сборки, и он называется
// отказом, а не молча пропускается: сеанс без сроков годности сделал бы
// бессрочными все выпущенные артефакты.
//
// # Сроки СЛИВАЮТСЯ, а не заменяются
//
// Движок наполняет ОДИН И ТОТ ЖЕ сеанс несколько раз за операцию: при обмене
// кода — выборкой кода, затем выборкой PKCE, затем снова выборкой кода перед
// выпуском, и между первой и последней назначает сроки выпускаемой пары.
// Замена карты сроков записью кода стирала бы их: пара уезжала в хранилище без
// своих сроков, и токен обновления без срока движок считает бессрочным.
// Слияние — ключ записи перекрывает ключ сеанса, прочие остаются — то, чего
// движок ждёт от хранилища (так ведёт себя разбор JSON в карту, на который он
// рассчитан). Граница семейства и прочее — из записи целиком.
//
// # Нулевое время в записи — нарушение контракта порта
//
// Ни граница семейства, ни срок артефакта не бывают нулевым временем в записи,
// которую церемония отдаёт на хранение: границу-ноль отвергает выдача
// (checkGrantBounds), нулевого срока не пишет сеанс (SetExpiresAt). Значит,
// ноль в записи из хранилища — порча записи на стороне службы, и она
// отвергается как ErrPortContract, а не принимается: нулевая граница сжала бы
// к нулю срок нового токена обновления, нулевой срок сделал бы бессрочным
// предъявленный — движок читает нулевой срок токена обновления как «без
// срока». Трактовать ноль как «ключа нет» значило бы молча чинить чужую запись
// в сторону меньшей строгости.
func hydrateSession(s engine.Session, rec SessionRecord) error {
	cs, ours := s.(*ceremonySession)
	if !ours {
		return failf(CodeCeremonyMisuse, nil,
			"The authorization engine asked to hydrate a session this package did not create.", "", "")
	}
	if err := checkStoredInstants("SessionRecord.NotAfter", rec.NotAfter); err != nil {
		return err
	}
	if err := checkStoredInstants("SessionRecord.ExpiresAt", rec.ExpiresAt); err != nil {
		return err
	}
	cs.Subject = rec.Subject
	cs.Username = rec.Username
	cs.notAfter = make(map[engine.TokenType]time.Time, len(rec.NotAfter))
	for kind, at := range rec.NotAfter {
		cs.notAfter[engineTypeOf(kind)] = at
	}
	if cs.ExpiresAt == nil {
		cs.ExpiresAt = make(map[engine.TokenType]time.Time, len(rec.ExpiresAt))
	}
	for kind, at := range rec.ExpiresAt {
		cs.SetExpiresAt(engineTypeOf(kind), at)
	}
	cs.Extra = make(map[string]any, len(rec.Claims))
	for k, v := range rec.Claims {
		cs.Extra[k] = v
	}
	return nil
}

// checkStoredInstants отвергает нулевое время в карте сроков записи. Вид —
// первый по порядку имени, чтобы текст отказа не зависел от порядка обхода.
func checkStoredInstants(field string, instants map[TokenKind]time.Time) error {
	for _, kind := range slices.Sorted(maps.Keys(instants)) {
		if instants[kind].IsZero() {
			return contractBreach(field, strconv.Quote(string(kind))+" is the zero time; "+
				"a stored bound or expiry is a real instant or an absent key, so a zero one is a corrupted record "+
				"(for a refresh token the engine reads a zero expiry as no expiry at all)")
		}
	}
	return nil
}

// newSession собирает пустой сеанс движка. ЕДИНСТВЕННОЕ место, где сеанс
// возникает, — отсюда и уверенность hydrateSession в его типе.
func newSession() engine.Session {
	return &ceremonySession{
		DefaultSession: engine.DefaultSession{
			ExpiresAt: map[engine.TokenType]time.Time{},
			Extra:     map[string]any{},
		},
		notAfter: map[engine.TokenType]time.Time{},
	}
}

// ── Мелочи ──────────────────────────────────────────────────────────────────

// copyStrings отдаёт СВОЙ срез. Без копии наша запись и запрос движка делили
// бы один массив, и правка в движке меняла бы уже отданное наружу значение.
func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// copyValues отдаёт свою карту с копиями значений — по тем же основаниям.
func copyValues(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = copyStrings(v)
	}
	return out
}
