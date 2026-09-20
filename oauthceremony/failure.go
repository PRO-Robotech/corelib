// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import "net/http"

// FailureCode — ЗНАЧЕНИЕ, по которому вызывающий различает случай отказа.
//
// # Почему не строка протокола
//
// На проводе OAuth 2.0 живёт короткий закрытый перечень (`invalid_request`,
// `invalid_grant`, …), и он НЕ РАЗЛИЧАЕТ то, что нам различать обязательно:
// «код уже погашен» и «клиент прислал мусор» оба уезжают наружу как
// `invalid_grant`. Если наш перечень совпадёт с проводным, вызывающему
// останется различать случаи по ТЕКСТУ подсказки — то есть по тому, что
// апстрим вправе переписать в любом выпуске.
//
// Поэтому FailureCode — СВОЙ перечень, а проводное представление вычисляется
// из него методом WireCode. Обратное отображение неоднозначно и не
// объявляется: из `server_error` нельзя вывести, была ли это поломка порта
// или нарушение его контракта.
//
// # Почему целое, а не строка
//
// Ноль обязан быть НЕ ЗНАЧЕНИЕМ: забытое присваивание обязано выглядеть как
// «код не назван», а не как один из настоящих случаев. У строкового перечня
// нулём была бы пустая строка, неотличимая от «сюда ещё не дописали».
type FailureCode uint16

// Случаи, различаемые по значению. Перечень ЗАКРЫТ: корзины «прочее» с
// человеческим пояснением здесь нет — есть CodeUnknown, и он означает ровно
// «движок вернул отказ, которого нет в таблице перевода», то есть ДЕФЕКТ
// таблицы, который обязан быть виден как таковой.
const (
	// CodeUnspecified — код не назван. Никогда не возвращается наружу;
	// его появление означает, что ошибка собрана в обход конструктора.
	CodeUnspecified FailureCode = iota

	// ── Отказы протокола, приходящие от движка ──────────────────────────

	CodeInvalidRequest
	CodeInvalidClient
	CodeInvalidGrant
	CodeUnauthorizedClient
	CodeUnsupportedGrantType
	CodeUnsupportedResponseType
	CodeUnsupportedResponseMode
	CodeInvalidScope
	CodeAccessDenied
	CodeServerError
	CodeTemporarilyUnavailable
	CodeInvalidState
	CodeInsufficientEntropy
	CodeMisconfiguration
	CodeNotFound
	CodeRequestUnauthorized
	CodeRequestForbidden
	CodeTokenExpired
	CodeTokenSignatureMismatch
	CodeInvalidTokenFormat
	CodeTokenClaim
	CodeScopeNotGranted
	CodeInactiveToken
	CodeLoginRequired
	CodeConsentRequired
	CodeInteractionRequired
	CodeRequestNotSupported
	CodeRequestURINotSupported
	CodeRegistrationNotSupported
	CodeInvalidRequestURI
	CodeInvalidRequestObject
	CodeAssertionReplayed
	CodeStorageConflict
	CodeAuthorizationCodeConsumed
	CodeUnhandledRequest

	// ── Отказы нашей границы, у движка не имеющие представления ─────────

	// CodeGrantNotFound — порт сказал «такой записи нет». Отдельно от
	// CodeInvalidGrant: «нет записи» и «запись есть, но не подходит» —
	// разные события для того, кто читает журнал.
	CodeGrantNotFound

	// CodePortContract — порт нарушил свой контракт: вернул неназванное
	// число затронутых строк либо число, невозможное при корректной
	// одноинструкционной записи. Это ДЕФЕКТ реализации порта, а не отказ
	// протокола, и он обязан быть отличим от CodeServerError.
	CodePortContract

	// CodePortDeadline — вызов порта не уложился в отведённый срок.
	CodePortDeadline

	// CodePortCanceled — вызов порта снят вызывающим.
	CodePortCanceled

	// CodeCeremonyMisuse — церемонию позвали не так: пустой предмет,
	// намерение не от этой церемонии, взаимоисключающие поля запроса.
	CodeCeremonyMisuse

	// CodeUnknown — движок вернул отказ, которого нет в таблице перевода.
	CodeUnknown

	// failureCodeCount — граница перечня. Не код.
	failureCodeCount
)

