// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestAccessKeyPrefix_InCanon — ключ доступа человека (WebAuthn, iam Ф7,
// PRO-Robotech/kacho#1273; приёмка `access-keys-are-ours.md`, Р10, §9 п. 8)
// адресуется формой `ak-<17>`.
//
// Без записи в каноне дефис-префиксов `validate.ResourceID` отвергает
// КОРРЕКТНЫЙ идентификатор, который сам же продукт и произвёл: снятие ключа по
// его собственному `id` отвечало бы `INVALID_ARGUMENT` на всяком входе — тот же
// класс, что у `lim` и `mbr` (`api-conventions.md` §«ДВА ПРАВИЛА ОБ ОДНОМ ПОЛЕ»).
//
// Канон утверждается ЛИТЕРАЛОМ, а не через экспортируемую константу: иначе,
// пока префикс не заведён, файл не собрался бы, и «префикс не объявлен» стало бы
// неотличимо от «файл не компилируется».
func TestAccessKeyPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["ak"]; !ok {
		t.Errorf("дефис-префикс %q отсутствует в KnownHyphenPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный %q- идентификатор "+
			"(iam Ф7, приёмка Р10, §9 п. 8)", "ak", "ak")
	}
}

// TestAccessKeyPrefix_MintsHyphenForm — КОНТРОЛЬ: генератор допускает
// двухсимвольный префикс, и тело чеканки лежит в алфавите канона — то есть
// «ключ отвергнут маршрутизатором» после записи в канон может означать только
// отсутствие записи, а не форму тела.
func TestAccessKeyPrefix_MintsHyphenForm(t *testing.T) {
	id := NewHyphenID("ak")
	if len(id) != 20 || id[:3] != "ak-" {
		t.Fatalf("NewHyphenID(%q) = %q — ожидалась форма ak-<17>", "ak", id)
	}
	for i := 3; i < len(id); i++ {
		if !isCrockfordChar(id[i]) {
			t.Errorf("знак %q вне крокфордова алфавита в %q", id[i], id)
		}
	}
}
