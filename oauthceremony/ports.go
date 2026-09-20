// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"time"
)

// ── Число затронутых строк как ТИП ──────────────────────────────────────────

// StoreOutcome — исход записи: сколько строк она затронула.
//
// # Зачем отдельный тип, а не int64
//
// Контракт всех пишущих портов здесь — ОДНОИНСТРУКЦИОННАЯ запись с условием
// в самой инструкции:
//
//	UPDATE authorization_codes
//	   SET consumed_at = now()
//	 WHERE signature = $1 AND consumed_at IS NULL
//
// и возврат `tag.RowsAffected()`. Противоположный способ — «прочитать,
// проверить в коде, потом записать» — между чтением и записью оставляет окно,
// в котором второй запрос успевает погасить тот же код. Никакая
// изолированность транзакции этого окна не закрывает, если чтение и запись
// разнесены по разным инструкциям без блокировки.
//
// Если бы метод возвращал `(int64, error)`, реализация, забывшая передать
// число, вернула бы ноль — и ноль неотличим от честного «строк не затронуто».
// Обе ветви выглядели бы одинаково, и ошибка реализации стала бы ошибкой
// протокола на проводе.
//
// StoreOutcome отличает «не названо» от «названо нулём». Нулевое значение
// типа НЕ ОБЪЯВЛЕНО, и церемония отвергает его как нарушение контракта порта
// (ErrPortContract), а не как «строк не затронуто». Собрать объявленный исход
// можно ТОЛЬКО через RowsTouched, то есть только назвав число.
//
// # Что это НЕ гарантирует
//
// Тип не может заставить реализацию исполнить одну инструкцию вместо двух. Он
// делает другое: делает невозможным ВЕРНУТЬ исход, не назвав число, и делает
// видимым число, отличное от единицы. Реализация «проверить-потом-записать»,
// честно вернувшая RowsTouched(1) после гонки, всё равно даст двойное
// погашение — и это ловится пробой службы на конкурентное погашение, не
// типом. Тип закрывает МОЛЧАЛИВЫЙ путь, а не все пути.
type StoreOutcome struct {
	// declared — исход собран конструктором, а не нулевым значением.
	declared bool
	// rows — сколько строк затронула инструкция.
	rows int64
}

// RowsTouched собирает исход записи из числа, которое вернул драйвер.
//
//	tag, err := tx.Exec(ctx, q, sig)
//	if err != nil { return oauthceremony.StoreOutcome{}, err }
//	return oauthceremony.RowsTouched(tag.RowsAffected()), nil
//
// Отрицательное число — тоже законный вход: драйвер, который не умеет считать
// строки, возвращает −1, и церемония обязана увидеть это как нарушение
// контракта, а не превратить в ноль по дороге.
func RowsTouched(n int64) StoreOutcome { return StoreOutcome{declared: true, rows: n} }

// Rows — число затронутых строк. Осмысленно только при Declared() == true.
func (o StoreOutcome) Rows() int64 { return o.rows }

// Declared — назван ли исход. Ложь означает нулевое значение типа, то есть
// реализацию, вернувшую исход в обход RowsTouched.
func (o StoreOutcome) Declared() bool { return o.declared }

// ── Порты ───────────────────────────────────────────────────────────────────
//
// # Кто это реализует
//
// НИКТО В ФУНДАМЕНТЕ. Все интерфейсы ниже реализует СЛУЖБА, владеющая базой:
// сегодня это служба удостоверения (`kacho-iam`), завтра — любая другая,
// решившая быть сервером авторизации. Фундамент объявляет форму и требования
// и не приносит ни одной реализации, даже «в памяти для проб»: реализация в
// памяти в фундаменте немедленно стала бы тем, на что сошлются в рабочем
// пути. Пробы этого пакета носят свои подставки в файлах `_test.go`, то есть
// в графе сборки потребителя их нет.
//
// # Что общего у всех портов
//
// Порт отвечает НАШИМИ ошибками (ErrGrantNotFound, ErrStorageConflict,
// ErrAuthorizationCodeConsumed, ErrPortContract) либо любой своей — тогда
// церемония переложит её в CodeServerError, сохранив текст в Debug. Порт НЕ
// обязан знать ни о движке, ни о его системе ошибок.
//
// Срок каждого вызова назначает церемония: она передаёт порту контекст с
// дедлайном Config.PortTimeout. Порт обязан этот контекст уважать и обязан
// вернуть отказ, а не висеть.

