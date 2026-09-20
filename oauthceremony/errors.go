// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"errors"
	"strings"
)

// ProtocolError — НАША форма отказа. Единственный тип ошибки, который
// покидает этот пакет.
//
// # Что значит «различать по значению»
//
// Вызывающий пишет `errors.Is(err, oauthceremony.ErrInvalidGrant)` и получает
// ответ, не читая ни одной строки текста. Сравнение идёт по полю Code через
// метод Is (см. ниже), поэтому совпадает и с образцом-часовым, у которого нет
// ни описания, ни подсказки, и с настоящим отказом, у которого они есть.
//
// Обратное тоже обязано выполняться: два РАЗНЫХ случая никогда не совпадают,
// как бы ни совпали их тексты. Это держится пробой TestErrorsAreDistinctByValue.
//
// # Почему указатель
//
// Часовой обязан быть сравним по указателю тоже (`err == ErrInvalidGrant`
// иногда пишут по привычке), и обязан быть неизменяемым для вызывающего.
// Значение-структура дало бы копию, которую вызывающий мог бы дополнить и
// передать дальше как наш часовой.
type ProtocolError struct {
	// Code — случай. Единственное поле, по которому дозволено ветвиться.
	Code FailureCode

	// Description — описание случая для человека. Годится проводу.
	Description string

	// Hint — подсказка, что делать. Годится проводу.
	Hint string

	// Debug — внутренние подробности: текст отказа хранилища, имя порта,
	// имя обработчика. НЕ ГОДИТСЯ ПРОВОДУ никогда: здесь оседают строки
	// движка и строки подключения к базе. Годится журналу.
	Debug string

	// cause — исходный отказ. Не экспортируется намеренно: наружу
	// пробрасывать чужое значение нельзя, а errors.Is по стандартным
	// часовым (context.DeadlineExceeded) обязан продолжать работать —
	// его обслуживает Unwrap.
	cause error
}

// Error печатает имя случая и описание. Текст НЕ является контрактом и не
// годится для ветвления — для этого есть Code.
func (e *ProtocolError) Error() string {
	var b strings.Builder
	b.WriteString("oauthceremony: ")
	b.WriteString(e.Code.String())
	if e.Description != "" {
		b.WriteString(": ")
		b.WriteString(e.Description)
	}
	if e.Hint != "" {
		b.WriteString(" (")
		b.WriteString(e.Hint)
		b.WriteString(")")
	}
	return b.String()
}

// Is сравнивает по ЗНАЧЕНИЮ случая, а не по указателю и не по тексту.
func (e *ProtocolError) Is(target error) bool {
	var t *ProtocolError
	if !errors.As(target, &t) {
		return false
	}
	return t.Code == e.Code
}

// Unwrap отдаёт исходный отказ, чтобы errors.Is по часовым стандартной
// библиотеки (context.DeadlineExceeded, context.Canceled) продолжал работать
// сквозь нашу оболочку. Возвращаемый тип — интерфейс error, поэтому чужой ТИП
// наружу не проходит: вызывающий не может его назвать.
func (e *ProtocolError) Unwrap() error { return e.cause }

// HTTPStatus — состояние ответа, приличное этому случаю.
func (e *ProtocolError) HTTPStatus() int { return e.Code.HTTPStatus() }

// WireCode — значение поля `error` ответа по RFC 6749 §5.2.
func (e *ProtocolError) WireCode() string { return e.Code.WireCode() }

// CodeOf достаёт случай из любой ошибки.
//
// Для ошибки не из этого пакета отвечает CodeUnspecified — «код не назван», а
// НЕ CodeUnknown: CodeUnknown означает провал нашей таблицы перевода, и
// смешивать его с «эту ошибку сделали не мы» нельзя, иначе дефект таблицы
// перестанет быть видимым.
func CodeOf(err error) FailureCode {
	var pe *ProtocolError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return CodeUnspecified
}

// sentinel собирает образец-часовой: только код, без описания.
func sentinel(code FailureCode) *ProtocolError { return &ProtocolError{Code: code} }

