// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"errors"
	"time"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// storageBridge — хранилище В ТЕРМИНАХ ДВИЖКА, собранное поверх НАШИХ портов.
//
// Тип не экспортируется: движок знает только его, служба — только порты, и
// пересечься им негде.
//
// # Что мост делает сверх перекладывания
//
//  1. Назначает КАЖДОМУ вызову порта срок (Config.PortTimeout). Без срока
//     зависшее хранилище держит запрос до таймаута поверхности, а к тому мигу
//     соседние вызовы того же обмена уже исполнены — и откатить их некому.
//  2. Проверяет ЧИСЛО затронутых строк по контракту порта и превращает
//     «строк не затронуто» в тот случай, который этому соответствует
//     ИМЕННО ЗДЕСЬ: у погашения кода это «код уже израсходован», у снятия
//     токена — законный исход.
//  3. Сопрягает наш точный случай с часовым движка (см. engineError), чтобы
//     управляющие ветви движка продолжали работать, а точность не терялась.
type storageBridge struct {
	ports   Ports
	timeout time.Duration
}

// transactionalStorageBridge — тот же мост, но с единицей работы.
//
// # Почему ОТДЕЛЬНЫЙ ТИП
//
// Движок выясняет, умеет ли хранилище транзакции, утверждением типа. Будь
// методы на одном мосту всегда, движок получал бы «умею» и при пустом порте,
// а связка «погасить код + положить токены» тихо перестала бы быть
// неделимой: методы-пустышки вернули бы nil, и откатывать было бы нечего.
type transactionalStorageBridge struct {
	*storageBridge
}

// newStorageBridge собирает мост, ВЫБИРАЯ тип по наличию единицы работы.
//
// Возвращает оба вида: base — сам мост, которым церемония отзывает семейство
// повторённого токена по завершении операции (вне единицы работы движка), и
// forEngine — то, что видит движок и о чём он спрашивает утверждением типа.
func newStorageBridge(ports Ports, timeout time.Duration) (base *storageBridge, forEngine any) {
	base = &storageBridge{ports: ports, timeout: timeout}
	if ports.Transaction == nil {
		return base, base
	}
	return base, &transactionalStorageBridge{storageBridge: base}
}

// ── Срок вызова ─────────────────────────────────────────────────────────────

// deadline назначает вызову порта срок. Уже назначенный более короткий срок
// сохраняется: context.WithTimeout берёт ближайший.
func (b *storageBridge) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, b.timeout)
}

// ── Разбор исхода порта ─────────────────────────────────────────────────────

// fromPort переводит отказ порта в наш отказ, приписывая имя вызова.
func fromPort(op string, err error) *ProtocolError {
	var ours *ProtocolError
	if errors.As(err, &ours) {
		return failf(ours.Code, err, ours.Description, ours.Hint, op+": "+ours.Debug)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return failf(CodePortDeadline, err, "A storage call did not finish in time.", "", op+": "+err.Error())
	case errors.Is(err, context.Canceled):
		return failf(CodePortCanceled, err, "A storage call was canceled.", "", op+": "+err.Error())
	}
	return failf(CodeServerError, err, "The storage backing the authorization server failed.", "", op+": "+err.Error())
}

// contractBreach — порт нарушил контракт. Отдельный конструктор, чтобы
// нарушение контракта нельзя было перепутать с отказом хранилища.
func contractBreach(op, why string) *ProtocolError {
	return failf(CodePortContract, nil,
		"A storage port of the authorization server broke its contract.",
		"Fix the port implementation; this is not a protocol failure.", op+": "+why)
}

// checkDeclared — общая часть всех разборов: отказ порта и незаполненный
// исход.
func checkDeclared(op string, out StoreOutcome, err error) *ProtocolError {
	if err != nil {
		return fromPort(op, err)
	}
	if !out.Declared() {
		return contractBreach(op, "the outcome was returned as the zero value of StoreOutcome; "+
			"build it with RowsTouched(tag.RowsAffected())")
	}
	if out.Rows() < 0 {
		return contractBreach(op, "the driver did not report the number of affected rows")
	}
	return nil
}

// exactlyOneRow — исход записи, обязанной затронуть ровно одну строку.
func exactlyOneRow(op string, out StoreOutcome, err error) *ProtocolError {
	if bad := checkDeclared(op, out, err); bad != nil {
		return bad
	}
	if out.Rows() != 1 {
		return contractBreach(op, "the single statement touched a number of rows other than one")
	}
	return nil
}

