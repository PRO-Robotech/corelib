// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package option_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/PRO-Robotech/corelib/option"
)

// value_test.go — «не задано» отличимо от «задано нулевым».
//
// Ради этого различия тип и существует: у имени ресурса пустая строка есть
// ЗАКОННОЕ значение (`api-conventions.md` §«Имя ресурса»), поэтому «имя не
// прислали» нулевым значением не выражается.

func TestUnsetIsNotTheZeroValue(t *testing.T) {
	var unset option.ValueOf[string]
	if !unset.IsNone() {
		t.Fatal("нулевое значение обязано означать «не задано»")
	}
	if v, ok := unset.Maybe(); ok || v != "" {
		t.Fatalf("Maybe() на незаданном = (%q, %v)", v, ok)
	}

	// Задано ПУСТОЙ строкой — это «задано», а не «не задано».
	empty := option.MustNewOption("")
	if empty.IsNone() {
		t.Fatal("заданное пустой строкой прочитано как незаданное — различие потеряно")
	}
	v, ok := empty.Maybe()
	if !ok || v != "" {
		t.Fatalf("Maybe() на заданном пустом = (%q, %v)", v, ok)
	}
}

func TestSetAndUnsetMoveBothWays(t *testing.T) {
	var v option.ValueOf[int]
	v.Set(42)
	if got, ok := v.Maybe(); !ok || got != 42 {
		t.Fatalf("после Set: (%v, %v)", got, ok)
	}
	v.Unset()
	if !v.IsNone() {
		t.Fatal("после Unset значение осталось заданным")
	}
	if got := v.SomeOr(7); got != 7 {
		t.Fatalf("SomeOr на незаданном = %v, ожидалось 7", got)
	}
	v.Set(1)
	if got := v.SomeOr(7); got != 1 {
		t.Fatalf("SomeOr на заданном = %v, ожидалось 1", got)
	}
}

// validatable — внутренний тип, умеющий проверять себя сам.
type validatable string

var errBad = errors.New("значение негодно")

func (v validatable) Validate() error {
	if v == "bad" {
		return errBad
	}
	return nil
}

// Проверка ДЕЛЕГИРУЕТСЯ внутреннему типу, и только когда значение задано.
// Незаданное годно by construction: проверять нечего.
func TestValidateDelegatesOnlyWhenSet(t *testing.T) {
	var unset option.ValueOf[validatable]
	if err := unset.Validate(); err != nil {
		t.Fatalf("незаданное признано негодным: %v", err)
	}
	if err := option.MustNewOption(validatable("ok")).Validate(); err != nil {
		t.Fatalf("годное признано негодным: %v", err)
	}
	if err := option.MustNewOption(validatable("bad")).Validate(); !errors.Is(err, errBad) {
		t.Fatalf("негодное пропущено: %v", err)
	}
}

// Внутренний тип без собственной проверки — не находка: делегировать некому.
func TestValidateIsSilentWhenTheInnerTypeCannotValidateItself(t *testing.T) {
	if err := option.MustNewOption(42).Validate(); err != nil {
		t.Fatalf("тип без Validate признан негодным: %v", err)
	}
}

func TestIsEqComparesBothSidesAndTheSetFlag(t *testing.T) {
	eq := func(a, b int) bool { return a == b }
	set1, set2 := option.MustNewOption(1), option.MustNewOption(2)
	var none1, none2 option.ValueOf[int]

	if !none1.IsEq(none2, eq) {
		t.Fatal("два незаданных признаны разными")
	}
	if !set1.IsEq(option.MustNewOption(1), eq) {
		t.Fatal("два одинаковых заданных признаны разными")
	}
	if set1.IsEq(set2, eq) {
		t.Fatal("разные заданные признаны равными")
	}
	// Несовпадение самого признака заданности — тоже неравенство, и это
	// главное: иначе «не задано» слилось бы с «задано нулевым».
	if set1.IsEq(none1, eq) || none1.IsEq(set1, eq) {
		t.Fatal("заданное признано равным незаданному")
	}
}

func TestJSONCarriesTheAbsenceAsNull(t *testing.T) {
	var unset option.ValueOf[string]
	raw, err := json.Marshal(unset)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "null" {
		t.Fatalf("незаданное закодировано как %s, ожидалось null", raw)
	}

	raw, err = json.Marshal(option.MustNewOption("треска"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `"треска"` {
		t.Fatalf("заданное закодировано как %s", raw)
	}

	var back option.ValueOf[string]
	if err := json.Unmarshal([]byte("null"), &back); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if !back.IsNone() {
		t.Fatal("null разобран как заданное")
	}
	if err := json.Unmarshal([]byte(`"треска"`), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := back.Maybe(); !ok || v != "треска" {
		t.Fatalf("после разбора: (%q, %v)", v, ok)
	}
}

func TestStringNamesTheAbsence(t *testing.T) {
	var unset option.ValueOf[int]
	if got := unset.String(); got != "none" {
		t.Fatalf("String() незаданного = %q", got)
	}
	if got := option.MustNewOption(42).String(); got != "42" {
		t.Fatalf("String() заданного = %q", got)
	}
}
