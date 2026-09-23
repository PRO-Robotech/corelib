// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// translate_internal_test.go — пробы ТАБЛИЦЫ ПЕРЕВОДА.
//
// Файл внутренний намеренно: таблица не экспортируется и не должна, а
// проверять её снаружи можно было бы только через наблюдаемое поведение —
// то есть не проверить столкновение двух записей, которое проявляется лишь
// на редком отказе.
package oauthceremony

import (
	"errors"
	"net/http"
	"testing"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// TestEngineFailureTableHasNoCollisions — ни одна запись не затирает другую.
//
// Движок различает свои отказы парой (поле `error`, состояние HTTP). Совпади
// пара у двух записей — вторая молча вытеснила бы первую, и целая ветвь
// перевода перестала бы существовать, не сломав ни сборки, ни проб.
func TestEngineFailureTableHasNoCollisions(t *testing.T) {
	if len(engineToFailure) != len(engineFailurePairs) {
		t.Fatalf("в таблице %d записей на %d пар — ключи столкнулись",
			len(engineToFailure), len(engineFailurePairs))
	}

	seenCodes := map[FailureCode]string{}
	for _, pair := range engineFailurePairs {
		if previous, taken := seenCodes[pair.code]; taken {
			t.Errorf("случай %s назначен и %q, и %q — два отказа движка стали одним",
				pair.code, previous, pair.engineErr.ErrorField)
		}
		seenCodes[pair.code] = pair.engineErr.ErrorField
		if !pair.code.Declared() {
			t.Errorf("отказ движка %q переведён в необъявленный случай", pair.engineErr.ErrorField)
		}
	}
	t.Logf("пар перевода: %d шт; различных случаев: %d шт", len(engineFailurePairs), len(seenCodes))
}

// TestEveryDeclaredCodeTranslatesBackToTheEngine — обратный перевод полон.
//
// Случай без обратной записи уехал бы клиенту как «внутренняя ошибка» —
// молча, через запасную ветвь toEngine. Запасная ветвь обязана существовать,
// но не обязана срабатывать.
func TestEveryDeclaredCodeTranslatesBackToTheEngine(t *testing.T) {
	var orphans []string
	for code := FailureCode(1); code.Declared(); code++ {
		if _, known := failureToEngine[code]; !known {
			orphans = append(orphans, code.String())
		}
	}
	if len(orphans) != 0 {
		t.Fatalf("случаи без обратного перевода (%d шт): %v", len(orphans), orphans)
	}
	t.Logf("записей обратного перевода: %d шт", len(failureToEngine))
}

// TestUnknownEngineFailureBecomesCodeUnknown — отказ движка, которого нет в
// таблице, называется CodeUnknown, а не подменяется ближайшим.
//
// CodeUnknown означает ровно «таблица неполна» и обязан быть виден как дефект
// таблицы, а не раствориться в CodeServerError.
func TestUnknownEngineFailureBecomesCodeUnknown(t *testing.T) {
	stranger := &engine.RFC6749Error{
		ErrorField:       "an_error_this_package_has_never_seen",
		DescriptionField: "invented for this probe",
		CodeField:        http.StatusTeapot,
	}

	translated := fromEngine(stranger)
	if CodeOf(translated) != CodeUnknown {
		t.Fatalf("случай %v, ожидался %v", CodeOf(translated), CodeUnknown)
	}

	var ours *ProtocolError
	if !errors.As(translated, &ours) {
		t.Fatal("перевод не дал нашего ProtocolError")
	}
	if ours.Debug == "" {
		t.Error("непонятный отказ приехал без подробностей в Debug — искать будет нечего")
	}
}

// TestKnownEngineFailureKeepsItsCase — близнец предыдущей пробы,
// отличающийся ровно одним фактом: отказ в таблице есть.
//
// Без него прибор был бы зелёным и у перевода, называющего CodeUnknown всё
// подряд.
func TestKnownEngineFailureKeepsItsCase(t *testing.T) {
	translated := fromEngine(engine.ErrInvalidGrant)
	if CodeOf(translated) != CodeInvalidGrant {
		t.Fatalf("случай %v, ожидался %v", CodeOf(translated), CodeInvalidGrant)
	}
}

// TestOurFailurePassesThroughTheEngineUnchanged — наш отказ, отданный прямо,
// не переводится второй раз.
func TestOurFailurePassesThroughTheEngineUnchanged(t *testing.T) {
	ours := failf(CodePortContract, nil, "probe", "", "probe")
	if translated := fromEngine(ours); translated != error(ours) {
		t.Fatalf("наш отказ подменён при переводе: %v", translated)
	}
}

// TestSentinelsAreImmutable — часовой не несёт ни описания, ни подсказки, ни
// подробностей.
//
// Часовой с текстом провоцировал бы отдать его наружу как готовый отказ, и
// текст одного случая стал бы текстом всех его появлений.
func TestSentinelsAreImmutable(t *testing.T) {
	for _, s := range allSentinels {
		if s.Description != "" || s.Hint != "" || s.Debug != "" || s.cause != nil {
			t.Errorf("часовой %s несёт содержимое: описание=%q подсказка=%q подробности=%q",
				s.Code, s.Description, s.Hint, s.Debug)
		}
	}
	if len(allSentinels) == 0 {
		t.Fatal("перечень часовых пуст")
	}
}
