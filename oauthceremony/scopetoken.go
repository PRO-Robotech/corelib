// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"strconv"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// ── Форма области ───────────────────────────────────────────────────────────

// scopeTokenDefect называет, чем область не `scope-token` (RFC 6749 §3.3);
// пусто — годна.
//
//	scope-token = 1*( %x21 / %x23-5B / %x5D-7E )
//
// # Почему судит церемония
//
// Область уезжает клиенту строкой через пробел (`scope` ответа точки
// авторизации и точки токена), и знак вне грамматики меняет прочитанное:
// пробел внутри одной области клиент читает двумя областями. Движок выданное
// не судит, а правило с образцом (`tenant.*`, ScopeMatchingWildcard) покрывает
// любой непустой хвост — пробел и табуляцию в том числе.
//
// Один судья на три входа, и правило у них одно: запрос авторизации (отказ
// клиенту CodeInvalidScope, requireScopeTokens), решение службы о выдаче
// (ErrCeremonyMisuse, checkGrantWithinRequest) и запись гранта из хранилища
// (нарушение контракта порта, requesterFromGrant). Отказ называет смещение и
// значение первого негодного байта: область с управляющим знаком, напечатанная
// как есть, была бы нечитаема.
func scopeTokenDefect(scope string) string {
	if scope == "" {
		return "it is empty, and a scope-token is at least one character"
	}
	for i := range len(scope) {
		if c := scope[i]; !isScopeTokenByte(c) {
			return "the byte at offset " + strconv.Itoa(i) + " is " + abnfByte(c) +
				", outside %x21 / %x23-5B / %x5D-7E"
		}
	}
	return ""
}

// isScopeTokenByte — байт входит в грамматику `scope-token`: печатный знак
// ASCII, кроме пробела, кавычки (%x22) и обратной косой черты (%x5C).
func isScopeTokenByte(c byte) bool {
	return c == 0x21 || (0x23 <= c && c <= 0x5B) || (0x5D <= c && c <= 0x7E)
}

// abnfByte записывает байт так, как его записывает грамматика: `%x20`.
func abnfByte(c byte) string {
	const digits = "0123456789ABCDEF"
	return "%x" + string([]byte{digits[c>>4], digits[c&0x0F]})
}

// requireScopeTokens отвергает запрос авторизации, запрошенная область которого
// не `scope-token`, случаем CodeInvalidScope (RFC 6749 §4.1.2.1: «запрошенная
// область негодна или искажена»).
//
// Судится запрос ДВИЖКА, уже разобранный: область, разделённую пробелом, он
// разделил на две, и каждую из них видит здесь отдельно. Судится после
// разбора ещё и затем, чтобы отказ был ДОСТАВИМ: Authorize возвращает
// намерение с проверенным адресом возврата, и DenyAuthorization собирает
// перенаправление. Отказ до разбора доставить было бы некуда. Подсказка и
// описание постоянны; область — только в Debug.
func requireScopeTokens(requester engine.AuthorizeRequester) error {
	if requester == nil {
		return nil
	}
	for _, scope := range requester.GetRequestedScopes() {
		if defect := scopeTokenDefect(scope); defect != "" {
			return failf(CodeInvalidScope, nil,
				"The authorization request carries a scope that is not a scope-token.",
				"Each scope is 1*( %x21 / %x23-5B / %x5D-7E ) and scopes are separated by one space (RFC 6749 §3.3).",
				"scope "+strconv.Quote(scope)+": "+defect)
		}
	}
	return nil
}
