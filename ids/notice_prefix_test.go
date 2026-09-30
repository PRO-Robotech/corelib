// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestNtcPrefix_InCanon — извещение оператора (NTF-5, PRO-Robotech/kacho#2924;
// приёмка `sub-phase-NTF-5-operator-notices-acceptance.md`, Р3) адресуется
// формой `ntc-<17>`, и служба `notify` чеканит её через NewHyphenID.
//
// Без записи в каноне дефис-префиксов `validate.ResourceID` отвергает
// КОРРЕКТНЫЙ идентификатор, который сам же продукт и произвёл: публичный и
// внутренний `Get` извещения по его собственному `id` отвечали бы
// `INVALID_ARGUMENT` на всяком входе — тот же класс, что у `lim`, `mbr` и `ak`
// (`api-conventions.md` §«ДВА ПРАВИЛА ОБ ОДНОМ ПОЛЕ»).
//
// Канон утверждается ЛИТЕРАЛОМ, а не через экспортируемую константу: иначе,
// пока префикс не заведён, файл не собрался бы, и «префикс не объявлен» стало бы
// неотличимо от «файл не компилируется».
func TestNtcPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["ntc"]; !ok {
		t.Errorf("дефис-префикс %q отсутствует в KnownHyphenPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный %q- идентификатор "+
			"(NTF-5, приёмка Р3)", "ntc", "ntc")
	}
}

// TestNtcPrefix_MintsHyphenForm — КОНТРОЛЬ: генератор принимает префикс `ntc`,
// длина чеканки — 21 знак (`ntc-` и 17, приёмка NTF-5 считает по ней длину
// ссылки), тело лежит в алфавите канона. Поэтому «извещение отвергнуто
// маршрутизатором» после записи в канон может означать только отсутствие
// записи, а не форму тела.
func TestNtcPrefix_MintsHyphenForm(t *testing.T) {
	id := NewHyphenID("ntc")
	if len(id) != 21 || id[:4] != "ntc-" {
		t.Fatalf("NewHyphenID(%q) = %q — ожидалась форма ntc-<17>", "ntc", id)
	}
	for i := 4; i < len(id); i++ {
		if !isCrockfordChar(id[i]) {
			t.Errorf("знак %q вне крокфордова алфавита в %q", id[i], id)
		}
	}
}
