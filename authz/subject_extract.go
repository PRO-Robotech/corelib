// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"

	"github.com/PRO-Robotech/corelib/operations"
)

// defaultSubjectExtractor — стандартная реализация на основе
// operations.PrincipalFromContextOK.
//
// Возвращает:
//   - subjectFGA — "user:usr_xxx" или "service_account:sva_xxx"
//   - principalID — raw ID (для rate-limit-bucket'а)
//   - ok — false, если ctx не назвал НИКОГО (см. ниже), либо id несёт
//     FGA-разделители
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
// Поэтому здесь стоит БЕЗУСЛОВНЫЙ отказ ДО любого вопроса модели, и признак
// анонимности берётся из ОДНОГО общего предиката operations.Principal.IsAnonymous
// (пустая пара ИЛИ зарезервированное слово operations.AnonymousPrincipalID в
// любом заявленном типе). Тип не может быть признаком: его назначает себе тот,
// кто прислал заголовки личности. Единый предикат — чтобы производитель метки и
// её распознаватель не разъехались.
//
// ok=false → interceptor fail-closed (denied). Сужается именно анонимность:
// ЯВНО установленный bootstrap-принципал (`{system, bootstrap}`) остаётся
// личностью и штатно обрабатывается опцией AllowSystemPrincipal.
func defaultSubjectExtractor(ctx context.Context) (string, string, bool) {
	p, ok := operations.PrincipalFromContextOK(ctx)
	if !ok || p.IsAnonymous() {
		return "", "", false
	}
	// principal id приходит из недоверенного x-kacho-principal-id header'а. Если
	// он несёт FGA-разделители (':' / '#' / '@' / whitespace) — трактуем как
	// anonymous (ok=false → interceptor fail-closed deny), а НЕ собираем из него
	// инъекционно-оформленный subject. Симметрично FormatObject-валидации объекта.
	if !validSubjectID(p.ID) {
		return "", "", false
	}
	return FormatSubject(p.Type, p.ID), p.ID, true
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
