// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicecontract_test

import (
	"context"
	"testing"

	"github.com/PRO-Robotech/corelib/servicecontract"
)

// stubProbe — минимальный порт существования объекта. Он ничего не решает: его
// присутствие судится осью скрытия, а не его ответами.
type stubProbe struct{}

func (stubProbe) ObjectExists(context.Context, string, string) (bool, error) { return false, nil }

// ProbeableTypes — охват заглушки. Здесь он законно непустой и произвольный:
// предмет этих проб — что судит КОНСТРУКТОР дескриптора (порт принесён при
// объявленном скрытии), а охват судит носитель на старте, и своей пробой
// (`servicehost`, О5в).
func (stubProbe) ProbeableTypes() []string { return []string{"demo_widget"} }

// stubCheck — решатель владельца модели. Как и порт выше, он ничего не решает:
// его присутствие судится источником решения, а не его ответами.
type stubCheck struct{}

func (stubCheck) Check(context.Context, string, string, string) (bool, error) { return false, nil }

// TestAxisTellsValueFromNotApplicable — ось обязана РАЗЛИЧАТЬ три состояния, а
// не два: «значение», «не применимо, потому что …» и «не сказано ничего».
// Пока их два, необъяснённая пустая клетка неотличима от объяснённой.
func TestAxisTellsValueFromNotApplicable(t *testing.T) {
	var undeclared servicecontract.Axis[[]string]
	if _, ok := undeclared.Get(); ok {
		t.Fatal("необъявленная ось отдала значение")
	}
	if _, ok := undeclared.NotApplicableBecause(); ok {
		t.Fatal("необъявленная ось объявила себя неприменимой")
	}
	if undeclared.Declared() {
		t.Fatal("необъявленная ось объявила себя объявленной")
	}

	valued := servicecontract.Value([]string{"a"})
	v, ok := valued.Get()
	if !ok || len(v) != 1 || v[0] != "a" {
		t.Fatalf("ось со значением отдала %v, ok=%v", v, ok)
	}
	if _, ok := valued.NotApplicableBecause(); ok {
		t.Fatal("ось со значением объявила себя неприменимой")
	}
	if !valued.Declared() {
		t.Fatal("ось со значением не объявлена")
	}

	na := servicecontract.NotApplicable[[]string]("предмета нет by construction")
	if _, ok := na.Get(); ok {
		t.Fatal("неприменимая ось отдала значение")
	}
	because, ok := na.NotApplicableBecause()
	if !ok || because != "предмета нет by construction" {
		t.Fatalf("причина потеряна: %q ok=%v", because, ok)
	}
	if !na.Declared() {
		t.Fatal("неприменимая ось не объявлена")
	}
}

// TestNotApplicableWithoutReasonIsNotDeclared — «не применимо» без причины
// объявлением не является. Это и есть механизм: пустая строка не закрывает ось,
// поэтому объявить «не применимо» дешевле, чем промолчать, нельзя.
func TestNotApplicableWithoutReasonIsNotDeclared(t *testing.T) {
	a := servicecontract.NotApplicable[[]string]("")
	if a.Declared() {
		t.Fatal("«не применимо» с пустой причиной сочтено объявлением")
	}
}

// TestValueOfEmptySliceIsStillADeclaration — пустое ЗНАЧЕНИЕ и НЕОБЪЯВЛЕННОСТЬ
// — разные вещи. Сервис вправе сказать «эмичу пустой набор» явно, и это не то
// же самое, что промолчать.
func TestValueOfEmptySliceIsStillADeclaration(t *testing.T) {
	a := servicecontract.Value([]string{})
	if !a.Declared() {
		t.Fatal("явно объявленное пустое значение сочтено умолчанием")
	}
	v, ok := a.Get()
	if !ok || v == nil || len(v) != 0 {
		t.Fatalf("пустое значение потеряно: %v ok=%v", v, ok)
	}
}

// TestParseMode — режим приходит строкой из окружения и обязан быть разобран
// ОДНИМ местом: пока каждый сервис разбирал его сам, «production» и
// «production-strict» различались у семерых семью способами.
func TestParseMode(t *testing.T) {
	for in, want := range map[string]servicecontract.Mode{
		"dev":               servicecontract.ModeDev,
		"production":        servicecontract.ModeProduction,
		"production-strict": servicecontract.ModeProductionStrict,
	} {
		got, err := servicecontract.ParseMode(in)
		if err != nil {
			t.Fatalf("ParseMode(%q) отказал: %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseMode(%q) = %v, ждали %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "prod", "PRODUCTION", "production strict"} {
		if _, err := servicecontract.ParseMode(bad); err == nil {
			t.Fatalf("ParseMode(%q) принял неизвестный режим — умолчание здесь есть решение о доступе, принятое никем", bad)
		}
	}
	if !servicecontract.ModeProduction.IsProduction() || !servicecontract.ModeProductionStrict.IsProduction() {
		t.Fatal("боевой режим не признан боевым")
	}
	if servicecontract.ModeDev.IsProduction() {
		t.Fatal("dev признан боевым")
	}
}
