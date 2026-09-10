// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// Principal — кто инициировал операцию. Заполняется auth-interceptor'ом
// api-gateway. Без auth — stub {Type: "system", ID: "bootstrap",
// DisplayName: "System"}.
//
// Семантика полей:
//   - Type: "user" | "service_account" | "system"
//   - ID:   "usr-..." | "sva-..." | "bootstrap"
//   - DisplayName: human-readable email / SA-name / "System"
//
// Каждая запись в operations-таблице хранит эти поля в колонках
// principal_type / principal_id / principal_display_name (миграция
// migrations/common/0002_operations_principal.sql).
type Principal struct {
	Type        string
	ID          string
	DisplayName string
}

// AnonymousPrincipalID — зарезервированное слово, которым edge помечает запрос
// БЕЗ credential'а (`Principal{system, anonymous}`). Настоящего принципала с
// таким id не существует: реальные id — префиксованный crockford-base32
// (`usr-…`/`sva-…`), их пространство с этим словом не пересекается.
//
// Слово существует только как ярлык для аудита и логов. Оно НЕ является
// личностью, и любое сравнение личностей обязано это учитывать — см.
// Principal.IsAnonymous.
const AnonymousPrincipalID = "anonymous"

// SystemPrincipal — stub principal для control-plane bootstrap'а / системных
// операций (миграции, фоновый retry, тесты без auth). Также используется как
// дефолт в CreateWithPrincipal-обертке legacy-Create и в PrincipalFromContext
// для пустого ctx.
func SystemPrincipal() Principal {
	return Principal{Type: "system", ID: "bootstrap", DisplayName: "System"}
}

// IsAnonymous сообщает, что принципал не называет НИКОГО.
//
// Таких случая два, и они обязаны трактоваться одинаково:
//   - пара пуста (принципала не было вовсе);
//   - пара несёт зарезервированное слово AnonymousPrincipalID — «неизвестно
//     кто», выданное запросу без credential'а.
//
// Второй случай — источник целого класса дефектов: как только у «неизвестно
// кто» появляется ИМЯ, любое сравнение с этим именем даёт истину. Два
// независимых безымянных запроса получают один и тот же ключ и оказываются
// друг для друга «своими»; гейт вида «личность извлеклась ⇒ аутентифицирован»
// на нём срабатывает. Поэтому именованная анонимность обязана вести себя как
// отсутствие принципала везде, где принимается решение о личности.
//
// Тип при этом не проверяется намеренно: слово зарезервировано целиком, и
// «неизвестно кто» с любым заявленным типом остаётся неизвестно кем.
//
// НЕ включает ЯВНО установленный системный принципал (`{system, bootstrap}`):
// это настоящая bootstrap-личность доверенного internal-пути, она остаётся
// владельцем своих операций. Сужается именно анонимность.
func (p Principal) IsAnonymous() bool {
	return p.Type == "" || p.ID == "" || p.ID == AnonymousPrincipalID
}

// IsSystem сообщает, что принципал — ЯВНАЯ bootstrap-личность внутреннего пути
// (`{system, bootstrap}`, см. SystemPrincipal).
//
// Пара сверяется с SystemPrincipal(), а не с двумя написанными здесь словами:
// иначе имя внутренней личности объявлялось бы в дереве второй раз, и второе
// объявление разошлось бы с первым молча. Именно так оно и было — край
// перечислял эту пару голыми строками дважды, в двух соседних условиях.
//
// НЕ то же, что IsAnonymous, и путать их нельзя. Аноним не называет НИКОГО;
// bootstrap-личность называет вполне определённого внутреннего вызывающего и
// остаётся владельцем своих операций. Различение делает и authz-слой, и вывод
// ключа владения (OwnerFromContext).
//
// DisplayName не сверяется намеренно: он косметический, приходит из разных
// мест и решением о личности быть не может.
func (p Principal) IsSystem() bool {
	sys := SystemPrincipal()
	return p.Type == sys.Type && p.ID == sys.ID
}

// Operation — domain-тип, зеркалит proto Operation.
// Используется repo / service-слоями внутри Kachō-сервисов.
type Operation struct {
	ID          string
	Description string
	CreatedAt   time.Time
	CreatedBy   string
	ModifiedAt  time.Time
	Done        bool
	Metadata    *anypb.Any     // специфичные метаданные (CreateInstanceMetadata и т.д.)
	Error       *status.Status // заполнен если done && ошибка
	Response    *anypb.Any     // заполнен если done && успех — финальное состояние ресурса

	// ResourceID — id ресурса-владельца операции для денормализованного индекса
	// operations.resource_id (фильтр List(ListFilter{ResourceID})). Если задан
	// use-case'ом явно — используется как есть; если пуст — repo падает на
	// reflection-fallback (первое `*_id`-поле Metadata). Явное значение НАДЁЖНЕЕ:
	// reflection-угадывание «первое _id == owning resource» ошибётся, если в
	// *Metadata первым объявлено не-owning поле (project_id/parent_id/…). Ставьте
	// его при конструировании операции.
	ResourceID string

	// Principal — кто инициировал операцию (kaname-resolved). Без auth
	// заполняется SystemPrincipal(); при наличии auth-ctx — из него через
	// PrincipalFromContext.
	Principal Principal
}