// failureMeta — по одной строке на каждый код. Полнота держится пробой
// TestFailureCodeTableIsTotal, а не внимательностью: код без строки печатался
// бы как `oauthceremony.FailureCode(37)` и молча терял бы и проводное
// представление, и состояние HTTP.
var failureMeta = [failureCodeCount]struct {
	name   string
	wire   string
	status int
}{
	CodeUnspecified:               {"unspecified", "server_error", http.StatusInternalServerError},
	CodeInvalidRequest:            {"invalid_request", "invalid_request", http.StatusBadRequest},
	CodeInvalidClient:             {"invalid_client", "invalid_client", http.StatusUnauthorized},
	CodeInvalidGrant:              {"invalid_grant", "invalid_grant", http.StatusBadRequest},
	CodeUnauthorizedClient:        {"unauthorized_client", "unauthorized_client", http.StatusBadRequest},
	CodeUnsupportedGrantType:      {"unsupported_grant_type", "unsupported_grant_type", http.StatusBadRequest},
	CodeUnsupportedResponseType:   {"unsupported_response_type", "unsupported_response_type", http.StatusBadRequest},
	CodeUnsupportedResponseMode:   {"unsupported_response_mode", "unsupported_response_mode", http.StatusBadRequest},
	CodeInvalidScope:              {"invalid_scope", "invalid_scope", http.StatusBadRequest},
	CodeAccessDenied:              {"access_denied", "access_denied", http.StatusForbidden},
	CodeServerError:               {"server_error", "server_error", http.StatusInternalServerError},
	CodeTemporarilyUnavailable:    {"temporarily_unavailable", "temporarily_unavailable", http.StatusServiceUnavailable},
	CodeInvalidState:              {"invalid_state", "invalid_state", http.StatusBadRequest},
	CodeInsufficientEntropy:       {"insufficient_entropy", "insufficient_entropy", http.StatusBadRequest},
	CodeMisconfiguration:          {"misconfiguration", "server_error", http.StatusInternalServerError},
	CodeNotFound:                  {"not_found", "not_found", http.StatusNotFound},
	CodeRequestUnauthorized:       {"request_unauthorized", "request_unauthorized", http.StatusUnauthorized},
	CodeRequestForbidden:          {"request_forbidden", "request_forbidden", http.StatusForbidden},
	CodeTokenExpired:              {"token_expired", "invalid_token", http.StatusUnauthorized},
	CodeTokenSignatureMismatch:    {"token_signature_mismatch", "token_signature_mismatch", http.StatusBadRequest},
	CodeInvalidTokenFormat:        {"invalid_token_format", "invalid_token", http.StatusBadRequest},
	CodeTokenClaim:                {"token_claim", "token_claim", http.StatusUnauthorized},
	CodeScopeNotGranted:           {"scope_not_granted", "scope_not_granted", http.StatusForbidden},
	CodeInactiveToken:             {"inactive_token", "token_inactive", http.StatusUnauthorized},
	CodeLoginRequired:             {"login_required", "login_required", http.StatusBadRequest},
	CodeConsentRequired:           {"consent_required", "consent_required", http.StatusBadRequest},
	CodeInteractionRequired:       {"interaction_required", "interaction_required", http.StatusBadRequest},
	CodeRequestNotSupported:       {"request_not_supported", "request_not_supported", http.StatusBadRequest},
	CodeRequestURINotSupported:    {"request_uri_not_supported", "request_uri_not_supported", http.StatusBadRequest},
	CodeRegistrationNotSupported:  {"registration_not_supported", "registration_not_supported", http.StatusBadRequest},
	CodeInvalidRequestURI:         {"invalid_request_uri", "invalid_request_uri", http.StatusBadRequest},
	CodeInvalidRequestObject:      {"invalid_request_object", "invalid_request_object", http.StatusBadRequest},
	CodeAssertionReplayed:         {"assertion_replayed", "invalid_client", http.StatusBadRequest},
	CodeStorageConflict:           {"storage_conflict", "server_error", http.StatusConflict},
	CodeAuthorizationCodeConsumed: {"authorization_code_consumed", "invalid_grant", http.StatusBadRequest},
	CodeUnhandledRequest:          {"unhandled_request", "invalid_request", http.StatusBadRequest},
	CodeGrantNotFound:             {"grant_not_found", "invalid_grant", http.StatusBadRequest},
	CodePortContract:              {"port_contract", "server_error", http.StatusInternalServerError},
	CodePortDeadline:              {"port_deadline", "temporarily_unavailable", http.StatusServiceUnavailable},
	CodePortCanceled:              {"port_canceled", "temporarily_unavailable", http.StatusServiceUnavailable},
	CodeCeremonyMisuse:            {"ceremony_misuse", "server_error", http.StatusInternalServerError},
	CodeUnknown:                   {"unknown", "server_error", http.StatusInternalServerError},
}

// String возвращает НАШЕ имя случая. Оно предназначено журналу и метке
// наблюдаемости, а не проводу.
func (c FailureCode) String() string {
	if !c.inTable() {
		return "oauthceremony.FailureCode(undeclared)"
	}
	return failureMeta[c].name
}

// WireCode возвращает значение поля `error` ответа по RFC 6749 §5.2.
//
// Отображение СЖИМАЮЩЕЕ и односторонне: несколько наших случаев уезжают в
// один проводной код намеренно — наружу не выдаётся то, по чему нападающий
// отличает «кода нет» от «код уже погашен».
func (c FailureCode) WireCode() string {
	if !c.inTable() {
		return "server_error"
	}
	return failureMeta[c].wire
}

// HTTPStatus возвращает состояние ответа, приличное этому случаю.
func (c FailureCode) HTTPStatus() int {
	if !c.inTable() {
		return http.StatusInternalServerError
	}
	return failureMeta[c].status
}

// Declared отвечает, назван ли случай. Нужен тому, кто получил FailureCode из
// сериализованного вида и обязан отвергнуть и чужое число, и CodeUnspecified:
// «код не назван» — не случай, а признак того, что значение собрано в обход
// конструктора.
func (c FailureCode) Declared() bool { return c > CodeUnspecified && c < failureCodeCount }

// inTable — есть ли у кода строка в failureMeta. Шире Declared ровно на
// CodeUnspecified: у него строка есть, чтобы печать не теряла смысл.
func (c FailureCode) inTable() bool { return c < failureCodeCount }
