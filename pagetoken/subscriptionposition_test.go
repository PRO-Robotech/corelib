// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package pagetoken_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/pagetoken"
)

// TestSubscriptionPositionRoundTrips — позиция возвращается ДОСЛОВНО и
// разбирается в ту же величину.
func TestSubscriptionPositionRoundTrips(t *testing.T) {
	for _, seq := range []int64{0, 1, 42, 1 << 40} {
		token := pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: seq})
		if token == "" {
			t.Fatalf("seq=%d: кодек выдал пустой токен — пустое означает «позиция не задана»", seq)
		}
		got, ok := pagetoken.DecodeSubscriptionPosition(token)
		if !ok {
			t.Fatalf("seq=%d: собственный токен не разобрался", seq)
		}
		if got == nil || got.Settled != seq {
			t.Fatalf("seq=%d: разобралось в %+v", seq, got)
		}
	}
}

// TestSubscriptionPositionEmptyMeansUnset — пустая строка есть ОТСУТСТВИЕ
// позиции, а не нулевая позиция: у «с начала журнала» и «позиция не задана»
// разные исходы, и кодек их не смешивает.
func TestSubscriptionPositionEmptyMeansUnset(t *testing.T) {
	got, ok := pagetoken.DecodeSubscriptionPosition("")
	if !ok {
		t.Fatal("пустой токен обязан разбираться: это законный вход «позиция не задана»")
	}
	if got != nil {
		t.Fatalf("пустой токен обязан давать отсутствие, а дал %+v", got)
	}
}

// TestSubscriptionPositionRejectsForeignAndMalformed — позиция, сконструированная
// клиентом либо им изменённая, отвергается, а не принимается «как похожая».
//
// Отдельной строкой — токен ДРУГОЙ формы того же пакета: он декодируется
// стандартным base64 и потому «выглядит похожим». Приняв его, подписка начала бы
// с величины, которой никто не выдавал.
func TestSubscriptionPositionRejectsForeignAndMalformed(t *testing.T) {
	foreign := pagetoken.Encode(pagetoken.Cursor{ID: "net-abc"})
	cases := map[string]string{
		"не base64": "!!!не токен!!!",
		"чужая форма того же пакета": foreign,
		"своя форма, но не число":    encodeRaw("sub1|не-число"),
		"своя форма, отрицательная":  encodeRaw("sub1|-1"),
		"чужая версия формы":         encodeRaw("sub9|1"),
		"без разделителя":            encodeRaw("sub1"),
		"лишний хвост":               encodeRaw("sub1|1|1"),
	}
	for name, token := range cases {
		if _, ok := pagetoken.DecodeSubscriptionPosition(token); ok {
			t.Errorf("%s: принят токен %q, а обязан быть отвергнут", name, token)
		}
	}
}

// TestSubscriptionPositionIsOpaque — токен не содержит величины в читаемом виде.
//
// Не косметика: клиент, увидевший в токене число, начнёт его сравнивать и
// конструировать, и тогда починка внутренней формы станет ломающим изменением.
func TestSubscriptionPositionIsOpaque(t *testing.T) {
	token := pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: 123456789})
	if strings.Contains(token, "123456789") {
		t.Fatalf("величина видна в токене: %q", token)
	}
}

// TestSubscriptionPositionWellFormed — вердикт о годности берётся тем же
// разбором, что исполняется на пути чтения, а не вторым кодеком рядом.
func TestSubscriptionPositionWellFormed(t *testing.T) {
	if !pagetoken.SubscriptionPositionWellFormed("") {
		t.Error("пустая позиция обязана считаться годной")
	}
	good := pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: 7})
	if !pagetoken.SubscriptionPositionWellFormed(good) {
		t.Error("собственный токен объявлен негодным")
	}
	if pagetoken.SubscriptionPositionWellFormed("мусор") {
		t.Error("мусор объявлен годным")
	}
}