// atMostOneRow — исход снятия: ноль строк законен, больше одной — нет.
func atMostOneRow(op string, out StoreOutcome, err error) *ProtocolError {
	if bad := checkDeclared(op, out, err); bad != nil {
		return bad
	}
	if out.Rows() > 1 {
		return contractBreach(op, "a statement keyed by a unique signature touched more than one row")
	}
	return nil
}

// anyRows — исход отзыва по гранту: строк может быть сколько угодно.
func anyRows(op string, out StoreOutcome, err error) *ProtocolError {
	return checkDeclared(op, out, err)
}

// pairEngine сопрягает наш случай с часовым движка И заносит наш случай в
// ведомость операции: движок вправе вернуть наружу свой, более грубый вердикт,
// не сохранив наш (см. operationNotes).
func pairEngine(ctx context.Context, ours *ProtocolError, engineSentinel error) error {
	return &engineError{ours: note(ctx, ours), engine: engineSentinel}
}

// ── Справочник клиентов ─────────────────────────────────────────────────────

func (b *storageBridge) GetClient(ctx context.Context, id string) (engine.Client, error) {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	reg, err := b.ports.Clients.LookupClient(ctx, id)
	if err != nil {
		ours := fromPort("ClientDirectory.LookupClient", err)
		if ours.Code == CodeGrantNotFound {
			return nil, pairEngine(ctx, ours, engine.ErrNotFound)
		}
		return nil, note(ctx, ours)
	}
	return clientViewOf(reg), nil
}

// ClientAssertionJWTValid и SetClientAssertionJWT — часть контракта
// хранилища движка (engine.Storage), и утверждений клиента церемония НЕ
// обслуживает: представление клиента не объявляет способа доказательства (см.
// clientViewOf), и движок отвергает утверждение раньше, чем спросит о `jti`.
// Дойди он сюда — отказ, а не разрешение: «не обслуживаем» не может молча
// стать «принято».
func (b *storageBridge) ClientAssertionJWTValid(context.Context, string) error {
	return clientAssertionsNotServed()
}

// SetClientAssertionJWT — см. ClientAssertionJWTValid.
func (b *storageBridge) SetClientAssertionJWT(context.Context, string, time.Time) error {
	return clientAssertionsNotServed()
}

func clientAssertionsNotServed() *ProtocolError {
	return failf(CodeInvalidClient, nil,
		"Client assertions are not served by this authorization server.",
		"Authenticate the client with its secret.", "engine storage: client assertion reached the bridge")
}

// ── Коды авторизации ────────────────────────────────────────────────────────

