// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestNotificationPrefix_InCanon — строка ленты уведомлений (NTF-1, решение Р8;
// kacho#2915) адресуется формой `ntf-<17>`.
//
// Без записи в каноне дефис-префиксов `validate.ResourceID` отвергает
// КОРРЕКТНЫЙ идентификатор, который продукт сам и произвёл, — тот же класс, что
// у `lim`, `mbr` и `ak` (`api-conventions.md` §«ДВА ПРАВИЛА ОБ ОДНОМ ПОЛЕ»).
//
// Канон утверждается ЛИТЕРАЛОМ, а не через экспортируемую константу: иначе,
// пока префикс не заведён, файл не собрался бы, и «префикс не объявлен» стало бы
// неотличимо от «файл не компилируется».
func TestNotificationPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["ntf"]; !ok {
		t.Errorf("дефис-префикс %q отсутствует в KnownHyphenPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный %q- идентификатор "+
			"строки ленты (NTF-1, Р8)", "ntf", "ntf")
	}
}

// TestNotificationPrefix_MintsHyphenForm — КОНТРОЛЬ: генератор чеканит форму
// `ntf-<17>` с телом в алфавите канона — то есть отказ маршрутизатора после
// записи в канон может означать только отсутствие записи, а не форму тела.
func TestNotificationPrefix_MintsHyphenForm(t *testing.T) {
	id := NewHyphenID("ntf")
	if len(id) != 21 || id[:4] != "ntf-" {
		t.Fatalf("NewHyphenID(%q) = %q — ожидалась форма ntf-<17>", "ntf", id)
	}
	for i := 4; i < len(id); i++ {
		if !isCrockfordChar(id[i]) {
			t.Errorf("знак %q вне крокфордова алфавита в %q", id[i], id)
		}
	}
}
