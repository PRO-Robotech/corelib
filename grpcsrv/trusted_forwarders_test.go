// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

import (
	"testing"
)

// TestZeroTrustedForwardersIsNeverNarrowed — нулевое значение типа обязано быть
// САМЫМ СТРОГИМ прочтением, какое доступно предикату.
//
// Запретить нулевое значение Go не даёт: `var f TrustedForwarders` соберётся
// всегда. Значит единственное, чем можно распорядиться, — это ЧТО ОНО ЗНАЧИТ.
// Оно значит «круг не сужен», поэтому «забыл заполнить» упирается в отказ старта
// у каждого, кто сужает, а не в тихое «доверяем любому предъявившему сертификат».
// Обратное прочтение (нулевое значение отвечает «сужено») сделало бы пропуск
// поля неотличимым от заполненного круга — ровно та дыра, ради которой круг и
// заведён.
func TestZeroTrustedForwardersIsNeverNarrowed(t *testing.T) {
	var zero TrustedForwarders
	if zero.IsNarrowed() {
		t.Fatal("the zero value reports itself as narrowed: a forgotten field would then pass every " +
			"boot guard and every self-report while the process trusts ANY certificate-verified peer " +
			"to speak for a user")
	}
	if got := zero.Len(); got != 0 {
		t.Fatalf("zero value Len() = %d, want 0", got)
	}
	if got := zero.SANs(); len(got) != 0 {
		t.Fatalf("zero value SANs() = %v, want empty", got)
	}
}

// TestNewTrustedForwardersDropsWhatTheTransportDrops — конструктор обязан считать
// ровно то, что доедет до решения о доверии.
//
// Круг сужается только непустыми записями: `SANS=","` даёт срез из двух пустых
// строк, а транспорт их отбрасывает. Предикат, считающий длину сырого среза,
// назвал бы такой круг суженным — и вернул бы дыру через стражу старта.
func TestNewTrustedForwardersDropsWhatTheTransportDrops(t *testing.T) {
	cases := []struct {
		name     string
		raw      []string
		narrowed bool
		want     []string
	}{
		{name: "nothing given", raw: nil, narrowed: false},
		{name: "single blank", raw: []string{""}, narrowed: false},
		{name: "comma only", raw: []string{"", ""}, narrowed: false},
		{name: "whitespace only", raw: []string{"   ", "\t"}, narrowed: false},
		{
			name:     "one real SAN",
			raw:      []string{"spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"},
			narrowed: true,
			want:     []string{"spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"},
		},
		{
			name:     "surrounding whitespace is trimmed",
			raw:      []string{" spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway "},
			narrowed: true,
			want:     []string{"spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"},
		},
		{
			name:     "blanks mixed with a real SAN",
			raw:      []string{"", "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway", ""},
			narrowed: true,
			want:     []string{"spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"},
		},
		{
			name:     "duplicates collapse",
			raw:      []string{"spiffe://kacho.cloud/ns/kacho/sa/a", "spiffe://kacho.cloud/ns/kacho/sa/a"},
			narrowed: true,
			want:     []string{"spiffe://kacho.cloud/ns/kacho/sa/a"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewTrustedForwarders(tc.raw...)
			if f.IsNarrowed() != tc.narrowed {
				t.Fatalf("IsNarrowed() = %v, want %v (raw %q)", f.IsNarrowed(), tc.narrowed, tc.raw)
			}
			got := f.SANs()
			if len(got) != len(tc.want) {
				t.Fatalf("SANs() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("SANs()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestTrustedForwardersSANsCannotBeMutatedThroughTheReturnedSlice — круг обязан
// быть неизменяемым после сборки: иначе «один объект у стража, отчёта и
// транспорта» перестаёт быть одним и тем же значением.
func TestTrustedForwardersSANsCannotBeMutatedThroughTheReturnedSlice(t *testing.T) {
	f := NewTrustedForwarders("spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway")
	got := f.SANs()
	got[0] = "spiffe://evil"
	if again := f.SANs(); again[0] != "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway" {
		t.Fatalf("the circle changed through a returned slice: %q", again[0])
	}
}

// TestPrincipalExtractChainKeepsThePairInOrder — пара извлечения обязана
// приезжать целиком и в одном порядке.
//
// Сертификат доказывает, ЧЕЙ это пир, и ничего не говорит о праве представляться
// другим: сначала снимается личность сертификата, и только потом по ней решается,
// принимать ли переданные заголовки. Собранная вручную пара позволяет и потерять
// звено, и переставить их местами; собранная конструктором — нет.
func TestPrincipalExtractChainKeepsThePairInOrder(t *testing.T) {
	d := NewTrustDomain("kacho.cloud")
	f := NewTrustedForwarders("spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway")
	if got := len(PrincipalExtractUnary(d, f)); got != 2 {
		t.Fatalf("PrincipalExtractUnary returned %d interceptor(s), want the pair (2)", got)
	}
	if got := len(PrincipalExtractStream(d, f)); got != 2 {
		t.Fatalf("PrincipalExtractStream returned %d interceptor(s), want the pair (2)", got)
	}
}
