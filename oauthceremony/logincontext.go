// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"strconv"
	"strings"
	"time"

	"github.com/PRO-Robotech/corelib/acrlevel"
)

// ── Контекст входа ──────────────────────────────────────────────────────────

// loginContextClaimNames — имена, под которыми контекст входа известен как
// утверждения токена: `sid` (OpenID Connect Front-Channel Logout 1.0 §3),
// `acr` и `auth_time` (OpenID Connect Core 1.0 §2, RFC 9470 §3). У каждого из
// этих значений есть поле записи — SessionID, ACR, AuthTime, — и ключ карты
// Claims с таким именем был бы вторым его местом: порт выпуска выбирал бы
// между ними сам. Порядок — порядок полей; по нему отказ называет первый
// найденный ключ, и текст не зависит от обхода карты.
var loginContextClaimNames = []string{"sid", "acr", "auth_time"}

// loginContextDefect называет первый изъян контекста входа — поле и почему;
// пусто — контекст годен.
//
// Один судья на оба входа: решение службы о выдаче (отказ ErrCeremonyMisuse у
// AuthorizationGrant) и запись сеанса из хранилища (нарушение контракта порта у
// SessionRecord). Поля у них одни и те же, и разные правила разошлись бы.
//
// Словарь уровня — ранжирование acrlevel, одна таблица на платформу: годен
// уровень, который она ставит выше анонима. Своего перечня здесь нет: второй
// перечень одного словаря разошёлся бы с первым молча. Грант выдаётся после
// входа, поэтому ни пустой уровень, ни "0" (аноним) уровнем входа не бывают.
func loginContextDefect(sessionID, acr string, authTime time.Time, claims map[string]any) (field, why string) {
	switch {
	case strings.TrimSpace(sessionID) == "":
		return "SessionID", "the login session is not named; the family of a grant ends with the session it was issued from"
	case acrlevel.Rank(acr) == 0:
		return "ACR", strconv.Quote(acr) + " is not a login level: the platform ranking (acrlevel) ranks it as anonymous, " +
			"and a grant follows a login"
	case authTime.IsZero():
		return "AuthTime", "the zero time is no moment of authentication"
	}
	for _, name := range loginContextClaimNames {
		if _, restated := claims[name]; restated {
			return "Claims", "carries the key " + strconv.Quote(name) +
				", which the login context holds as a field of its own; a value has one place"
		}
	}
	return "", ""
}