// ClientDirectory — откуда берутся записи клиентов.
//
// Реализует СЛУЖБА. Ожидается чтение из таблицы клиентов с кэшем на стороне
// службы; церемония не кэширует ничего — отзыв клиента обязан действовать
// немедленно.
type ClientDirectory interface {
	// LookupClient отдаёт запись клиента по его публичному
	// идентификатору.
	//
	// Клиента нет → ErrGrantNotFound. Отдавать пустую запись без ошибки
	// запрещено: «клиента нет» и «клиент есть, но пуст» — разные события.
	LookupClient(ctx context.Context, clientID string) (ClientRegistration, error)
}

// AuthorizationCodeVault — хранилище кодов авторизации.
//
// Реализует СЛУЖБА.
//
// # ГДЕ ЗДЕСЬ АТОМАРНОСТЬ И ПОЧЕМУ ОНА ЦЕЛИКОМ НА НАШЕЙ СТОРОНЕ
//
// Измерено по внесённому движку (`internal/oauth2/handler/oauth2/
// flow_authorize_code_token.go`, ревизия 3726c54): чтение кода стоит на
// строке 126, открытие транзакции — на строке 155, погашение — на строке 167.
// То есть движок читает код СНАРУЖИ транзакции, а гасит ВНУТРИ, и между этими
// двумя действиями нет ни блокировки, ни повторной проверки. Два
// одновременных обмена одним кодом оба проходят чтение и оба доходят до
// погашения.
//
// Значит защита от повторного использования кода — РОВНО ОДНА инструкция
// ConsumeAuthorizationCode, и никакая часть её не лежит в движке.
type AuthorizationCodeVault interface {
	// StoreAuthorizationCode кладёт грант под подписью кода.
	//
	// Подпись уникальна: ожидается INSERT, затронувший ровно одну строку.
	// Затронуто 0 → ErrPortContract (вставка, ничего не вставившая, —
	// дефект). Затронуто больше 1 → ErrPortContract (подпись не
	// уникальна). Столкновение подписей → ErrStorageConflict.
	StoreAuthorizationCode(ctx context.Context, signature string, grant GrantRecord) (StoreOutcome, error)

	// FetchAuthorizationCode отдаёт грант по подписи кода.
	//
	// Исходов ТРИ, и все три названы:
	//
	//  1. Код есть и не погашен → (грант, nil).
	//  2. Кода нет вовсе → (пустой грант, ErrGrantNotFound).
	//  3. Код есть и УЖЕ ПОГАШЕН → (ГРАНТ, ErrAuthorizationCodeConsumed).
	//
	// Третий исход обязан отдавать грант ВМЕСТЕ с ошибкой. Это не
	// небрежность контракта: обнаружив повторное предъявление кода,
	// движок отзывает ВСЕ артефакты того гранта (RFC 6749 §4.1.2,
	// замечание о безопасности), а для отзыва ему нужен идентификатор
	// гранта. Вернув пустой грант, реализация превратила бы обнаруженную
	// атаку в тихий отказ, оставив выданные токены живыми.
	FetchAuthorizationCode(ctx context.Context, signature string) (GrantRecord, error)

	// ConsumeAuthorizationCode гасит код.
	//
	// ОДНОЙ ИНСТРУКЦИЕЙ, с условием внутри неё:
	//
	//	UPDATE authorization_codes
	//	   SET consumed_at = now()
	//	 WHERE signature = $1 AND consumed_at IS NULL
	//
	// и возвратом RowsTouched(tag.RowsAffected()).
	//
	// Исходы по числу строк:
	//   1 → код погашен нами; обмен продолжается;
	//   0 → код уже был погашен (нас обогнали) → церемония отвечает
	//       ErrAuthorizationCodeConsumed, и обмен не состоится;
	//   иное → ErrPortContract: подпись не уникальна либо драйвер не
	//       умеет считать строки.
	//
	// ЗАПРЕЩЕНО: читать, сравнивать в коде и потом писать. Запрещено
	// возвращать StoreOutcome{} — незаполненный исход отвергается как
	// нарушение контракта, а не толкуется как ноль строк.
	ConsumeAuthorizationCode(ctx context.Context, signature string) (StoreOutcome, error)
}