func (b *storageBridge) CreateAuthorizeCodeSession(ctx context.Context, code string, request engine.Requester) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.AuthorizationCodes.StoreAuthorizationCode(ctx, code, grantFromRequester(request))
	if bad := exactlyOneRow("AuthorizationCodeVault.StoreAuthorizationCode", out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// GetAuthorizeCodeSession отдаёт грант по подписи кода.
//
// Погашенный код — ПОВТОР (RFC 6749 §4.1.2): грант его семейства
// записывается в ведомость ДО сборки запроса, и отзыв исполняет церемония по
// завершении операции — как на полосе токена обновления. Движок, увидев
// часовой, отзывает и сам, но его исход после отзыва огрубляется: отказ его
// отзыва был бы неотличим от успеха. Погашенный код без гранта — нарушение
// контракта порта: отзывать нечего.
//
// Годный код записывается в ведомость как ПРЕДЪЯВЛЕННЫЙ: по нему повтор
// узнаётся там, где грант мосту не приходит, — на нуле строк погашения и на
// пропавшей записи PKCE.
func (b *storageBridge) GetAuthorizeCodeSession(ctx context.Context, code string, session engine.Session) (engine.Requester, error) {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	const op = "AuthorizationCodeVault.FetchAuthorizationCode"
	rec, err := b.ports.AuthorizationCodes.FetchAuthorizationCode(ctx, code)
	if err == nil {
		notesFrom(ctx).notePresentedCode(presentedCode{
			signature:  code,
			grantID:    rec.GrantID,
			clientID:   rec.ClientID,
			proofBound: proofBound(rec),
		})
		return requesterFromGrant(ctx, b.ports.Clients.LookupClient, rec, session)
	}
	ours := fromPort(op, err)
	switch ours.Code {
	case CodeAuthorizationCodeConsumed:
		if rec.GrantID == "" {
			return nil, note(ctx, contractBreach(op, "a consumed authorization code was returned without its grant; "+
				"what was issued under a replayed code cannot be revoked without its identifier"))
		}
		ours = codeReplayed(op)
		notesFrom(ctx).markReplayedFamily(rec.GrantID, rec.ClientID, ours, RevocationCodeReplay)
		requester, buildErr := requesterFromGrant(ctx, b.ports.Clients.LookupClient, rec, session)
		if buildErr != nil {
			// Как у токена обновления: движок получит отказ сборки, а
			// церемония ответит повтором и отзовёт семейство по записанному
			// гранту.
			notesFrom(ctx).record(ours)
			return nil, buildErr
		}
		return requester, pairEngine(ctx, ours, engine.ErrInvalidatedAuthorizeCode)
	case CodeGrantNotFound:
		return nil, pairEngine(ctx, ours, engine.ErrNotFound)
	default:
		return nil, note(ctx, ours)
	}
}

// InvalidateAuthorizeCodeSession гасит код.
//
// ЗДЕСЬ И ТОЛЬКО ЗДЕСЬ решается, состоится ли обмен: ноль затронутых строк
// означает, что код погасил кто-то другой, — ОДНОВРЕМЕННЫЙ ПОВТОР: выборку
// кода прошли двое. Обмен обязан не состояться, а семейство гранта — умереть
// вместе с парой, которую получил опередивший: сервер не знает, который из
// двоих законный. Движок на этом исходе откатывает свою единицу работы и не
// отзывает ничего; грант берётся из ведомости (код выбран в этой же операции),
// и отзыв исполняет церемония.
func (b *storageBridge) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	const op = "AuthorizationCodeVault.ConsumeAuthorizationCode"
	out, err := b.ports.AuthorizationCodes.ConsumeAuthorizationCode(ctx, code)
	if bad := checkDeclared(op, out, err); bad != nil {
		return note(ctx, bad)
	}
	switch {
	case out.Rows() == 1:
		return nil
	case out.Rows() == 0:
		ours := codeReplayed(op)
		if presented, known := notesFrom(ctx).presentedCodeOf(code); known {
			notesFrom(ctx).markReplayedFamily(presented.grantID, presented.clientID, ours, RevocationCodeReplay)
		}
		return pairEngine(ctx, ours, engine.ErrInvalidatedAuthorizeCode)
	default:
		return note(ctx, contractBreach(op, "the single statement touched more than one row"))
	}
}

// codeReplayed — наш отказ на повтор кода. Один конструктор на все пути,
// которыми повтор замечается: выборку погашенного кода, ноль строк погашения
// и пропавшую запись PKCE у кода, к PKCE привязанного.
func codeReplayed(op string) *ProtocolError {
	return failf(CodeAuthorizationCodeConsumed, nil,
		"The authorization code was already redeemed.",
		"Every authorization code may be redeemed exactly once; what was issued under it has been revoked.", op)
}

// proofBound — привязан ли код к PKCE: у его записи есть `code_challenge`.
// Поле доезжает до записи кода потому, что церемония называет его движку в
// перечне сохраняемых полей (Config.SanitationWhiteList в New).
func proofBound(rec GrantRecord) bool {
	for _, challenge := range rec.Form["code_challenge"] {
		if challenge != "" {
			return true
		}
	}
	return false
}

// ── Токены доступа ──────────────────────────────────────────────────────────

