// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	jose "github.com/go-jose/go-jose/v3"

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
	{engine.ErrJTIKnown, CodeAssertionReplayed},
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
		return failf(CodePortDeadline, err, "A storage call did not finish in time.", "", err.Error())
	case errors.Is(err, context.Canceled):
		return failf(CodePortCanceled, err, "A storage call was canceled.", "", err.Error())
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

// oidcClientView — клиент, у которого НАЗВАН способ доказательства.
//
// # Почему отдельный тип, а не поле
//
// Движок спрашивает про способ доказательства утверждением типа. Реализуй его
// единственный clientView — и движок увидел бы способ у КАЖДОГО клиента; у
// того, кто способа не называл, способом оказалась бы пустая строка, и
// доказательство секретом в заголовке Authorization было бы отвергнуто с
// сообщением про «client_secret_basic вместо ”». То есть тип-утверждение
// молча сменил бы поведение всем.
//
// Два типа говорят движку правду: способ назван — спрашивай; не назван —
// вопрос неприменим.
type oidcClientView struct {
	*clientView
	keys *jose.JSONWebKeySet
}

func (c *oidcClientView) GetRequestURIs() []string            { return c.reg.RequestURIs }
func (c *oidcClientView) GetJSONWebKeys() *jose.JSONWebKeySet { return c.keys }
func (c *oidcClientView) GetJSONWebKeysURI() string           { return c.reg.JSONWebKeySetURI }
func (c *oidcClientView) GetTokenEndpointAuthMethod() string  { return string(c.reg.TokenAuthMethod) }
func (c *oidcClientView) GetRequestObjectSigningAlgorithm() string {
	return c.reg.RequestObjectSigningAlg
}

func (c *oidcClientView) GetTokenEndpointAuthSigningAlgorithm() string {
	if c.reg.TokenAuthSigningAlg == "" {
		return defaultAssertionSigningAlg
	}
	return c.reg.TokenAuthSigningAlg
}

// defaultAssertionSigningAlg — умолчание OIDC Core §9 для private_key_jwt.
const defaultAssertionSigningAlg = "RS256"

// clientViewOf собирает представление клиента для движка.
func clientViewOf(reg ClientRegistration) (engine.Client, error) {
	base := &clientView{reg: reg}

	namesAuthMethod := reg.TokenAuthMethod != ""
	carriesKeyMaterial := len(reg.JSONWebKeySet) > 0 || reg.JSONWebKeySetURI != "" || len(reg.RequestURIs) > 0
	if !namesAuthMethod && !carriesKeyMaterial {
		return base, nil
	}

	view := &oidcClientView{clientView: base}
	if len(reg.JSONWebKeySet) > 0 {
		set := new(jose.JSONWebKeySet)
		if err := json.Unmarshal(reg.JSONWebKeySet, set); err != nil {
			return nil, failf(CodeMisconfiguration, err,
				"The registered JSON Web Key Set of the client could not be decoded.",
				"Store the key set as the JSON document defined by RFC 7517 §5.", err.Error())
		}
		view.keys = set
	}
	return view, nil
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
	client, err := clientViewOf(reg)
	if err != nil {
		return nil, err
	}

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

// sessionRecordOf снимает с сеанса движка нашу запись.
func sessionRecordOf(s engine.Session) SessionRecord {
	rec := SessionRecord{ExpiresAt: map[TokenKind]time.Time{}, Claims: map[string]any{}}
	if s == nil {
		return rec
	}
	rec.Subject = s.GetSubject()
	rec.Username = s.GetUsername()

	def, ours := s.(*engine.DefaultSession)
	if !ours {
		return rec
	}
	for kind, at := range def.ExpiresAt {
		rec.ExpiresAt[TokenKind(kind)] = at
	}
	for k, v := range def.Extra {
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
func hydrateSession(s engine.Session, rec SessionRecord) error {
	def, ours := s.(*engine.DefaultSession)
	if !ours {
		return failf(CodeCeremonyMisuse, nil,
			"The authorization engine asked to hydrate a session this package did not create.", "", "")
	}
	def.Subject = rec.Subject
	def.Username = rec.Username
	def.ExpiresAt = make(map[engine.TokenType]time.Time, len(rec.ExpiresAt))
	for kind, at := range rec.ExpiresAt {
		def.ExpiresAt[engine.TokenType(kind)] = at
	}
	def.Extra = make(map[string]any, len(rec.Claims))
	for k, v := range rec.Claims {
		def.Extra[k] = v
	}
	return nil
}

// newSession собирает пустой сеанс движка. ЕДИНСТВЕННОЕ место, где сеанс
// возникает, — отсюда и уверенность hydrateSession в его типе.
func newSession() engine.Session {
	return &engine.DefaultSession{
		ExpiresAt: map[engine.TokenType]time.Time{},
		Extra:     map[string]any{},
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
