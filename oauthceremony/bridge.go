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

// Тексты отказа портов. Портов два вида — хранения и выпуска токена доступа,
// — и тексты называют ПОРТ, а не хранилище: отказ порта выпуска, названный
// отказом хранилища, послал бы оператора чинить не то. Какой именно порт
// отказал, называют подробности (Debug) — именем вызова.
const (
	textPortFailed       = "A port of the authorization server failed."
	textPortDeadline     = "A port call did not finish in time."
	textPortCanceled     = "A port call was canceled."
	textPortContract     = "A port of the authorization server broke its contract."
	textPortContractHint = "Fix the port implementation; this is not a protocol failure."
)

// fromPort переводит отказ порта в наш отказ, приписывая имя вызова.
func fromPort(op string, err error) *ProtocolError {
	var ours *ProtocolError
	if errors.As(err, &ours) {
		return failf(ours.Code, err, ours.Description, ours.Hint, op+": "+ours.Debug)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return failf(CodePortDeadline, err, textPortDeadline, "", op+": "+err.Error())
	case errors.Is(err, context.Canceled):
		return failf(CodePortCanceled, err, textPortCanceled, "", op+": "+err.Error())
	}
	return failf(CodeServerError, err, textPortFailed, "", op+": "+err.Error())
}

// contractBreach — порт нарушил контракт. Отдельный конструктор, чтобы
// нарушение контракта нельзя было перепутать с отказом порта.
func contractBreach(op, why string) *ProtocolError {
	return failf(CodePortContract, nil, textPortContract, textPortContractHint, op+": "+why)
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

// GetClient отдаёт движку запись клиента.
//
// # Клиент, которого справочник не знает, в доказательстве клиента
//
// В операции, где клиент доказывает себя (обмен, интроспекция, отзыв —
// operationNotes.provesClient), движок спрашивает справочник ради
// доказательства и на «клиента нет» отказывает СРАЗУ, не сверяя секрета.
// Отказ неизвестному клиенту стоил бы тогда меньше отказа неверному секрету, а
// на интроспекции ещё и назывался бы другими словами: время и текст ответа
// стали бы прибором для перебора зарегистрированных клиентов.
//
// Поэтому неизвестному клиенту здесь отдаётся представление без единого права
// (unregisteredClient), и движок идёт тем же путём, что с известным: к сверке
// секрета портом службы (clientSecretHasher) — ровно один раз, — и отказ
// приходит оттуда же и теми же словами. Доказать себя такое представление не
// может: «совпал» о нём — нарушение контракта порта (verifyClientSecret).
//
// Вне доказательства клиента (точка авторизации) «клиента нет» — отказ, как
// прежде: секрета там не предъявляют.
//
// Клиент, о котором спросили, записывается в ведомость операции: по ней хешер
// церемонии узнаёт, чей секрет сверять.
//
// # Отказ справочника — отказ операции
//
// Справочник, который не ответил, — не «клиента нет» и не «клиент не доказан»:
// движок сжимает всякий отказ этого вызова в отказ доказательства (на
// интроспекции — не оборачивая), и сбой хранилища выглядел бы потоком
// неверных секретов. Поэтому отказ пишется в ведомость операции любым случаем,
// и операция отвечает им, как отказом порта сверки (verifyClientSecret).
func (b *storageBridge) GetClient(ctx context.Context, id string) (engine.Client, error) {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	notes := notesFrom(ctx)
	reg, err := b.ports.Clients.LookupClient(ctx, id)
	if err != nil {
		ours := fromPort("ClientDirectory.LookupClient", err)
		switch {
		case ours.Code == CodeGrantNotFound && notes.provesClient():
			notes.noteClientClaim(id, false)
			return unregisteredClientOf(id), nil
		case ours.Code == CodeGrantNotFound:
			return nil, pairEngine(ctx, ours, engine.ErrNotFound)
		default:
			notes.record(ours)
			return nil, ours
		}
	}
	notes.noteClientClaim(id, true)
	return clientViewOf(reg), nil
}

// ── Сверка секрета клиента ──────────────────────────────────────────────────

// clientSecretHasher — хешер секретов в настройках движка, который сверяет
// портом службы (Ports.ClientSecrets).
//
// Движок сверяет секрет клиента на двух путях — доказательство точек токена и
// отзыва и своё у интроспекции, — и оба зовут хешер настроек ровно один раз на
// клиента: прежних проверочных значений у представлений клиента нет (оборот
// секретов — забота службы), и других сверок движок не делает. Хешер движка
// по умолчанию на этих путях не участвует: функцию, цену и сравнение выбирает
// служба.
//
// Хешировать хешер церемонии не умеет: проверочное значение чеканит служба.
type clientSecretHasher struct {
	bridge *storageBridge
}

// Compare сверяет предъявленный секрет портом службы. Проверочного значения,
// которое передаёт движок, у церемонии нет — представления клиента отдают его
// пустым; клиента хешер берёт из ведомости операции.
func (h clientSecretHasher) Compare(ctx context.Context, _, presented []byte) error {
	return h.bridge.verifyClientSecret(ctx, presented)
}

// Hash отказывает: проверочное значение секрета чеканит служба, а не
// церемония (см. clientSecretHasher).
func (clientSecretHasher) Hash(context.Context, []byte) ([]byte, error) {
	return nil, misuse("the ceremony mints no client secret hash; the verification value belongs to the service")
}

var _ engine.Hasher = clientSecretHasher{}

// verifyClientSecret — одна сверка секрета клиента, которого операция
// доказывает.
//
// Исход порта становится ответом движку так:
//   - «совпал» о зарегистрированном клиенте — клиент доказан;
//   - «не совпал» — отказ clientSecretRefused, на который движок отвечает своим
//     отказом доказательства: одним и тем же для неизвестного клиента и для
//     неверного секрета;
//   - отказ порта, вердикт вне словаря, «совпал» о клиенте, которого справочник
//     не знает, и сверка вне доказательства клиента — отказ ОПЕРАЦИИ. Он
//     пишется в ведомость любым случаем: на интроспекции движок отказа хешера
//     не оборачивает, а на точке токена оборачивает в «клиент не доказан», и
//     без записи несостоявшаяся сверка стала бы вердиктом «не совпал».
func (b *storageBridge) verifyClientSecret(ctx context.Context, presented []byte) error {
	const op = "ClientSecretVerifier.VerifyClientSecret"
	notes := notesFrom(ctx)
	fail := func(failure *ProtocolError) error {
		notes.record(failure)
		return failure
	}

	claim, named := notes.claimedClient()
	if !named {
		return fail(failf(CodeCeremonyMisuse, nil,
			"The authorization server was asked to verify a client secret outside a client authentication.", "",
			op+": the operation looked up no client for authentication; the port was not called"))
	}

	ctx, cancel := b.deadline(ctx)
	defer cancel()

	verdict, err := b.ports.ClientSecrets.VerifyClientSecret(ctx, claim.clientID, NewPresentedSecret(string(presented)))
	switch {
	case err != nil:
		return fail(fromPort(op, err))
	case !verdict.Declared():
		return fail(contractBreach(op, "the verdict is neither SecretMatched nor SecretMismatched"))
	case verdict == SecretMatched && !claim.registered:
		return fail(contractBreach(op, "the verifier matched the secret of a client the directory does not know"))
	case verdict == SecretMatched:
		return nil
	default:
		return clientSecretRefused()
	}
}

// clientSecretRefused — «секрет не совпал». Один конструктор и один текст на
// оба случая, которые обязаны быть неотличимы: неизвестный клиент и неверный
// секрет.
func clientSecretRefused() *ProtocolError {
	return failf(CodeInvalidClient, nil, "The client could not be authenticated.", "",
		"ClientSecretVerifier.VerifyClientSecret: the presented secret did not match")
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

// CreateAuthorizeCodeSession кладёт запись кода — грант вместе с привязкой к
// доказательству владения ключом. Привязка доезжает сюда потому, что
// церемония называет оба её поля движку в перечне сохраняемых полей
// (Config.SanitationWhiteList в New).
//
// Негодной привязки здесь быть не может: запрос без неё церемония отвергает
// раньше, чем движок выпускает код (requireProofKey). Если она всё же пришла,
// запись в хранилище не уезжает.
func (b *storageBridge) CreateAuthorizeCodeSession(ctx context.Context, code string, request engine.Requester) error {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	rec, bad := codeRecordFromRequester(request)
	if bad != nil {
		return note(ctx, bad)
	}
	out, err := b.ports.AuthorizationCodes.StoreAuthorizationCode(ctx, code, rec)
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
// Код, погашенный ЭТОЙ ЖЕ операцией, повтором не является: движок выбирает
// код второй раз перед выдачей, уже после того, как церемония погасила его при
// предъявлении (см. consumeCode).
//
// Годный код записывается в ведомость как ПРЕДЪЯВЛЕННЫЙ: по его гранту повтор
// узнаётся на нуле строк погашения, а из его записи движок получает привязку
// PKCE. Живой код без годной привязки S256 — порча записи службой, нарушение
// контракта порта; на погашенном коде привязка не судится — отзыву семейства
// она не нужна, и порча записи не вправе оставить семейство живым.
func (b *storageBridge) GetAuthorizeCodeSession(ctx context.Context, code string, session engine.Session) (engine.Requester, error) {
	ctx, cancel := b.deadline(ctx)
	defer cancel()

	const op = "AuthorizationCodeVault.FetchAuthorizationCode"
	rec, err := b.ports.AuthorizationCodes.FetchAuthorizationCode(ctx, code)
	if err != nil {
		ours := fromPort(op, err)
		switch {
		case ours.Code == CodeAuthorizationCodeConsumed && rec.Grant.GrantID == "":
			return nil, note(ctx, contractBreach(op, "a consumed authorization code was returned without its grant; "+
				"what was issued under a replayed code cannot be revoked without its identifier"))
		case ours.Code == CodeAuthorizationCodeConsumed && !notesFrom(ctx).consumedHere(code):
			return b.replayedCode(ctx, op, rec, session)
		case ours.Code == CodeAuthorizationCodeConsumed:
			// Погашен этой операцией при предъявлении — выборка выдачи.
		case ours.Code == CodeGrantNotFound:
			return nil, pairEngine(ctx, ours, engine.ErrNotFound)
		default:
			return nil, note(ctx, ours)
		}
	}
	if defect := bindingDefect(rec.ProofKey); defect != "" {
		return nil, note(ctx, contractBreach(op, "a live authorization code came back without an S256 proof-key binding: "+defect))
	}
	notesFrom(ctx).notePresentedCode(presentedCode{signature: code, record: rec})
	return requesterFromCode(ctx, b.ports.Clients.LookupClient, rec, session)
}

// replayedCode — ответ на выборку кода, погашенного не этой операцией: повтор.
func (b *storageBridge) replayedCode(ctx context.Context, op string, rec AuthorizationCodeRecord, session engine.Session) (engine.Requester, error) {
	ours := codeReplayed(op)
	notesFrom(ctx).markReplayedFamily(rec.Grant.GrantID, rec.Grant.ClientID, ours, RevocationCodeReplay)
	requester, buildErr := requesterFromGrant(ctx, b.ports.Clients.LookupClient, rec.Grant, session)
	if buildErr != nil {
		// Как у токена обновления: движок получит отказ сборки, а церемония
		// ответит повтором и отзовёт семейство по записанному гранту.
		notesFrom(ctx).record(ours)
		return nil, buildErr
	}
	return requester, pairEngine(ctx, ours, engine.ErrInvalidatedAuthorizeCode)
}

// InvalidateAuthorizeCodeSession гасит код на выдаче — ту же запись, что и
// снятие привязки PKCE (см. consumeCode).
func (b *storageBridge) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	return b.consumeCode(ctx, code)
}

// consumeCode гасит код РОВНО ОДИН РАЗ за операцию.
//
// Движок гасит код дважды за обмен и разными словами: снимает привязку PKCE
// при предъявлении, до сверки доказательства (DeletePKCERequestSession), и
// гасит код на выдаче (InvalidateAuthorizeCodeSession). У службы и привязка, и
// код — одна запись, и оба действия означают одно: код использован. Первое из
// них гасит запись и заносит погашение в ведомость; второе, найдя его там,
// порта не зовёт.
//
// ЗДЕСЬ И ТОЛЬКО ЗДЕСЬ решается, состоится ли обмен: ноль затронутых строк
// означает, что код погасил кто-то другой, — ОДНОВРЕМЕННЫЙ ПОВТОР: выборку
// кода прошли двое. Обмен обязан не состояться, а семейство гранта — умереть
// вместе с парой, которую получил опередивший: сервер не знает, который из
// двоих законный. Грант берётся из ведомости (код выбран в этой же операции),
// и отзыв исполняет церемония.
func (b *storageBridge) consumeCode(ctx context.Context, code string) error {
	if notesFrom(ctx).consumedHere(code) {
		return nil
	}

	ctx, cancel := b.deadline(ctx)
	defer cancel()

	const op = "AuthorizationCodeVault.ConsumeAuthorizationCode"
	out, err := b.ports.AuthorizationCodes.ConsumeAuthorizationCode(ctx, code)
	if bad := checkDeclared(op, out, err); bad != nil {
		return note(ctx, bad)
	}
	switch {
	case out.Rows() == 1:
		notesFrom(ctx).noteConsumed(code)
		return nil
	case out.Rows() == 0:
		ours := codeReplayed(op)
		if presented, known := notesFrom(ctx).presentedCodeOf(code); known {
			notesFrom(ctx).markReplayedFamily(presented.record.Grant.GrantID, presented.record.Grant.ClientID,
				ours, RevocationCodeReplay)
		}
		return pairEngine(ctx, ours, engine.ErrInvalidatedAuthorizeCode)
	default:
		return note(ctx, contractBreach(op, "the single statement touched more than one row"))
	}
}

// codeReplayed — наш отказ на повтор кода. Один конструктор на все пути,
// которыми повтор замечается: выборку погашенного кода и ноль строк погашения.
func codeReplayed(op string) *ProtocolError {
	return failf(CodeAuthorizationCodeConsumed, nil,
		"The authorization code was already redeemed.",
		"Every authorization code may be redeemed exactly once; what was issued under it has been revoked.", op)
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
		notes := notesFrom(ctx)
		if failure := notes.unidentifiedFailure(); failure != nil {
			notes.record(failure)
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
//
// Привязка кода к доказательству — поля ЗАПИСИ КОДА (AuthorizationCodeRecord),
// и отдельного хранилища у неё нет. Три метода ниже — часть контракта
// хранилища обработчика PKCE движка, и каждый переводит его слово на эту одну
// запись.

// CreatePKCERequestSession: писать нечего. Обработчик PKCE движка стоит ПОСЛЕ
// обработчика кода (порядок закреплён в New) и привязывает к коду то, что уже
// уехало в хранилище вместе с кодом: оба берут поля из одного и того же
// запроса авторизации. Вторая запись под подписью кода была бы вторым ходом
// там, где служба держит одну строку.
func (b *storageBridge) CreatePKCERequestSession(context.Context, string, engine.Requester) error {
	return nil
}

// GetPKCERequestSession отдаёт привязку PKCE по подписи кода — из записи кода,
// выбранной в этой же операции: обработчик кода движка выбирает код раньше,
// чем обработчик PKCE спрашивает привязку, и отказ той выборки до обработчика
// PKCE не доходит. Спрос привязки у кода, которого операция не выбирала, —
// дефект провязки, а не «PKCE не было»: ответить «записи нет» значило бы
// позволить движку судить код без привязки.
func (b *storageBridge) GetPKCERequestSession(ctx context.Context, signature string, session engine.Session) (engine.Requester, error) {
	presented, known := notesFrom(ctx).presentedCodeOf(signature)
	if !known {
		return nil, failf(CodeCeremonyMisuse, nil,
			"The authorization server was asked for the proof key of a code it did not read.", "",
			"engine storage: GetPKCERequestSession came before GetAuthorizeCodeSession of the same code")
	}

	ctx, cancel := b.deadline(ctx)
	defer cancel()

	return requesterFromCode(ctx, b.ports.Clients.LookupClient, presented.record, session)
}

// DeletePKCERequestSession — привязку снимают вместе с кодом: при
// предъявлении, до сверки доказательства, код гасится (см. consumeCode).
// Предъявление и есть использование кода: код, предъявленный с неверным
// доказательством, второго предъявления не получает — оно отвечается повтором.
func (b *storageBridge) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return b.consumeCode(ctx, signature)
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
