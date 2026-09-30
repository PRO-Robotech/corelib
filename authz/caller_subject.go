// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
)

// ServiceSubjectType — тип служебного субъекта модели прав.
//
// Тенантский словарь ([FormatSubject], [TenantSubject]) этого слова не знает и
// не узнаёт: служебный субъект производится только из проверенного сертификата
// звеном идентичности служб, а не из заголовков личности, которые назначает себе
// отправитель.
const ServiceSubjectType = "service"

// ServiceSubject — ЕДИНСТВЕННЫЙ производитель строки `service:<имя>`.
//
// Его зовут [CallerSubject] и применитель манифестов владельца модели. Имя вне
// формы DNS label строки не даёт: пустой результат fail-closed — такая строка не
// совпадёт ни с одной выдачей и не будет записана.
func ServiceSubject(name grpcsrv.ServiceName) string {
	if !name.Valid() {
		return ""
	}
	return ServiceSubjectType + ":" + string(name)
}

// Caller — вызывающий, названный [CallerSubject]: либо пересланный доверенным
// принципал, либо служба, опознанная звеном идентичности. Нулевое значение
// никого не называет.
type Caller struct {
	forwarded operations.Principal
	service   grpcsrv.ServiceName
	isService bool
	named     bool
}

// Subject — субъект вопроса к модели прав.
//
// Для пересланного принципала — прежний кодек звена прав [FormatSubject]
// (словарь не расширяется); для службы — [ServiceSubject].
func (c Caller) Subject() string {
	switch {
	case !c.named:
		return ""
	case c.isService:
		return ServiceSubject(c.service)
	default:
		return FormatSubject(c.forwarded.Type, c.forwarded.ID)
	}
}

// PrincipalID — ключ корзины бюджета отказов.
//
// У службы ключ — строка С ТИПОМ `service:<имя>`, а не голое имя (CX1-03):
// иначе служба `x` и пользователь `x` делили бы одну корзину, и шторм отказов
// одного отнимал бы бюджет у другого.
func (c Caller) PrincipalID() string {
	switch {
	case !c.named:
		return ""
	case c.isService:
		return ServiceSubject(c.service)
	default:
		return c.forwarded.ID
	}
}

// Service — имя службы, если вызывающий — служба.
func (c Caller) Service() (grpcsrv.ServiceName, bool) {
	if !c.named || !c.isService {
		return "", false
	}
	return c.service, true
}

// Forwarded — пересланный принципал, если решил он.
func (c Caller) Forwarded() (operations.Principal, bool) {
	if !c.named || c.isService {
		return operations.Principal{}, false
	}
	return c.forwarded, true
}

// StepUpType — тип принципала для [grpcsrv.EvaluateStepUp].
//
// Служба оценивается как машинный принципал: интерактивной церемонии у неё нет,
// и пол `acr` для неё недостижим by construction. Значение выводится из
// сертификата, а не из заголовков, поэтому исключение не покупается подделкой
// типа. Для пересланного принципала — его тип (он уже сужен кругом
// пересылающих); для «никого» — пустая строка, освобождения не дающая.
func (c Caller) StepUpType() string {
	switch {
	case !c.named:
		return ""
	case c.isService:
		return grpcsrv.PrincipalTypeServiceAccount
	default:
		return c.forwarded.Type
	}
}

// CallerSubject — ОДНА функция субъекта вызывающего. Её зовут извлекатель звена
// прав и субъект сужения списков (`sec-one-predicate-three-readers`).
//
// Порядок решения, и он несущий:
//
//  1. есть пересланный доверенным принципал — он решает. Анонимность, тип
//     `service` и идентификатор с разделителями модели субъекта не дают, и
//     сертификату такой вызов НЕ уступается: пересылка, которая никого не
//     назвала, остаётся решением «никого»;
//  2. иначе, если звено идентичности положило имя службы, — `service:<имя>`;
//  3. иначе — «субъекта нет».
//
// Второй носитель читается ЗДЕСЬ и больше нигде.
func CallerSubject(ctx context.Context) (Caller, bool) {
	if p, forwarded := operations.PrincipalFromContextOK(ctx); forwarded {
		if p.IsAnonymous() || p.Type == ServiceSubjectType || !validSubjectID(p.ID) {
			return Caller{}, false
		}
		return Caller{forwarded: p, named: true}, true
	}
	if name, recognized := grpcsrv.ServiceNameFromContext(ctx); recognized && name.Valid() {
		return Caller{service: name, isService: true, named: true}, true
	}
	return Caller{}, false
}
