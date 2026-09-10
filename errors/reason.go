// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package errors

import (
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Reason — машинный признак полосы резолва, по которому клиент отличает
// «я не нашёл СВОЁ» от «предусловие на ЧУЖОЙ ресурс не выполнено», не разбирая
// прозу сообщения (api-conventions.md §By-lane code-split).
//
// # Почему тип, а не строка
//
// Токен, переданный строкой, выразим любой: шестая полоса заводится опечаткой и
// доезжает до клиента молча, потому что клиент ключуется на равенство и просто
// не совпадёт — то есть отказ будет выглядеть как отсутствие признака. Поля здесь
// неэкспортируемые, поэтому за пределами пакета собрать значение с произвольным
// токеном НЕЛЬЗЯ: словарь закрыт компилятором, а не соглашением.
//
// # Почему код лежит ВНУТРИ полосы
//
// Токен и код — две половины одного утверждения о полосе. Разъехавшись, они дают
// худший из возможных исходов: ответ, который машинно заявляет одну полосу, а
// кодом — другую. Держать их вместе значит сделать расхождение невыразимым, а не
// обнаруживаемым. Отсюда же следует, что смена канона полосы — правка ОДНОЙ
// строки здесь, а не тринадцати мест в сервисах.
type Reason struct {
	token string
	code  codes.Code
}

// Пять полос закрытого словаря. Шестой нет и не может быть заведена снаружи;
// добавление шестой здесь роняет TestLaneDictionaryIsClosedAtFive, который
// требует объявить её контрактом, а не просто вписать значение.
//
// Каждое значение — конструктор ошибок своей полосы: `ReasonX.Errf(...)`.
// Отдельных функций-конструкторов на полосу нет намеренно — три из пяти полос
// сегодня не имеют производителя в дереве, и такие функции были бы мёртвым
// кодом (ban #11), тогда как сам ЗАКРЫТЫЙ НАБОР мёртвым не бывает: он и есть
// то, что запрещает шестую.
//
// #nosec G101 -- ВСЕ ПЯТЬ токенов ниже суть МАШИННЫЙ ПРИЗНАК ПОЛОСЫ ОТКАЗА,
// уезжающий клиенту в деталях ответа, а не секрет. Эвристика статического
// анализа ключуется на форме имени — заглавные с подчёркиваниями рядом со
// строковым литералом — и не различает токен публичного контракта от учётных
// данных. Обоснование стоит здесь, у ГРУППЫ, а не построчно: предмет у всех
// пяти один, и построчные пометки закрывали бы по одной, оставляя следующую
// находкой при каждом добавлении полосы.
var (
	// ReasonInvalidResourceID — sync-формат: malformed own-id, отвергнутый
	// первым стейтментом RPC.
	ReasonInvalidResourceID = Reason{token: "INVALID_RESOURCE_ID", code: codes.InvalidArgument}

	// ReasonResourceNotFound — direct-read: own-owned id корректен, строки в
	// своей БД нет.
	ReasonResourceNotFound = Reason{token: "RESOURCE_NOT_FOUND", code: codes.NotFound}

	// ReasonPeerResourceMissing — peer-validate: чужой id не существует у
	// владельца. Код — FAILED_PRECONDITION: консумер здесь не «не нашёл своё»,
	// а «предусловие на чужой ресурс не выполнено».
	ReasonPeerResourceMissing = Reason{token: "PEER_RESOURCE_MISSING", code: codes.FailedPrecondition}

	// ReasonPeerResourceState — peer-validate: чужой ресурс есть, состояние не
	// позволяет.
	ReasonPeerResourceState = Reason{token: "PEER_RESOURCE_STATE", code: codes.FailedPrecondition}

	// ReasonPeerUnavailable — peer-validate: владелец недоступен. Fail-closed
	// для мутаций — непроверяемое предусловие не считается выполненным.
	ReasonPeerUnavailable = Reason{token: "PEER_UNAVAILABLE", code: codes.Unavailable}
)

// AllReasons возвращает словарь полос целиком. Нужен проверкам, утверждающим
// свойство НАБОРА (закрытость, отсутствие дублей), — без него они перечисляли бы
// полосы вручную и молчали бы ровно о той, которую забыли дописать.
func AllReasons() []Reason {
	return []Reason{
		ReasonInvalidResourceID,
		ReasonResourceNotFound,
		ReasonPeerResourceMissing,
		ReasonPeerResourceState,
		ReasonPeerUnavailable,
	}
}

// Token — машинный признак полосы, как он уезжает в google.rpc.ErrorInfo.reason.
func (r Reason) Token() string { return r.token }

// Code — gRPC-код полосы.
func (r Reason) Code() codes.Code { return r.code }

// String — реализация fmt.Stringer, чтобы полоса читалась в логах именем, а не
// раскладкой структуры.
func (r Reason) String() string { return r.token }

// IsDeclared отличает полосу словаря от нулевого значения типа. Нулевое значение
// собрать можно (`var r Reason`) — Go этого не запрещает; значимо то, что оно
// НЕ выдаёт себя за полосу контракта.
func (r Reason) IsDeclared() bool { return r.token != "" }

// PeerRef — координата ресурса, о котором отказ.
//
// Service — имя сервиса-источника отказа («vpc»), из которого собирается
// ErrorInfo.domain вида "<service>.kacho.cloud".
//
// ResourceID пуст там, где полоса намеренно не подтверждает существование чужого
// ресурса (анти-oracle). Пустое значение НЕ едет в метаданные пустой строкой:
// ключ с пустым значением читается как «идентификатор известен и пуст», то есть
// сообщает ровно то, что скрытие и должно было закрыть.
type PeerRef struct {
	Service      string
	ResourceType string
	ResourceID   string
}

// Errf собирает отказ этой полосы: код берётся у полосы, проза — у вызывающего,
// машинный признак уезжает в details.
//
// Проза НЕ выводится из полосы и не дополняется ею. Тексты — часть контракта
// Kachō и принадлежат вызывающему; полоса добавляет к сказанному только машинный
// признак, по которому клиент отличает «повтори позже» от «исправь ввод», не
// парся сообщение. Детали не влияют на HTTP-статус края (grpc-gateway отображает
// по КОДУ), поэтому постановка признака ничего не ломает у REST-клиента.
//
// Необъявленная полоса (нулевое значение) отдаёт INTERNAL без деталей: отказ,
// у которого нет полосы, не вправе притвориться полосой контракта.
func (r Reason) Errf(ref PeerRef, format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	if !r.IsDeclared() {
		return status.Error(codes.Internal, msg)
	}
	st := status.New(r.code, msg)

	meta := map[string]string{}
	if ref.ResourceType != "" {
		meta["resource_type"] = ref.ResourceType
	}
	if ref.ResourceID != "" {
		meta["resource_id"] = ref.ResourceID
	}

	withDetails, derr := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   r.token,
		Domain:   ref.Service + ".kacho.cloud",
		Metadata: meta,
	})
	if derr != nil {
		// Деталь не прикрепилась — код полосы важнее детали, отдаём его как есть.
		return st.Err()
	}
	return withDetails.Err()
}
