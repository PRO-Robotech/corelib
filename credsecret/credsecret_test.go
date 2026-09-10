// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package credsecret_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/credsecret"
	"github.com/PRO-Robotech/corelib/ids"
)

// BAT-1-01 — разбор даёт три части, идентификатор дословен, длины объявлены.
func TestBAT1_01_MintedSecretParsesIntoThreeParts(t *testing.T) {
	const credID = "uoc0123456789abcdefg"

	s, _, err := credsecret.Mint(credID)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	p, err := credsecret.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	if p.Mark != credsecret.Mark {
		t.Errorf("марка = %q, ожидалась %q", p.Mark, credsecret.Mark)
	}
	if p.CredentialID != credID {
		t.Errorf("идентификатор = %q, ожидался %q (дословно)", p.CredentialID, credID)
	}
	if len(p.SecretPart) != credsecret.SecretPartLen {
		t.Errorf("секретная часть = %d знаков, объявлено %d", len(p.SecretPart), credsecret.SecretPartLen)
	}
	if len(p.Checksum) != credsecret.ChecksumLen {
		t.Errorf("контрольная сумма = %d знаков, объявлено %d", len(p.Checksum), credsecret.ChecksumLen)
	}
}

// BAT-1-02 — разбор по ПОСЛЕДНЕМУ разделителю; положительный контроль рядом.
func TestBAT1_02_ParseSplitsOnTheLastSeparator(t *testing.T) {
	for _, credID := range []string{
		"uoc_legacybodywithunderscore", // сам содержит разделитель
		"uoc0123456789abcdefg",         // без внутреннего разделителя — положительный контроль
	} {
		s, _, err := credsecret.Mint(credID)
		if err != nil {
			t.Fatalf("Mint(%q): %v", credID, err)
		}
		p, err := credsecret.Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		if p.CredentialID != credID {
			t.Errorf("идентификатор восстановлен как %q, ожидался %q", p.CredentialID, credID)
		}
	}
}

// BAT-1-03 — полоса выбирается по МАРКЕ; строка без марки нашей полосой не является.
func TestBAT1_03_LaneIsChosenByTheMark(t *testing.T) {
	if credsecret.HasMark("eyJhbGciOiJFUzI1NiJ9.e30.sig") {
		t.Error("подписанный токен опознан как наша полоса")
	}
	if credsecret.HasMark("") {
		t.Error("пустая строка опознана как наша полоса")
	}
	s, _, err := credsecret.Mint("uoc0123456789abcdefg")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !credsecret.HasMark(s) { // положительный контроль
		t.Errorf("строка с маркой не опознана: %q", s)
	}
}

// BAT-1-04 — длина, алфавит, отсутствие разделителя; положительный контроль рядом.
func TestBAT1_04_MalformedShapeIsRejected(t *testing.T) {
	const credID = "uoc0123456789abcdefg"
	good, _, err := credsecret.Mint(credID)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	p, err := credsecret.Parse(good)
	if err != nil {
		t.Fatalf("Parse(good): %v", err)
	}

	cases := map[string]string{
		"короче объявленного":  credsecret.Mark + credID + "_" + p.SecretPart[1:] + p.Checksum,
		"длиннее объявленного": credsecret.Mark + credID + "_" + p.SecretPart + "0" + p.Checksum,
		"знак вне алфавита":    credsecret.Mark + credID + "_" + "i" + p.SecretPart[1:] + p.Checksum,
		"нет разделителя":      credsecret.Mark + credID + p.SecretPart + p.Checksum,
		"только марка":         credsecret.Mark,
	}
	for name, in := range cases {
		if _, err := credsecret.Parse(in); err == nil {
			t.Errorf("%s: принято, ожидался отказ (%q)", name, in)
		}
	}
	if _, err := credsecret.Parse(good); err != nil { // положительный контроль
		t.Errorf("годная строка отвергнута: %v", err)
	}
}

// BAT-1-05 — неверная контрольная сумма отвергается; та же строка с верной — принимается.
func TestBAT1_05_BadChecksumIsRejected(t *testing.T) {
	const credID = "uoc0123456789abcdefg"
	good, _, err := credsecret.Mint(credID)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	p, err := credsecret.Parse(good)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	bad := credsecret.Mark + credID + "_" + p.SecretPart + bumpFirst(p.Checksum)
	if _, err := credsecret.Parse(bad); err == nil {
		t.Errorf("строка с негодной контрольной суммой принята: %q", bad)
	}
	if _, err := credsecret.Parse(good); err != nil { // положительный контроль
		t.Errorf("годная строка отвергнута: %v", err)
	}
}

