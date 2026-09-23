// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
	enginehandler "github.com/PRO-Robotech/corelib/internal/oauth2/handler/oauth2"
)

// artifactStrategy — стратегия выпуска артефактов церемонии: то, чем движок
// выпускает, опознаёт и проверяет код авторизации, токен доступа и токен
// обновления.
//
// # Почему своя, а не стратегия движка
//
// Стратегии движка подписывают все три вида артефакта одним общим секретом
// церемонии. У такого секрета два порока: токен доступа подписан не тем, кто
// публикует ключи его проверки, а срок в ответе обмена считается не из того
// exp, что лежит в токене; и у службы появляется второй подписной материал,
// который она обязана хранить, вращать и беречь, хотя код и токен обновления
// сверяются только поиском в хранилище.
//
// Здесь подписного материала нет вовсе:
//
//   - код авторизации и токен обновления НЕПРОЗРАЧНЫ: значение — 256
//     случайных бит (opaqueArtifactBytes), подпись, под которой лежит грант, —
//     sha256 значения в hex. Строка хранилища значения не выдаёт, а подделать
//     значение под чужую подпись — найти прообраз sha256;
//   - токен доступа выпускает и опознаёт ПОРТ службы (AccessTokenIssuer), и
//     подпись, под которой лежит грант, — его jti.
//
// Значение неизменяемо после сборки и пригодно для одновременных обменов:
// собственного изменяемого состояния у него нет.
type artifactStrategy struct {
	issuer AccessTokenIssuer
	// timeout — срок ОДНОГО вызова порта выпуска, тот же, что у портов
	// хранения (Config.PortTimeout).
	timeout time.Duration
	// lifespans — сроки из настроек: по ним судится артефакт, у записи
	// которого срок не назван, — так же, как это делает движок.
	lifespans enginehandler.LifespanConfigProvider
}

// opaqueArtifactBytes — длина случайной части непрозрачного артефакта: 256
// бит. RFC 6749 §10.10 требует для непредсказуемых значений не менее 128 бит;
// вдвое больше — запас против подбора по всей совокупности живых артефактов.
const opaqueArtifactBytes = 32

var _ enginehandler.CoreStrategy = (*artifactStrategy)(nil)

// ── Непрозрачные артефакты ─────────────────────────────────────────────────

