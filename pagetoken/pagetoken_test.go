// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package pagetoken_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/pagetoken"
)

// TestRoundTripKeepsNanoseconds — точность до наносекунд сохраняется.
//
// Keyset сравнивает ХРАНИМУЮ отметку времени, а не усечённую до секунд, которую
// несёт сообщение ресурса. Потеряв точность здесь, страница начала бы повторять
// или пропускать строки, созданные в одну секунду, — и заметно это стало бы
// только на плотной вставке.
func TestRoundTripKeepsNanoseconds(t *testing.T) {
	want := pagetoken.Cursor{
		CreatedAt: time.Date(2026, 8, 22, 10, 0, 0, 123456789, time.UTC),
		ID:        "acc-1",
	}
	got, ok := pagetoken.Decode(pagetoken.Encode(want))
	if !ok || got == nil {
		t.Fatal("собственный токен не разобрался")
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("время не сошлось: %v против %v", got.CreatedAt, want.CreatedAt)
	}
	if got.ID != want.ID {
		t.Fatalf("идентификатор не сошёлся: %q против %q", got.ID, want.ID)
	}
}

// TestEmptyTokenIsTheFirstPage — отсутствие представимо ОТДЕЛЬНО от значения.
func TestEmptyTokenIsTheFirstPage(t *testing.T) {
	c, ok := pagetoken.Decode("")
	if !ok {
		t.Fatal("пустой токен — первая страница, а не негодный вход")
	}
	if c != nil {
		t.Fatal("у первой страницы курсора нет; нулевой курсор выдал бы «начало эпохи» за границу")
	}
}

// TestIdentifierMayContainTheSeparator — тело режется РОВНО НА ДВЕ части.
//
// Идентификатор вправе содержать что угодно, кроме первого разделителя. Резать
// по всем вхождениям значило бы терять хвост такого идентификатора молча.
func TestIdentifierMayContainTheSeparator(t *testing.T) {
	want := pagetoken.Cursor{CreatedAt: time.Now().UTC(), ID: "id|with|bars"}
	got, ok := pagetoken.Decode(pagetoken.Encode(want))
	if !ok || got == nil || got.ID != want.ID {
		t.Fatalf("идентификатор с разделителем потерян: %+v", got)
	}
}

// TestMalformedIsRejected — и по каждой оси отдельно, иначе «отвергает всё»
// неотличимо от «проверяет форму».
func TestMalformedIsRejected(t *testing.T) {
	cases := map[string]string{
		"не base64":            "!!!!",
		"нет разделителя":      encodeRaw("2026-08-22T10:00:00Z"),
		"пустой идентификатор": encodeRaw("2026-08-22T10:00:00Z|"),
		"время не разбирается": encodeRaw("вчера|acc-1"),
	}
	for name, token := range cases {
		if _, ok := pagetoken.Decode(token); ok {
			t.Errorf("%s: принято, а форма не сошлась", name)
		}
	}
	// Положительный контроль: без него отрицания выше зеленели бы на кодеке,
	// отвергающем любой вход.
	if _, ok := pagetoken.Decode(encodeRaw("2026-08-22T10:00:00Z|acc-1")); !ok {
		t.Fatal("законный токен отвергнут — отрицания выше ничего не доказывают")
	}
}

// TestTheTwoFormsAreNotInterchangeable — токены двух форм НЕ совместимы, и это
// объявлено, а не обнаруживается на чужом курсоре.
func TestTheTwoFormsAreNotInterchangeable(t *testing.T) {
	c := pagetoken.Cursor{CreatedAt: time.Now().UTC(), ID: "lb-1"}
	other := pagetoken.NULSeparatedRawURL.Encode(c)

	if _, ok := pagetoken.Decode(other); ok {
		t.Error("канонический кодек принял токен чужой формы — тогда различие форм " +
			"не выражено, и несовместимость обнаружится у клиента")
	}
	back, ok := pagetoken.NULSeparatedRawURL.Decode(other)
	if !ok || back == nil || back.ID != c.ID {
		t.Fatal("своя форма не разобрала свой же токен")
	}
}

// encodeRaw собирает токен из ПРОИЗВОЛЬНОГО тела — иначе негодную форму не
// подать: кодек своим Encode собирает только законную.
func encodeRaw(body string) string {
	return base64.StdEncoding.EncodeToString([]byte(body))
}