// BAT-1-06 — секретная часть одного удостоверения, приставленная к идентификатору
// другого, удостоверением не является: контрольная сумма и хеш покрывают ОБЕ части.
func TestBAT1_06_HalvesOfTwoCredentialsDoNotCompose(t *testing.T) {
	const idA, idB = "uoc0123456789abcdefg", "uoc0123456789abcdefh"

	a, hashA, err := credsecret.Mint(idA)
	if err != nil {
		t.Fatalf("Mint(A): %v", err)
	}
	b, hashB, err := credsecret.Mint(idB)
	if err != nil {
		t.Fatalf("Mint(B): %v", err)
	}
	pa, err := credsecret.Parse(a)
	if err != nil {
		t.Fatalf("Parse(A): %v", err)
	}
	pb, err := credsecret.Parse(b)
	if err != nil {
		t.Fatalf("Parse(B): %v", err)
	}

	chimera := credsecret.Mark + idB + "_" + pa.SecretPart + pa.Checksum
	if _, err := credsecret.Parse(chimera); err == nil {
		t.Errorf("химера принята разбором: %q", chimera)
	}
	// И даже если бы разбор прошёл — хеш её не признаёт.
	if credsecret.Verify(idB, pa.SecretPart, hashB) {
		t.Error("хеш B признал секретную часть A")
	}
	if !credsecret.Verify(idA, pa.SecretPart, hashA) { // положительный контроль
		t.Error("хеш A не признал собственную секретную часть")
	}
	if !credsecret.Verify(idB, pb.SecretPart, hashB) { // положительный контроль
		t.Error("хеш B не признал собственную секретную часть")
	}
	if bytes.Equal(hashA, hashB) {
		t.Error("хеши двух удостоверений совпали")
	}
}

// BAT-1-07 (половина U) — негодная контрольная сумма отсекается на уровне 2,
// то есть ДО всякого обращения к авторитету: Parse отказывает сам.
func TestBAT1_07_ChecksumRejectsWithoutAnyAuthorityCall(t *testing.T) {
	const credID = "uoc0123456789abcdefg"
	good, _, err := credsecret.Mint(credID)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	p, _ := credsecret.Parse(good)
	bad := credsecret.Mark + credID + "_" + p.SecretPart + bumpFirst(p.Checksum)

	if _, err := credsecret.Parse(bad); err == nil {
		t.Fatal("негодная контрольная сумма принята")
	}
	if _, err := credsecret.Parse(good); err != nil { // положительный контроль
		t.Fatalf("годная строка отвергнута: %v", err)
	}
}

// BAT-1-08 — источник случайности: две подряд произведённые строки различны;
// отдавший ошибку источник даёт ОТКАЗ, а не строку предсказуемого вида.
func TestBAT1_08_RandomnessIsCryptographicAndFailureRefuses(t *testing.T) {
	const credID = "uoc0123456789abcdefg"
	seen := map[string]struct{}{}
	for i := 0; i < 64; i++ {
		s, _, err := credsecret.Mint(credID)
		if err != nil {
			t.Fatalf("Mint #%d: %v", i, err)
		}
		p, err := credsecret.Parse(s)
		if err != nil {
			t.Fatalf("Parse #%d: %v", i, err)
		}
		if _, dup := seen[p.SecretPart]; dup {
			t.Fatalf("секретная часть повторилась на итерации %d", i)
		}
		seen[p.SecretPart] = struct{}{}
	}

	if _, _, err := credsecret.MintFrom(failingReader{}, credID); err == nil {
		t.Error("сорванный источник случайности дал строку вместо отказа")
	}
}

// BAT-1-67 — оба префикса удостоверений классифицируются платформенным каталогом.
func TestBAT1_67_BothCredentialPrefixesAreKnownToTheCatalog(t *testing.T) {
	for _, id := range []string{"uoc0123456789abcdefg", "soc0123456789abcdefg"} {
		if !ids.HasKnownPrefix(id) {
			t.Errorf("префикс %q не классифицируется ids.KnownPrefixes()", id[:3])
		}
	}
}

// BAT-1-65 — предикат формы объявлен ровно один раз и доступен гейту.
func TestBAT1_65_ThePatternIsDeclaredOnceAndExported(t *testing.T) {
	re := credsecret.Pattern()
	if re == nil {
		t.Fatal("credsecret.Pattern() вернул nil")
	}
	s, _, err := credsecret.Mint("uoc0123456789abcdefg")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !re.MatchString(s) {
		t.Errorf("объявленный предикат не принимает собственную чеканку: %q", s)
	}
	if re.MatchString(strings.ToUpper(s)) {
		t.Error("предикат принял верхний регистр — алфавит объявлен нижним")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("источник случайности недоступен")
}

func bumpFirst(s string) string {
	if s[0] == '0' {
		return "1" + s[1:]
	}
	return "0" + s[1:]
}