// AccessTokenVault — хранилище токенов доступа.
//
// Реализует СЛУЖБА.
type AccessTokenVault interface {
	// StoreAccessToken кладёт грант под подписью токена доступа.
	// Ожидается ровно одна затронутая строка.
	StoreAccessToken(ctx context.Context, signature string, grant GrantRecord) (StoreOutcome, error)

	// FetchAccessToken отдаёт грант по подписи. Нет → ErrGrantNotFound.
	FetchAccessToken(ctx context.Context, signature string) (GrantRecord, error)

	// DropAccessToken снимает токен доступа по подписи.
	//
	// Затронуто 0 строк — ЗАКОННЫЙ исход, а не отказ: снятие
	// несуществующего артефакта идемпотентно по RFC 7009 §2.2, и служба
	// не обязана знать, кто снял его первым. Больше одной строки →
	// ErrPortContract.
	DropAccessToken(ctx context.Context, signature string) (StoreOutcome, error)
}

// RefreshTokenVault — хранилище токенов обновления.
//
// Реализует СЛУЖБА.
type RefreshTokenVault interface {
	// StoreRefreshToken кладёт грант под подписью токена обновления,
	// связывая его с подписью выпущенного вместе с ним токена доступа.
	// Связь нужна, чтобы отзыв одного снимал второй.
	StoreRefreshToken(ctx context.Context, signature, accessSignature string, grant GrantRecord) (StoreOutcome, error)

	// FetchRefreshToken отдаёт грант по подписи. Нет → ErrGrantNotFound.
	FetchRefreshToken(ctx context.Context, signature string) (GrantRecord, error)

	// DropRefreshToken снимает токен обновления по подписи. Ноль строк —
	// законный исход, как и у DropAccessToken.
	DropRefreshToken(ctx context.Context, signature string) (StoreOutcome, error)

	// RotateRefreshToken помечает токен обновления использованным при
	// обороте.
	//
	// ОДНОЙ ИНСТРУКЦИЕЙ, по тем же основаниям, что и погашение кода:
	//
	//	UPDATE refresh_tokens
	//	   SET rotated_at = now()
	//	 WHERE grant_id = $1 AND signature = $2 AND rotated_at IS NULL
	//
	// Исходы: 1 → оборот наш; 0 → токен уже обернули (повторное
	// предъявление) → ErrAuthorizationCodeConsumed по смыслу «артефакт
	// уже израсходован»; иное → ErrPortContract.
	RotateRefreshToken(ctx context.Context, grantID, signature string) (StoreOutcome, error)
}

// GrantRevoker — отзыв всех артефактов одного гранта.
//
// Реализует СЛУЖБА.
type GrantRevoker interface {
	// RevokeGrantRefreshTokens снимает ВСЕ токены обновления гранта.
	// Ноль строк — законный исход (нечего снимать).
	RevokeGrantRefreshTokens(ctx context.Context, grantID string) (StoreOutcome, error)

	// RevokeGrantAccessTokens снимает ВСЕ токены доступа гранта.
	// Ноль строк — законный исход.
	RevokeGrantAccessTokens(ctx context.Context, grantID string) (StoreOutcome, error)
}

// ProofKeyVault — хранилище доказательств владения ключом (PKCE, RFC 7636).
//
// Реализует СЛУЖБА. Хранит запрос авторизации под подписью КОДА, чтобы при
// обмене можно было сверить `code_verifier` с `code_challenge`.
type ProofKeyVault interface {
	// StoreProofKeyRequest кладёт запрос под подписью кода. Ожидается
	// ровно одна затронутая строка.
	StoreProofKeyRequest(ctx context.Context, signature string, grant GrantRecord) (StoreOutcome, error)

	// FetchProofKeyRequest отдаёт запрос по подписи. Нет →
	// ErrGrantNotFound.
	FetchProofKeyRequest(ctx context.Context, signature string) (GrantRecord, error)

	// DropProofKeyRequest снимает запрос по подписи. Ноль строк —
	// законный исход.
	DropProofKeyRequest(ctx context.Context, signature string) (StoreOutcome, error)
}

