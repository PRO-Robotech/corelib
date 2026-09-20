// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// errors_test.go — пробы перевода отказов.
//
// Проверяется РАЗЛИЧИМОСТЬ ПО ЗНАЧЕНИЮ: вызывающий обязан уметь отличить один
// случай от другого, не читая ни одного текста, и обязан НЕ путать разные
// случаи, как бы ни совпали их описания.
package oauthceremony_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// sentinelRoster — часовой на каждый объявленный случай.
//
// Перечень ведётся руками: он и есть утверждение «эти случаи различимы
// снаружи». Полноту держит TestEveryDeclaredCodeHasASentinel.
func sentinelRoster() map[oauthceremony.FailureCode]*oauthceremony.ProtocolError {
	return map[oauthceremony.FailureCode]*oauthceremony.ProtocolError{
		oauthceremony.CodeInvalidRequest:            oauthceremony.ErrInvalidRequest,
		oauthceremony.CodeInvalidClient:             oauthceremony.ErrInvalidClient,
		oauthceremony.CodeInvalidGrant:              oauthceremony.ErrInvalidGrant,
		oauthceremony.CodeUnauthorizedClient:        oauthceremony.ErrUnauthorizedClient,
		oauthceremony.CodeUnsupportedGrantType:      oauthceremony.ErrUnsupportedGrantType,
		oauthceremony.CodeUnsupportedResponseType:   oauthceremony.ErrUnsupportedResponseType,
		oauthceremony.CodeUnsupportedResponseMode:   oauthceremony.ErrUnsupportedResponseMode,
		oauthceremony.CodeInvalidScope:              oauthceremony.ErrInvalidScope,
		oauthceremony.CodeAccessDenied:              oauthceremony.ErrAccessDenied,
		oauthceremony.CodeServerError:               oauthceremony.ErrServerError,
		oauthceremony.CodeTemporarilyUnavailable:    oauthceremony.ErrTemporarilyUnavailable,
		oauthceremony.CodeInvalidState:              oauthceremony.ErrInvalidState,
		oauthceremony.CodeInsufficientEntropy:       oauthceremony.ErrInsufficientEntropy,
		oauthceremony.CodeMisconfiguration:          oauthceremony.ErrMisconfiguration,
		oauthceremony.CodeNotFound:                  oauthceremony.ErrNotFound,
		oauthceremony.CodeRequestUnauthorized:       oauthceremony.ErrRequestUnauthorized,
		oauthceremony.CodeRequestForbidden:          oauthceremony.ErrRequestForbidden,
		oauthceremony.CodeTokenExpired:              oauthceremony.ErrTokenExpired,
		oauthceremony.CodeTokenSignatureMismatch:    oauthceremony.ErrTokenSignatureMismatch,
		oauthceremony.CodeInvalidTokenFormat:        oauthceremony.ErrInvalidTokenFormat,
		oauthceremony.CodeTokenClaim:                oauthceremony.ErrTokenClaim,
		oauthceremony.CodeScopeNotGranted:           oauthceremony.ErrScopeNotGranted,
		oauthceremony.CodeInactiveToken:             oauthceremony.ErrInactiveToken,
		oauthceremony.CodeLoginRequired:             oauthceremony.ErrLoginRequired,
		oauthceremony.CodeConsentRequired:           oauthceremony.ErrConsentRequired,
		oauthceremony.CodeInteractionRequired:       oauthceremony.ErrInteractionRequired,
		oauthceremony.CodeRequestNotSupported:       oauthceremony.ErrRequestNotSupported,
		oauthceremony.CodeRequestURINotSupported:    oauthceremony.ErrRequestURINotSupported,
		oauthceremony.CodeRegistrationNotSupported:  oauthceremony.ErrRegistrationNotSupported,
		oauthceremony.CodeInvalidRequestURI:         oauthceremony.ErrInvalidRequestURI,
		oauthceremony.CodeInvalidRequestObject:      oauthceremony.ErrInvalidRequestObject,
		oauthceremony.CodeAssertionReplayed:         oauthceremony.ErrAssertionReplayed,
		oauthceremony.CodeStorageConflict:           oauthceremony.ErrStorageConflict,
		oauthceremony.CodeAuthorizationCodeConsumed: oauthceremony.ErrAuthorizationCodeConsumed,
		oauthceremony.CodeUnhandledRequest:          oauthceremony.ErrUnhandledRequest,
		oauthceremony.CodeGrantNotFound:             oauthceremony.ErrGrantNotFound,
		oauthceremony.CodePortContract:              oauthceremony.ErrPortContract,
		oauthceremony.CodePortDeadline:              oauthceremony.ErrPortDeadline,
		oauthceremony.CodePortCanceled:              oauthceremony.ErrPortCanceled,
		oauthceremony.CodeCeremonyMisuse:            oauthceremony.ErrCeremonyMisuse,
		oauthceremony.CodeUnknown:                   oauthceremony.ErrUnknown,
	}
}

// declaredCodes перечисляет объявленные случаи, опираясь только на
// экспортированный предикат. Перечень кончается там, где Declared даёт ложь.
func declaredCodes() []oauthceremony.FailureCode {
	var out []oauthceremony.FailureCode
	for code := oauthceremony.FailureCode(1); code.Declared(); code++ {
		out = append(out, code)
	}
	return out
}