// newOpaqueArtifact выпускает значение непрозрачного артефакта и его подпись.
func newOpaqueArtifact() (value, signature string, err error) {
	var raw [opaqueArtifactBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	value = base64.RawURLEncoding.EncodeToString(raw[:])
	return value, opaqueSignature(value), nil
}

// opaqueSignature — подпись непрозрачного артефакта: sha256 значения в hex.
// Считается от ПРЕДЪЯВЛЕННОЙ строки, как она пришла: иная строка — иная
// подпись, и грант под ней не найдётся.
func opaqueSignature(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *artifactStrategy) AuthorizeCodeSignature(_ context.Context, code string) string {
	return opaqueSignature(code)
}

func (s *artifactStrategy) GenerateAuthorizeCode(context.Context, engine.Requester) (string, string, error) {
	return newOpaqueArtifact()
}

// ValidateAuthorizeCode судит срок кода. Подлинность уже доказана тем, что
// грант нашёлся под подписью предъявленного значения.
func (s *artifactStrategy) ValidateAuthorizeCode(ctx context.Context, r engine.Requester, _ string) error {
	return notExpired(r, engine.AuthorizeCode, s.lifespans.GetAuthorizeCodeLifespan(ctx), "Authorization code")
}

func (s *artifactStrategy) RefreshTokenSignature(_ context.Context, token string) string {
	return opaqueSignature(token)
}

func (s *artifactStrategy) GenerateRefreshToken(context.Context, engine.Requester) (string, string, error) {
	return newOpaqueArtifact()
}

// ValidateRefreshToken судит срок токена обновления. Нулевой срок движок
// читает у токена обновления как «без срока», и здесь то же чтение: без него
// токен, чей срок не записан, отвергался бы здесь и принимался бы движком в
// другом месте. Церемония срок пишет всегда (ceremonySession.SetExpiresAt).
func (s *artifactStrategy) ValidateRefreshToken(_ context.Context, r engine.Requester, _ string) error {
	exp := r.GetSession().GetExpiresAt(engine.RefreshToken)
	if !exp.IsZero() && exp.Before(time.Now().UTC()) {
		return engine.ErrTokenExpired.WithHintf("Refresh token expired at '%s'.", exp)
	}
	return nil
}

// ── Токен доступа ──────────────────────────────────────────────────────────

// GenerateAccessToken выпускает токен доступа портом службы.
//
// Граница срока — уже назначенный движком срок токена доступа в сеансе
// (срок из настроек от мига обмена, сжатый границей семейства). Выпуск позже
// неё — нарушение контракта порта, и такой токен клиенту не уезжает. Срок
// выпуска становится сроком в сеансе, то есть сроком у записи гранта, — у
// токена, записи и ответа обмена один источник срока: этот выпуск.
func (s *artifactStrategy) GenerateAccessToken(ctx context.Context, requester engine.Requester) (string, string, error) {
	const op = "AccessTokenIssuer.IssueAccessToken"
	grant := grantFromRequester(requester)
	bound, named := grant.Session.ExpiresAt[TokenKindAccess]
	if !named {
		return "", "", failf(CodeCeremonyMisuse, nil,
			"The authorization engine asked for an access token before assigning its expiry.", "", op)
	}

	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	issued, err := s.issuer.IssueAccessToken(callCtx, grant)
	if err != nil {
		return "", "", note(ctx, fromPort(op, err))
	}
	if breach := checkIssued(op, issued, bound); breach != nil {
		return "", "", note(ctx, breach)
	}

	requester.GetSession().SetExpiresAt(engine.AccessToken, issued.ExpiresAt)
	notesFrom(ctx).noteIssuedAccessToken(issued)
	return issued.Token, issued.ID, nil
}

// checkIssued сверяет выпуск с контрактом порта: названо всё, срок после
// момента выпуска и не позже границы церемонии.
func checkIssued(op string, issued IssuedAccessToken, bound time.Time) *ProtocolError {
	switch {
	case issued.Token == "":
		return contractBreach(op, "the access token was issued without its value")
	case issued.ID == "":
		return contractBreach(op, "the access token was issued without its identifier (jti); "+
			"its grant could be neither stored nor found")
	case issued.IssuedAt.IsZero():
		return contractBreach(op, "the access token was issued without its issuance instant (iat)")
	case !issued.ExpiresAt.After(issued.IssuedAt):
		return contractBreach(op, "the access token expires at "+issued.ExpiresAt.UTC().Format(time.RFC3339Nano)+
			", not after its issuance at "+issued.IssuedAt.UTC().Format(time.RFC3339Nano))
	case issued.ExpiresAt.After(bound):
		return contractBreach(op, "the access token expires at "+issued.ExpiresAt.UTC().Format(time.RFC3339Nano)+
			", later than the bound "+bound.UTC().Format(time.RFC3339Nano)+" the ceremony named")
	}
	return nil
}

// AccessTokenSignature опознаёт предъявленный токен доступа портом службы и
// отдаёт его jti.
//
// Подпись движку нужна ДО выборки гранта, а отказать этот метод движка не
// умеет. Поэтому неопознанный токен отдаётся пустой подписью, а ПРИЧИНА —
// ведомости операции: «не наш» туда не пишется (это законный ответ: грант не
// найдётся), отказ опознания — пишется, и мост отвечает им на выборку по
// пустой подписи (storageBridge.GetAccessTokenSession). Проглоти стратегия
// отказ, интроспекция назвала бы годный токен негодным, а отзыв ответил бы
// успехом, не сняв живой токен.
func (s *artifactStrategy) AccessTokenSignature(ctx context.Context, token string) string {
	const op = "AccessTokenIssuer.IdentifyAccessToken"
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	id, err := s.issuer.IdentifyAccessToken(callCtx, token)
	var failure *ProtocolError
	switch {
	case err == nil && id != "":
		notesFrom(ctx).noteUnidentified(nil)
		return id
	case err == nil:
		failure = contractBreach(op, "the token was identified with an empty identifier; there is nothing to find its grant by")
	default:
		if ours := fromPort(op, err); ours.Code != CodeGrantNotFound {
			failure = ours
		}
	}
	notesFrom(ctx).noteUnidentified(failure)
	return ""
}

// ValidateAccessToken судит срок токена доступа по записи гранта. Подлинность
// уже доказана опознанием: грант нашёлся под jti, который порт назвал, проверив
// подпись.
func (s *artifactStrategy) ValidateAccessToken(ctx context.Context, r engine.Requester, _ string) error {
	return notExpired(r, engine.AccessToken, s.lifespans.GetAccessTokenLifespan(ctx), "Access token")
}

// notExpired — срок артефакта по записи. Не названный срок судится так же, как
// судит его движок: от мига запроса плюс срок из настроек.
func notExpired(r engine.Requester, kind engine.TokenType, lifespan time.Duration, what string) error {
	now := time.Now().UTC()
	exp := r.GetSession().GetExpiresAt(kind)
	if exp.IsZero() {
		exp = r.GetRequestedAt().Add(lifespan)
	}
	if exp.Before(now) {
		return engine.ErrTokenExpired.WithHintf("%s expired at '%s'.", what, exp)
	}
	return nil
}