// AssertionReplayGuard — защита утверждений клиента от повторного
// предъявления (RFC 7523 §3, OIDC Core §9).
//
// Реализует СЛУЖБА.
//
// # Почему здесь ОДИН метод, а у движка их ДВА
//
// Движок спрашивает хранилище «этот `jti` известен?» и, получив «нет», ПОЗЖЕ
// говорит «запомни его». Между вопросом и ответом — окно, в которое
// укладывается повторное предъявление того же утверждения.
//
// Наш порт этого окна не открывает: он объявляет одно действие —
// «зарезервировать `jti`», исполняемое одной инструкцией
// `INSERT … ON CONFLICT DO NOTHING`. Проверка движка обслуживается из
// церемонии как заведомо разрешающая, а настоящее решение принимается по
// числу вставленных строк. Это сужение чужого контракта до более строгого, и
// оно законно: всякий, кто проходил у движка, проходит и здесь, а часть тех,
// кто проходил дважды, теперь не проходит.
type AssertionReplayGuard interface {
	// ClaimAssertionID резервирует идентификатор утверждения до
	// указанного срока.
	//
	// ОДНОЙ ИНСТРУКЦИЕЙ:
	//
	//	INSERT INTO client_assertions (assertion_id, expires_at)
	//	VALUES ($1, $2) ON CONFLICT (assertion_id) DO NOTHING
	//
	// Исходы: 1 → резерв наш, утверждение принимается; 0 → утверждение
	// уже предъявлялось → ErrAssertionReplayed; иное → ErrPortContract.
	//
	// Уборка просроченных записей — забота службы, а не церемонии:
	// церемония не знает ни расписания, ни размера таблицы.
	ClaimAssertionID(ctx context.Context, assertionID string, expiresAt time.Time) (StoreOutcome, error)
}

// UnitOfWork — НЕОБЯЗАТЕЛЬНЫЙ порт единицы работы.
//
// Реализует СЛУЖБА, если её хранилище умеет транзакции.
//
// # Что он даёт и чего НЕ даёт
//
// Движок оборачивает транзакцией связку «погасить код + положить токен
// доступа + положить токен обновления»: либо уедет всё, либо ничего.
// Транзакция НЕ защищает от повторного использования кода — чтение кода
// стоит до её открытия (см. AuthorizationCodeVault). Один порт закрывает
// частичную запись, другой — гонку; подменять один другим нельзя.
//
// Если порт не назван, церемония собирает мост БЕЗ транзакционных методов, и
// движок, проверяющий их наличие утверждением типа, честно видит хранилище
// без транзакций. Мост с методами-пустышками солгал бы движку, и связка выше
// перестала бы быть неделимой молча.
type UnitOfWork interface {
	// Begin открывает единицу работы и возвращает контекст, несущий её.
	// Все последующие вызовы портов придут с этим контекстом.
	Begin(ctx context.Context) (context.Context, error)

	// Commit закрепляет единицу работы, найденную в контексте.
	Commit(ctx context.Context) error

	// Rollback отменяет единицу работы, найденную в контексте.
	Rollback(ctx context.Context) error
}

// Ports — полный набор портов церемонии.
//
// Все поля, кроме Transaction, ОБЯЗАТЕЛЬНЫ: New отвергает набор с пустым
// полем поимённо. Необязательное хранилище означало бы ветку «а если его
// нет», то есть второй, непроверяемый путь исполнения.
type Ports struct {
	Clients            ClientDirectory
	AuthorizationCodes AuthorizationCodeVault
	AccessTokens       AccessTokenVault
	RefreshTokens      RefreshTokenVault
	Grants             GrantRevoker
	ProofKeys          ProofKeyVault
	Assertions         AssertionReplayGuard

	// Transaction — необязателен. Пусто означает «хранилище службы не
	// умеет транзакций», и церемония ведёт себя соответственно.
	Transaction UnitOfWork
}