func (b *storageBridge) CreateAccessTokenSession(ctx context.Context, signature string, request engine.Requester) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.AccessTokens.StoreAccessToken(ctx, signature, grantFromRequester(request))
	if bad := exactlyOneRow("AccessTokenVault.StoreAccessToken", out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// GetAccessTokenSession отдаёт грант по подписи токена доступа — его jti.
//
// Пустая подпись — токен, который порт выпуска НЕ ОПОЗНАЛ
// (artifactStrategy.AccessTokenSignature): пустого jti у выпущенного токена не
// бывает, его отвергает выпуск (checkIssued). Хранилище по пустой подписи не
// спрашивается. Если порт ответил «не наш», ответ — «записи нет». Если
// опознание ОТКАЗАЛО, ответ — этот отказ, и он пишется в ведомость операции
// любым случаем, а не только из перечня coarsenable: движок сжимает всякий
// отказ интроспекции в «токен неактивен», а отказ отзыва — в «временно
// недоступно», и без записи интроспекция назвала бы годный токен негодным.
// Отказ опознания — (а) точнее любого вердикта движка и (б) означает, что
// опознать токен доступа эта операция не смогла; операцию, которая всё же
// нашла артефакт иным путём (токен обновления), запись не трогает —
// ведомость читается только на отказе.
func (b *storageBridge) GetAccessTokenSession(ctx context.Context, signature string, session engine.Session) (engine.Requester, error) {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	if signature == "" {
		if failure := notesFrom(ctx).unidentifiedFailure(); failure != nil {
			notesFrom(ctx).record(failure)
			return nil, failure
		}
		return nil, pairEngine(ctx, fromPort("AccessTokenIssuer.IdentifyAccessToken", ErrGrantNotFound), engine.ErrNotFound)
	}

	rec, err := b.ports.AccessTokens.FetchAccessToken(ctx, signature)
	if err != nil {
		return nil, notFoundAware(ctx, "AccessTokenVault.FetchAccessToken", err)
	}
	return requesterFromGrant(ctx, b.ports.Clients.LookupClient, rec, session)
}

func (b *storageBridge) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.AccessTokens.DropAccessToken(ctx, signature)
	if bad := atMostOneRow("AccessTokenVault.DropAccessToken", out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// ── Токены обновления ───────────────────────────────────────────────────────

func (b *storageBridge) CreateRefreshTokenSession(ctx context.Context, signature, accessSignature string, request engine.Requester) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.RefreshTokens.StoreRefreshToken(ctx, signature, accessSignature, grantFromRequester(request))
	if bad := exactlyOneRow("RefreshTokenVault.StoreRefreshToken", out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// GetRefreshTokenSession отдаёт грант по подписи токена обновления.
//
// Обёрнутый токен — ПОВТОР, и он отдаётся движку ВМЕСТЕ с грантом и часовым
// «токен неактивен»: по этому часовому движок на пути обмена отзывает
// артефакты гранта (`flow_refresh.go`, handleRefreshTokenReuse), а для отзыва
// ему нужен идентификатор гранта. Грант семейства записывается в ведомость
// ДО сборки запроса: если сборка откажет (клиента успели снять), отзыв всё
// равно состоится — его исполнит церемония.
func (b *storageBridge) GetRefreshTokenSession(ctx context.Context, signature string, session engine.Session) (engine.Requester, error) {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	const op = "RefreshTokenVault.FetchRefreshToken"
	rec, err := b.ports.RefreshTokens.FetchRefreshToken(ctx, signature)
	if err == nil {
		return requesterFromGrant(ctx, b.ports.Clients.LookupClient, rec, session)
	}
	ours := fromPort(op, err)
	switch ours.Code {
	case CodeRefreshTokenRotated:
		if rec.GrantID == "" {
			return nil, note(ctx, contractBreach(op, "a rotated refresh token was returned without its grant; "+
				"the family of a replayed token cannot be revoked without its identifier"))
		}
		ours = refreshReplayed(op)
		notesFrom(ctx).markReplayedFamily(rec.GrantID, rec.ClientID, ours, RevocationRefreshReplay)
		requester, buildErr := requesterFromGrant(ctx, b.ports.Clients.LookupClient, rec, session)
		if buildErr != nil {
			// Случай повтора записывается и тогда, когда запрос собрать не
			// удалось: движок получит отказ сборки, а церемония ответит
			// повтором и отзовёт семейство по записанному гранту.
			notesFrom(ctx).record(ours)
			return nil, buildErr
		}
		return requester, pairEngine(ctx, ours, engine.ErrInactiveToken)
	case CodeGrantNotFound:
		return nil, pairEngine(ctx, ours, engine.ErrNotFound)
	default:
		return nil, note(ctx, ours)
	}
}

func (b *storageBridge) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.RefreshTokens.DropRefreshToken(ctx, signature)
	if bad := atMostOneRow("RefreshTokenVault.DropRefreshToken", out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// RotateRefreshToken помечает токен обновления обёрнутым.
//
// Ноль затронутых строк — ОДНОВРЕМЕННЫЙ ПОВТОР: выборку прошли двое, и этот
// оборот опередили. Движок на этом исходе откатывает свою единицу работы и
// семейства не отзывает; отзыв исполняет церемония по завершении операции, вне
// той единицы работы, — поэтому здесь грант только записывается в ведомость.
func (b *storageBridge) RotateRefreshToken(ctx context.Context, grantID, signature string) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	const op = "RefreshTokenVault.RotateRefreshToken"
	out, err := b.ports.RefreshTokens.RotateRefreshToken(ctx, grantID, signature)
	if bad := checkDeclared(op, out, err); bad != nil {
		return note(ctx, bad)
	}
	switch {
	case out.Rows() == 1:
		return nil
	case out.Rows() == 0:
		if grantID == "" {
			return note(ctx, contractBreach(op, "the refresh token was rotated under an empty grant identifier; "+
				"the family of a replayed token cannot be revoked without it"))
		}
		ours := refreshReplayed(op)
		notesFrom(ctx).markReplayedFamily(grantID, "", ours, RevocationRefreshReplay)
		return pairEngine(ctx, ours, engine.ErrInactiveToken)
	default:
		return note(ctx, contractBreach(op, "the single statement touched more than one row"))
	}
}

// refreshReplayed — наш отказ на повтор токена обновления. Один конструктор на
// оба пути, которыми повтор замечается: выборку и оборот.
func refreshReplayed(op string) *ProtocolError {
	return failf(CodeRefreshTokenRotated, nil,
		"The refresh token was already used.",
		"Every refresh token may be presented exactly once; the grant has been revoked.", op)
}

// ── Отзыв по гранту ─────────────────────────────────────────────────────────
//
// Оба отзыва зовут двое: движок (последовательный повтор кода и токена
// обновления, отзыв живого артефакта клиентом) и церемония
// (revokeReplayedFamily). Движок причины не передаёт — его хранилище отзывает
// по одному идентификатору запроса, — поэтому причину порту называет ведомость
// операции (operationNotes.revocationReason).

func (b *storageBridge) RevokeRefreshToken(ctx context.Context, grantID string) error {
	const op = "GrantRevoker.RevokeGrantRefreshTokens"
	reason, bad := revocationReasonFor(ctx, op)
	if bad != nil {
		return bad
	}

	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.Grants.RevokeGrantRefreshTokens(ctx, grantID, reason)
	if bad := anyRows(op, out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

func (b *storageBridge) RevokeAccessToken(ctx context.Context, grantID string) error {
	const op = "GrantRevoker.RevokeGrantAccessTokens"
	reason, bad := revocationReasonFor(ctx, op)
	if bad != nil {
		return bad
	}

	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.Grants.RevokeGrantAccessTokens(ctx, grantID, reason)
	if bad := anyRows(op, out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// revocationReasonFor — причина отзыва в операции, которой принадлежит ctx.
//
// Причины нет — порт не зовётся. Подставить её нечем: значения «не названа» у
// словаря нет, а любое из трёх было бы ложью службе в её журнал. Отзыв в
// операции, которая причины не называет и повтора в которой не замечено, —
// дефект провязки церемонии, а не отказ хранилища и не отказ протокола:
// каждая операция, в которой движок отзывает, причину называет (Revoke) либо
// замечает повтор раньше отзыва (Exchange).
func revocationReasonFor(ctx context.Context, op string) (RevocationReason, *ProtocolError) {
	reason, named := notesFrom(ctx).revocationReason()
	if !named {
		return "", failf(CodeCeremonyMisuse, nil,
			"The authorization server was about to revoke a grant without knowing why.", "",
			op+": the operation names no revocation reason and noticed no replay; the port was not called")
	}
	return reason, nil
}

// ── Доказательство владения ключом ──────────────────────────────────────────

func (b *storageBridge) CreatePKCERequestSession(ctx context.Context, signature string, request engine.Requester) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.ProofKeys.StoreProofKeyRequest(ctx, signature, grantFromRequester(request))
	if bad := exactlyOneRow("ProofKeyVault.StoreProofKeyRequest", out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// GetPKCERequestSession отдаёт запрос PKCE по подписи кода.
//
// # Пропавшая запись у кода, привязанного к PKCE, — ПОВТОР
//
// Запись PKCE снимает движок при предъявлении кода, ДО сверки доказательства
// и до погашения кода. Значит у кода, привязанного к PKCE (запись кода,
// выбранная в этой же операции, несёт `code_challenge`), запись пропадает
// ровно тогда, когда код уже предъявлялся: одновременно — и тот обмен ещё
// идёт, — либо раньше, с неверным доказательством. Это второе предъявление
// кода (RFC 6749 §4.1.2), и отвечается оно как повтор: семейство гранта
// отзывается, а движок получает НЕ «записи нет» — на «записи нет» при пустом
// доказательстве и PKCE, не требуемом настройками, он выдал бы токены по коду,
// к PKCE привязанному.
//
// У кода без привязки к PKCE записи не было никогда, и «записи нет» —
// законный исход.
func (b *storageBridge) GetPKCERequestSession(ctx context.Context, signature string, session engine.Session) (engine.Requester, error) {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	const op = "ProofKeyVault.FetchProofKeyRequest"
	rec, err := b.ports.ProofKeys.FetchProofKeyRequest(ctx, signature)
	if err != nil {
		ours := fromPort(op, err)
		if presented, known := notesFrom(ctx).presentedCodeOf(signature); ours.Code == CodeGrantNotFound && known && presented.proofBound {
			replay := codeReplayed(op)
			notesFrom(ctx).markReplayedFamily(presented.grantID, presented.clientID, replay, RevocationCodeReplay)
			return nil, pairEngine(ctx, replay, engine.ErrInvalidatedAuthorizeCode)
		}
		return nil, notFoundAware(ctx, op, err)
	}
	return requesterFromGrant(ctx, b.ports.Clients.LookupClient, rec, session)
}

func (b *storageBridge) DeletePKCERequestSession(ctx context.Context, signature string) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	out, err := b.ports.ProofKeys.DropProofKeyRequest(ctx, signature)
	if bad := atMostOneRow("ProofKeyVault.DropProofKeyRequest", out, err); bad != nil {
		return note(ctx, bad)
	}
	return nil
}

// ── Единица работы ──────────────────────────────────────────────────────────

// BeginTX открывает единицу работы.
//
// # Почему здесь НЕТ своего срока, хотя он есть у всех соседей
//
// Порт возвращает КОНТЕКСТ, несущий открытую транзакцию, и все последующие
// вызовы приходят с ним. Назначь мы здесь свой срок, возвращённый контекст
// был бы его потомком — и умер бы по выходе отсюда, вместе с `cancel`.
// Транзакция осталась бы открытой с мёртвым контекстом: ни закрепить, ни
// откатить.
//
// Срок у этого вызова всё же есть, и он не «никакой»: церемония назначает
// срок ВСЕЙ операции в начале каждого своего метода (Config.OperationTimeout),
// и открытие транзакции ограничено им. Предикат — проба
// TestBeginInheritsOperationDeadline.
func (b *transactionalStorageBridge) BeginTX(ctx context.Context) (context.Context, error) {
	txCtx, err := b.ports.Transaction.Begin(ctx)
	if err != nil {
		return ctx, note(ctx, fromPort("UnitOfWork.Begin", err))
	}
	return txCtx, nil
}

func (b *transactionalStorageBridge) Commit(ctx context.Context) error {
	inner, cancel := b.deadline(ctx)
	defer cancel()

	if err := b.ports.Transaction.Commit(inner); err != nil {
		return note(ctx, fromPort("UnitOfWork.Commit", err))
	}
	return nil
}

func (b *transactionalStorageBridge) Rollback(ctx context.Context) error {
	inner, cancel := b.deadline(ctx)
	defer cancel()

	if err := b.ports.Transaction.Rollback(inner); err != nil {
		return note(ctx, fromPort("UnitOfWork.Rollback", err))
	}
	return nil
}

// ── Мелочи ──────────────────────────────────────────────────────────────────

// notFoundAware переводит отказ чтения, сопрягая «записи нет» с часовым
// движка: без него движок принял бы отсутствие записи за поломку хранилища и
// ответил бы «внутренняя ошибка» вместо «грант недействителен».
func notFoundAware(ctx context.Context, op string, err error) error {
	ours := fromPort(op, err)
	if ours.Code == CodeGrantNotFound {
		return pairEngine(ctx, ours, engine.ErrNotFound)
	}
	return note(ctx, ours)
}