// TestEveryDeclaredCodeHasASentinel — у каждого случая есть часовой.
//
// Случай без часового снаружи неразличим: вызывающему нечего подставить в
// errors.Is, и ему остаётся сравнивать текст.
func TestEveryDeclaredCodeHasASentinel(t *testing.T) {
	roster := sentinelRoster()
	codes := declaredCodes()

	if len(codes) == 0 {
		t.Fatal("перечень случаев пуст — предикат Declared сломан")
	}

	var orphans []string
	for _, code := range codes {
		if _, named := roster[code]; !named {
			orphans = append(orphans, code.String())
		}
	}
	sort.Strings(orphans)
	if len(orphans) != 0 {
		t.Errorf("случаи без часового (%d шт): %s", len(orphans), strings.Join(orphans, ", "))
	}
	if len(roster) != len(codes) {
		t.Errorf("часовых %d шт, случаев %d шт — перечни разошлись", len(roster), len(codes))
	}
	t.Logf("объявленных случаев: %d шт; часовых: %d шт", len(codes), len(roster))
}

// TestEveryDeclaredCodeIsPrintableAndMappable — таблица случаев полна.
//
// Случай без имени печатался бы числом, а случай без проводного кода уезжал
// бы наружу пустым полем `error` — и то и другое молча.
func TestEveryDeclaredCodeIsPrintableAndMappable(t *testing.T) {
	for _, code := range declaredCodes() {
		name := code.String()
		switch {
		case name == "", strings.Contains(name, "undeclared"):
			t.Errorf("случай %d не имеет имени: %q", uint16(code), name)
		}
		if wire := code.WireCode(); wire == "" {
			t.Errorf("случай %s не имеет проводного кода", name)
		}
		if status := code.HTTPStatus(); status < 400 || status > 599 {
			t.Errorf("случай %s отдаёт состояние %d — не отказ", name, status)
		}
	}
}

// TestCodesAreDistinctByValue — разные случаи не совпадают, одинаковые
// совпадают. Обе половины обязательны: прибор, проверяющий только первую,
// зелёный и у Is, который всегда лжёт «нет».
func TestCodesAreDistinctByValue(t *testing.T) {
	roster := sentinelRoster()
	for leftCode, left := range roster {
		if !errors.Is(left, roster[leftCode]) {
			t.Errorf("часовой %s не совпал сам с собой", leftCode)
		}
		for rightCode, right := range roster {
			if leftCode == rightCode {
				continue
			}
			if errors.Is(left, right) {
				t.Errorf("часовые %s и %s совпали — случаи неразличимы", leftCode, rightCode)
			}
		}
	}
}

// TestEnrichedErrorStillMatchesItsSentinel — отказ, обогащённый описанием и
// подсказкой, продолжает совпадать с часовым.
//
// Это и есть «сравнение по значению»: совпадение держится полем Code, а не
// указателем и не текстом.
func TestEnrichedErrorStillMatchesItsSentinel(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	request := authorizeRequest()
	request.Scopes = []string{"openid", "scope-the-client-was-never-granted"}

	_, err := ceremony.Authorize(context.Background(), request)
	if err == nil {
		t.Fatal("запрос с незарегистрированной областью прошёл")
	}
	if !errors.Is(err, oauthceremony.ErrInvalidScope) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodeInvalidScope)
	}

	var protocolErr *oauthceremony.ProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatal("отказ не оказался нашим ProtocolError")
	}
	if protocolErr.Description == "" {
		t.Error("обогащённый отказ приехал без описания")
	}
	if protocolErr.WireCode() != "invalid_scope" {
		t.Errorf("проводной код %q, ожидался \"invalid_scope\"", protocolErr.WireCode())
	}
	if errors.Is(err, oauthceremony.ErrInvalidGrant) {
		t.Error("обогащённый отказ совпал с посторонним часовым")
	}
}

// TestCodeOfAForeignErrorIsUnspecified — чужая ошибка не притворяется нашей.
//
// Отвечать CodeUnknown было бы вредно: CodeUnknown означает провал НАШЕЙ
// таблицы перевода, и смешение скрыло бы её дефект.
func TestCodeOfAForeignErrorIsUnspecified(t *testing.T) {
	cases := map[string]error{
		"ошибка стандартной библиотеки": errors.New("some other failure"),
		"обёрнутая чужая":               fmt.Errorf("outer: %w", context.Canceled),
		"пустая":                        nil,
	}
	for name, err := range cases {
		if got := oauthceremony.CodeOf(err); got != oauthceremony.CodeUnspecified {
			t.Errorf("%s: CodeOf дал %v, ожидался CodeUnspecified", name, got)
		}
	}
}

// TestUndeclaredCodeValueIsRejected — число вне перечня случаем не является.
func TestUndeclaredCodeValueIsRejected(t *testing.T) {
	declared := declaredCodes()
	beyond := declared[len(declared)-1] + 1

	if beyond.Declared() {
		t.Fatalf("число за концом перечня (%d) названо случаем", uint16(beyond))
	}
	if oauthceremony.CodeUnspecified.Declared() {
		t.Error("CodeUnspecified назван случаем — «код не назван» случаем не является")
	}
	if got := beyond.WireCode(); got != "server_error" {
		t.Errorf("непонятное число уезжает наружу как %q, ожидалось \"server_error\"", got)
	}
}
