// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
)

// defaultSubjectExtractor — стандартная реализация: зовёт ОДНУ функцию субъекта
// [CallerSubject], ту же, что субъект сужения списков.
//
// Возвращает:
//   - subjectFGA — "user:usr_xxx", "service_account:sva_xxx" либо
//     "service:<имя>" для службы, опознанной звеном идентичности;
//   - principalID — ключ корзины бюджета отказов: raw ID пересланного
//     принципала либо "service:<имя>" с типом (CX1-03);
//   - ok — false, если [CallerSubject] никого не назвал.
//
// # Анонимность не становится субъектом
//
// Субъект — это то, о чём спрашивают модель прав. Пара, которая не называет
// никого, субъектом стать не может: у модели есть намеренные ПОДСТАНОВОЧНЫЕ
// выдачи (глобальный справочник платформы открыт всякому АУТЕНТИФИЦИРОВАННОМУ
// тенанту), а подстановка выполняется любой строкой подходящей формы. Стоит
// «неизвестно кто» получить форму субъекта — и выдача, задуманная для
// аутентифицированных, начинает отвечать «да» тому, кто не аутентифицировался.
//
// Отказ стоит ДО любого вопроса модели и живёт в [CallerSubject]: признак
// анонимности — общий предикат operations.Principal.IsAnonymous, идентификатор с
// разделителями модели (':' / '#' / '@' / пробелы) трактуется так же — не
// собирать из недоверенного заголовка инъекционно оформленный субъект.
//
// ok=false → interceptor fail-closed (denied). Сужается именно анонимность:
// ЯВНО установленный bootstrap-принципал (`{system, bootstrap}`) остаётся
// личностью и штатно обрабатывается опцией AllowSystemPrincipal.
func defaultSubjectExtractor(ctx context.Context) (string, string, bool) {
	c, ok := CallerSubject(ctx)
	if !ok {
		return "", "", false
	}
	return c.Subject(), c.PrincipalID(), true
}

// isAnonymousSubject — helper. Returns true для всех принципалов
// эквивалентных anonymous (closed-list match):
//
//   - empty subject / empty principal_id
//   - principal_id == "anonymous" (api-gateway injectAnonymous case)
//   - subject == "system:anonymous"
//   - principal_id == "bootstrap" / subject == "system:bootstrap"
//     (PrincipalFromContext fallback когда ctx без Principal — api-gateway
//     не передал x-kacho-principal-* metadata headers).
//
// Используется в breakglass-path: даже когда authz-Check недоступен,
// anonymous request'ы должны быть denied.
//
// Для extractor'а по умолчанию первый arm (ok=false) срабатывает уже сам —
// defaultSubjectExtractor отбивает анонимность общим предикатом
// operations.Principal.IsAnonymous. Остальные arm'ы остаются живыми для
// ПОДМЕНЁННОГО InterceptorOptions.SubjectExtractor: breakglass обходит Check, и
// полагаться в нём на дисциплину чужой реализации нельзя.
func isAnonymousSubject(ctx context.Context, extract func(context.Context) (string, string, bool)) bool {
	subjectFGA, principalID, ok := extract(ctx)
	if !ok || principalID == "" || subjectFGA == "" {
		return true
	}
	if principalID == "anonymous" || principalID == "bootstrap" {
		return true
	}
	if subjectFGA == "system:anonymous" || subjectFGA == "system:bootstrap" {
		return true
	}
	return false
}
