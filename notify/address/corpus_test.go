// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package address

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/idna"
)

// frozenEntry — замороженный случай профиля: вход и ключ, который профиль
// выпустил. malformed — вход, который профиль не преобразует.
type frozenEntry struct {
	in, key   string
	malformed bool
}

// frozenCorpus — замороженный корпус NTF1-B27 (IDNA). Ключи окон лимита и
// сетки notify построены этим профилем; смена профиля перекладывает ключи, и
// корпус на ней краснеет. Случай меняется только вместе с решением о смене
// профиля — это ломающая правка ключа (З3, CX1-30).
var frozenCorpus = []frozenEntry{
	// регистр домена сводится отображением профиля
	{in: "User@Example.Invalid", key: "User@example.invalid"},
	{in: "User@example.invalid", key: "User@example.invalid"},
	// локальная часть — без изменений (Р15 NTF-4)
	{in: "user@example.invalid", key: "user@example.invalid"},
	// Unicode-домен и его A-label — один ключ (УК36)
	{in: "user@bücher.example.invalid", key: "user@xn--bcher-kva.example.invalid"},
	{in: "user@xn--bcher-kva.example.invalid", key: "user@xn--bcher-kva.example.invalid"},
	{in: "user@BÜCHER.example.invalid", key: "user@xn--bcher-kva.example.invalid"},
	// профиль непереходный: ß не отображается в ss — два ключа (УК36)
	{in: "user@straße.example.invalid", key: "user@xn--strae-oqa.example.invalid"},
	{in: "user@strasse.example.invalid", key: "user@strasse.example.invalid"},
	// профиль с проверками поиска: вне профиля — отказ
	{in: "user@exa mple.invalid", malformed: true},
	{in: "user@-example.invalid", malformed: true},
	{in: "user@", malformed: true},
	// профиль без проверки длины DNS (VerifyDNSLength не входит в профиль):
	// пустая метка и хвостовая точка проходят как написаны — заморожено, чтобы
	// смена профиля в эту сторону тоже была замечена
	{in: "user@example..invalid", key: "user@example..invalid"},
	{in: "user@example.invalid.", key: "user@example.invalid."},
	{in: "us\r\ner@example.invalid", malformed: true},
}

// corpusFindings прогоняет корпус через нормализацию домена данным профилем.
func corpusFindings(p *idna.Profile) (findings []string, checked int) {
	for _, e := range frozenCorpus {
		checked++
		n, err := normalizeWith(p, e.in)
		switch {
		case e.malformed && !errors.Is(err, ErrMalformed):
			findings = append(findings, fmt.Sprintf("%q: ожидался отказ, профиль дал %q", e.in, n.v))
		case !e.malformed && err != nil:
			findings = append(findings, fmt.Sprintf("%q: ожидался ключ %q, профиль отказал: %v", e.in, e.key, err))
		case !e.malformed && n.v != e.key:
			findings = append(findings, fmt.Sprintf("%q: ожидался ключ %q, профиль дал %q", e.in, e.key, n.v))
		}
	}
	return findings, checked
}

// NTF1-B27 (IDNA): замороженный корпус на профиле пакета — находок 0.
func TestFrozenIDNACorpusHoldsOnThePackageProfile(t *testing.T) {
	findings, checked := corpusFindings(profile)
	t.Logf("случаев корпуса проверено: %d", checked)
	require.Equal(t, len(frozenCorpus), checked)
	require.NotZero(t, checked, "пустой корпус — не зелёный")
	require.Empty(t, findings)

	// Unicode и A-label — один ключ; ß и ss — два (прямо, через Normalize)
	a, err := Normalize("user@bücher.example.invalid")
	require.NoError(t, err)
	b, err := Normalize("user@xn--bcher-kva.example.invalid")
	require.NoError(t, err)
	require.Equal(t, a, b)
	c, err := Normalize("user@straße.example.invalid")
	require.NoError(t, err)
	d, err := Normalize("user@strasse.example.invalid")
	require.NoError(t, err)
	require.NotEqual(t, c, d)
}

// Инъекция: смена профиля на переходный — корпус краснеет и называет случай ß.
func TestFrozenIDNACorpusInjectionTransitionalProfileIsFound(t *testing.T) {
	transitional := idna.New(idna.MapForLookup(), idna.Transitional(true), idna.BidiRule(),
		idna.ValidateLabels(true), idna.StrictDomainName(true))
	findings, _ := corpusFindings(transitional)
	require.NotEmpty(t, findings)
	named := false
	for _, f := range findings {
		t.Log(f)
		named = named || f == fmt.Sprintf("%q: ожидался ключ %q, профиль дал %q",
			"user@straße.example.invalid", "user@xn--strae-oqa.example.invalid", "user@strasse.example.invalid")
	}
	require.True(t, named, "находка обязана называть случай ß")
}

// Инъекция: профиль без StrictDomainName — случай с пробелом в домене
// перестаёт отвергаться, корпус называет его.
func TestFrozenIDNACorpusInjectionLaxProfileIsFound(t *testing.T) {
	lax := idna.New(idna.MapForLookup(), idna.Transitional(false), idna.BidiRule(),
		idna.ValidateLabels(true), idna.StrictDomainName(false))
	findings, _ := corpusFindings(lax)
	require.NotEmpty(t, findings)
	found := false
	for _, f := range findings {
		t.Log(f)
		found = found || f[:len(`"user@exa mple.invalid"`)] == `"user@exa mple.invalid"`
	}
	require.True(t, found, "находка обязана называть домен с пробелом")
}
