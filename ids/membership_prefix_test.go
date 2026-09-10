// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestMembershipPrefix_InCanon — членство человека в аккаунте адресуется формой
// `mbr-<17>` (IAM-ID-2, DoD п. 3; §1 П6 приёмки).
//
// Без записи в каноне дефис-префиксов `validate.ResourceID` отвергает
// КОРРЕКТНЫЙ идентификатор, который сам же продукт и произвёл: одиночное чтение
// членства отвечало бы `INVALID_ARGUMENT` на всяком входе — возможность
// существовала бы, была бы покрыта пробами формы запроса и не работала бы ни при
// каком вызове (`api-conventions.md` §«ДВА ПРАВИЛА ОБ ОДНОМ ПОЛЕ»).
//
// Канон утверждается ЛИТЕРАЛОМ, а не через экспортируемую константу: иначе,
// пока префикс не заведён, файл не собрался бы, и «префикс не объявлен» стало бы
// неотличимо от «файл не компилируется».
func TestMembershipPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["mbr"]; !ok {
		t.Errorf("дефис-префикс %q отсутствует в KnownHyphenPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный %q- идентификатор "+
			"(IAM-ID-2, DoD п. 3)", "mbr", "mbr")
	}
}

// TestMembershipID_SQLShapeIsInCanonAlphabet — КОНТРОЛЬ, а не предмет.
//
// Идентификатор членства чеканит НЕ `NewHyphenID`, а неизменяемая SQL-функция
// `kaname.membership_mirror_id` — `'mbr-' || substr(md5(…), 1, 17)`. Проба
// записывает, что шестнадцатеричные цифры md5 суть подмножество крокфордова
// алфавита продукта, то есть тело такого идентификатора остаётся в алфавите
// канона. Она зелена и до записи в канон, и после — именно это и делает её
// полезной: она локализует пробел в каноне и снимает подозрение с формы тела.
func TestMembershipID_SQLShapeIsInCanonAlphabet(t *testing.T) {
	const hexDigits = "0123456789abcdef"
	for i := 0; i < len(hexDigits); i++ {
		if !isCrockfordChar(hexDigits[i]) {
			t.Errorf("шестнадцатеричная цифра %q вне крокфордова алфавита — "+
				"тело `mbr-`-идентификатора, чеканимого в SQL из md5, "+
				"не осталось бы в алфавите канона", hexDigits[i])
		}
	}
}
