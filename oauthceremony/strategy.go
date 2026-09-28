// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
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
	// deadline назначает срок ОДНОМУ вызову порта выпуска — тот же, что мост
	// назначает вызову порта хранения (storageBridge.deadline): у срока вызова
	// порта один источник, Config.PortTimeout.
	deadline func(context.Context) (context.Context, context.CancelFunc)
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
//
// Отказа у выпуска нет: crypto/rand.Read с Go 1.24 ошибки не возвращает и
// заполняет срез целиком, а при отказе источника случайности обрывает
// программу — артефакт из неполной случайности выпущен быть не может.
func newOpaqueArtifact() (value, signature string) {
	var raw [opaqueArtifactBytes]byte
	_, _ = rand.Read(raw[:])
	value = base64.RawURLEncoding.EncodeToString(raw[:])
	return value, opaqueSignature(value)
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
	value, signature := newOpaqueArtifact()
	return value, signature, nil
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
	value, signature := newOpaqueArtifact()
	return value, signature, nil
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
//
// # Грант порта — очищенный, как запись в хранилище
//
// Запрос движка на этом шаге — СЫРАЯ форма запроса токена: в ней предъявленный
// код и `code_verifier`, токен обновления и, при client_secret_post, секрет
// клиента. Движок очищает её только для хранилища (Sanitize перед записью
// гранта), а выпуск стоит раньше записи. Поэтому грант порту собирается из
// запроса, очищенного тем же перечнем, что и запись гранта: порт получает тот
// грант, что ляжет в хранилище под jti этого выпуска, и ни одного
// предъявленного секрета.
func (s *artifactStrategy) GenerateAccessToken(ctx context.Context, requester engine.Requester) (string, string, error) {
	const op = "AccessTokenIssuer.IssueAccessToken"
	grant := grantFromRequester(requester.Sanitize(nil))
	bound, named := grant.Session.ExpiresAt[TokenKindAccess]
	if !named {
		return "", "", failf(CodeCeremonyMisuse, nil,
			"The authorization engine asked for an access token before assigning its expiry.", "", op)
	}

	callCtx, cancel := s.deadline(ctx)
	defer cancel()
	calledAt := time.Now().UTC()
	issued, err := s.issuer.IssueAccessToken(callCtx, grant)
	if err != nil {
		return "", "", note(ctx, fromPort(op, err))
	}
	if breach := checkIssued(op, issued, calledAt, bound); breach != nil {
		return "", "", note(ctx, breach)
	}

	requester.GetSession().SetExpiresAt(engine.AccessToken, issued.ExpiresAt)
	notesFrom(ctx).noteIssuedAccessToken(issued)
	return issued.Token, issued.ID, nil
}

// checkIssued сверяет выпуск с контрактом порта: названо всё, момент выпуска
// не раньше вызова, срок после момента выпуска и не позже границы церемонии.
//
// # Почему момент выпуска судится от вызова
//
// Срок в ответе обмена — exp минус момент выпуска (withIssuedLifetime). Момент
// выпуска раньше вызова удлиняет его сверх того, что токену осталось жить:
// выпуск с iat за двое суток до вызова уехал бы с expires_in в двое суток,
// хотя exp не позже границы. Граница снизу — начало секунды, в которой
// церемония позвала порт: подписант, у которого iat — целые секунды, называет
// именно её, и ложь срока в ответе не превышает этой секунды.
//
// Предел по сроку настроек (exp минус iat не длиннее AccessTokenLifespan) этой
// лжи не ловит: у токена, которому по границе семейства осталось пять минут,
// iat на десять минут раньше вызова даёт срок в ответе в пятнадцать минут —
// короче настроек, а токену жить пять. И такой предел отверг бы законный
// выпуск: движок округляет границу до ближайшей секунды, то есть бывает и
// вверх, подписант с iat в целых секундах режет момент выпуска вниз, и exp
// минус iat бывает на секунду длиннее срока настроек.
//
// Момент выпуска ПОЗЖЕ вызова срок в ответе только укорачивает; позже границы
// он быть не может — exp позже iat и не позже границы.
func checkIssued(op string, issued IssuedAccessToken, calledAt, bound time.Time) *ProtocolError {
	notBefore := calledAt.Truncate(time.Second)
	switch {
	case issued.Token == "":
		return contractBreach(op, "the access token was issued without its value")
	case issued.ID == "":
		return contractBreach(op, "the access token was issued without its identifier (jti); "+
			"its grant could be neither stored nor found")
	case issued.IssuedAt.IsZero():
		return contractBreach(op, "the access token was issued without its issuance instant (iat)")
	case issued.IssuedAt.Before(notBefore):
		return contractBreach(op, "the access token was issued at "+issued.IssuedAt.UTC().Format(time.RFC3339Nano)+
			", before the second "+notBefore.Format(time.RFC3339)+" in which the ceremony called the issuer; "+
			"its lifetime in the token response would outlast the token")
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
//
// Исходы порта разбираются по контракту (AccessTokenIssuer.IdentifyAccessToken)
// и только так: jti — токен наш; ErrGrantNotFound — не наш; отказ ПОРТА —
// отказ операции (identificationFailure).
func (s *artifactStrategy) AccessTokenSignature(ctx context.Context, token string) string {
	const op = "AccessTokenIssuer.IdentifyAccessToken"
	callCtx, cancel := s.deadline(ctx)
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
		// «Не наш» — законный ответ, отказа нет.
		if ours := fromPort(op, err); ours.Code != CodeGrantNotFound {
			failure = identificationFailure(op, ours, token)
		}
	}
	notesFrom(ctx).noteUnidentified(failure)
	return ""
}

// identificationFailure — отказ опознания, как его видит операция.
//
// # Отказ, а не вердикт
//
// Отказ опознания обязан остаться ОТКАЗОМ: интроспекция читает случаи
// «неактивен» и «обёрнут» как ответ `active: false`, отзыв читает «не найден»
// как «нечего снимать», и вердикт о токене, пришедший от порта вместо jti,
// стал бы таким ответом — а отзыв по истёкшему токену доступа ответил бы
// успехом, оставив живым токен обновления его семейства. Порту дозволены
// только случаи отказа проверяющего — сбой, недоступность, срок и отмена
// вызова, нарушение контракта; всякий иной названный им случай — вердикт о
// токене (истёк, неактивен, чужая подпись) или любой иной случай протокола —
// нарушение контракта: подлинный токен, истёкший в том числе, порт отвечает
// jti, чужой — ErrGrantNotFound, а срок судит церемония.
//
// # Предъявленное значение — не в текст
//
// Порт получает предъявительский токен как есть — и токен доступа, и, на
// интроспекции без подсказки, токен обновления. Ни один текст отказа
// церемонии его не несёт: значение вырезается из описания, подсказки и
// подробностей, даже если порт, вопреки контракту, положил его в свой текст.
// Отказ порта остаётся причиной (Unwrap) таким, каким его вернул порт: это его
// значение, и его текст — предмет контракта порта.
func identificationFailure(op string, ours *ProtocolError, presented string) *ProtocolError {
	switch ours.Code {
	case CodeServerError, CodeTemporarilyUnavailable, CodePortDeadline, CodePortCanceled, CodePortContract:
	default:
		ours = failf(CodePortContract, ours.cause, textPortContract, textPortContractHint,
			op+": the issuer answered case "+ours.Code.String()+", which is none of its outcomes: "+
				"an authentic token, expired included, is answered with its jti, a foreign one with "+
				"ErrGrantNotFound, a failed check with a failure; "+ours.Debug)
	}
	return withoutPresented(ours, presented)
}

// presentedMarker — чем заменяется предъявленное значение в тексте отказа.
const presentedMarker = "[presented token]"

// withoutPresented — тот же отказ, но без предъявленного значения ни в одном
// тексте. Случай и причина не меняются.
func withoutPresented(p *ProtocolError, presented string) *ProtocolError {
	if presented == "" {
		return p
	}
	scrub := func(text string) string { return strings.ReplaceAll(text, presented, presentedMarker) }
	return failf(p.Code, p.cause, scrub(p.Description), scrub(p.Hint), scrub(p.Debug))
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