// Часовые. По одному на КАЖДЫЙ объявленный случай — перечень полон, и его
// полнота держится пробой TestEverySentinelHasACode, а не внимательностью.
//
// Часовой ИММУТАБЕЛЕН по уговору: его поля не меняются никем и никогда.
// Обогащённый отказ строится конструктором, а не правкой часового.
var (
	ErrInvalidRequest            = sentinel(CodeInvalidRequest)
	ErrInvalidClient             = sentinel(CodeInvalidClient)
	ErrInvalidGrant              = sentinel(CodeInvalidGrant)
	ErrUnauthorizedClient        = sentinel(CodeUnauthorizedClient)
	ErrUnsupportedGrantType      = sentinel(CodeUnsupportedGrantType)
	ErrUnsupportedResponseType   = sentinel(CodeUnsupportedResponseType)
	ErrUnsupportedResponseMode   = sentinel(CodeUnsupportedResponseMode)
	ErrInvalidScope              = sentinel(CodeInvalidScope)
	ErrAccessDenied              = sentinel(CodeAccessDenied)
	ErrServerError               = sentinel(CodeServerError)
	ErrTemporarilyUnavailable    = sentinel(CodeTemporarilyUnavailable)
	ErrInvalidState              = sentinel(CodeInvalidState)
	ErrInsufficientEntropy       = sentinel(CodeInsufficientEntropy)
	ErrMisconfiguration          = sentinel(CodeMisconfiguration)
	ErrNotFound                  = sentinel(CodeNotFound)
	ErrRequestUnauthorized       = sentinel(CodeRequestUnauthorized)
	ErrRequestForbidden          = sentinel(CodeRequestForbidden)
	ErrTokenExpired              = sentinel(CodeTokenExpired)
	ErrTokenSignatureMismatch    = sentinel(CodeTokenSignatureMismatch)
	ErrInvalidTokenFormat        = sentinel(CodeInvalidTokenFormat)
	ErrTokenClaim                = sentinel(CodeTokenClaim)
	ErrScopeNotGranted           = sentinel(CodeScopeNotGranted)
	ErrInactiveToken             = sentinel(CodeInactiveToken)
	ErrLoginRequired             = sentinel(CodeLoginRequired)
	ErrConsentRequired           = sentinel(CodeConsentRequired)
	ErrInteractionRequired       = sentinel(CodeInteractionRequired)
	ErrRequestNotSupported       = sentinel(CodeRequestNotSupported)
	ErrRequestURINotSupported    = sentinel(CodeRequestURINotSupported)
	ErrRegistrationNotSupported  = sentinel(CodeRegistrationNotSupported)
	ErrInvalidRequestURI         = sentinel(CodeInvalidRequestURI)
	ErrInvalidRequestObject      = sentinel(CodeInvalidRequestObject)
	ErrAssertionReplayed         = sentinel(CodeAssertionReplayed)
	ErrStorageConflict           = sentinel(CodeStorageConflict)
	ErrAuthorizationCodeConsumed = sentinel(CodeAuthorizationCodeConsumed)
	ErrUnhandledRequest          = sentinel(CodeUnhandledRequest)
	ErrGrantNotFound             = sentinel(CodeGrantNotFound)
	ErrPortContract              = sentinel(CodePortContract)
	ErrPortDeadline              = sentinel(CodePortDeadline)
	ErrPortCanceled              = sentinel(CodePortCanceled)
	ErrCeremonyMisuse            = sentinel(CodeCeremonyMisuse)
	ErrUnknown                   = sentinel(CodeUnknown)
)

// allSentinels — перечень часовых для пробы полноты. Не экспортируется:
// снаружи он был бы приглашением перебирать случаи вместо ветвления по коду.
var allSentinels = []*ProtocolError{
	ErrInvalidRequest, ErrInvalidClient, ErrInvalidGrant, ErrUnauthorizedClient,
	ErrUnsupportedGrantType, ErrUnsupportedResponseType, ErrUnsupportedResponseMode,
	ErrInvalidScope, ErrAccessDenied, ErrServerError, ErrTemporarilyUnavailable,
	ErrInvalidState, ErrInsufficientEntropy, ErrMisconfiguration, ErrNotFound,
	ErrRequestUnauthorized, ErrRequestForbidden, ErrTokenExpired,
	ErrTokenSignatureMismatch, ErrInvalidTokenFormat, ErrTokenClaim,
	ErrScopeNotGranted, ErrInactiveToken, ErrLoginRequired, ErrConsentRequired,
	ErrInteractionRequired, ErrRequestNotSupported, ErrRequestURINotSupported,
	ErrRegistrationNotSupported, ErrInvalidRequestURI, ErrInvalidRequestObject,
	ErrAssertionReplayed, ErrStorageConflict, ErrAuthorizationCodeConsumed,
	ErrUnhandledRequest, ErrGrantNotFound, ErrPortContract, ErrPortDeadline,
	ErrPortCanceled, ErrCeremonyMisuse, ErrUnknown,
}

// failf собирает наш отказ с контекстом.
func failf(code FailureCode, cause error, description, hint, debug string) *ProtocolError {
	return &ProtocolError{
		Code:        code,
		Description: description,
		Hint:        hint,
		Debug:       debug,
		cause:       cause,
	}
}

// misuse — отказ «церемонию позвали не так». Отдельный конструктор, чтобы
// такой отказ нельзя было перепутать с отказом протокола в месте сборки.
func misuse(description string) *ProtocolError {
	return failf(CodeCeremonyMisuse, nil, description, "", "")
}
