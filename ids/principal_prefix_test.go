// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestPrincipalPrefixes_AreNamedConstants — приставки двух семейств принципала
// (пользователь и сервисный аккаунт) объявлены ИМЕНОВАННЫМИ константами каталога,
// а не только литералами перечня.
//
// Их читают два производителя одной формы: нормализация субъекта в инициатора
// журнала (`auth.InitiatorOf`) и выражение `CHECK` колонки инициатора, которое
// генератор миграции выводит из этого каталога (NTF-3, З2, З3). Литерал в каждом
// из них был бы вторым местом об одном предмете.
func TestPrincipalPrefixes_AreNamedConstants(t *testing.T) {
	if PrefixUser != "usr" {
		t.Errorf("PrefixUser = %q, ожидалось %q", PrefixUser, "usr")
	}
	if PrefixServiceAccount != "sva" {
		t.Errorf("PrefixServiceAccount = %q, ожидалось %q", PrefixServiceAccount, "sva")
	}
	known, hyphen := KnownPrefixes(), KnownHyphenPrefixes()
	for _, p := range []string{PrefixUser, PrefixServiceAccount} {
		if _, ok := known[p]; !ok {
			t.Errorf("приставка %q вне KnownPrefixes", p)
		}
		if _, ok := hyphen[p]; !ok {
			t.Errorf("приставка %q вне KnownHyphenPrefixes", p)
		}
	}
}

// TestIsValidHyphen — дефисная форма `<приставка>-<17>` судится так же строго,
// как слитная у IsValid: приставка ровно та, тело — 17 знаков крокфордова
// алфавита. Близнец каждого отказа — годный идентификатор, выпущенный
// NewHyphenID той же приставкой.
func TestIsValidHyphen(t *testing.T) {
	good := NewHyphenID(PrefixUser)
	if !IsValidHyphen(good, PrefixUser) {
		t.Fatalf("IsValidHyphen(%q, %q) = false на идентификаторе, выпущенном NewHyphenID", good, PrefixUser)
	}
	body := good[len(PrefixUser)+1:]
	cases := map[string]struct{ id, prefix string }{
		"чужая приставка":        {good, PrefixServiceAccount},
		"слитная форма":          {PrefixUser + body, PrefixUser},
		"тело короче":            {PrefixUser + "-" + body[1:], PrefixUser},
		"тело длиннее":           {good + "0", PrefixUser},
		"знак вне алфавита":      {PrefixUser + "-" + "U" + body[1:], PrefixUser},
		"пустая строка":          {"", PrefixUser},
		"пустая приставка":       {"-" + body, ""},
		"приставка длиннее трёх": {"usrx-" + body, "usrx"},
		"приставка короче двух":  {"u-" + body, "u"},
		"точка вместо дефиса":    {PrefixUser + "." + body, PrefixUser},
	}
	for name, c := range cases {
		if IsValidHyphen(c.id, c.prefix) {
			t.Errorf("%s: IsValidHyphen(%q, %q) = true", name, c.id, c.prefix)
		}
	}
}
