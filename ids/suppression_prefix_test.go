// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestSuppressionPrefix_InCanon — запись подавления notify (NTF-4, правка Х1;
// PRO-Robotech/kacho#2919; приёмка NTF-4 §1.1, ресурс `Suppression`) адресуется
// формой `nsp-<17>`, и служба `notify` чеканит её через NewHyphenID.
//
// Без записи в каноне дефис-префиксов `validate.ResourceID` отвергает
// КОРРЕКТНЫЙ идентификатор, который продукт сам и произвёл: `Get` и `Delete`
// записи по её собственному `id` отвечали бы `INVALID_ARGUMENT` на всяком входе
// (NTF4-24, NTF4-82) — тот же класс, что у `lim`, `mbr`, `ak` и `ntc`.
//
// Канон утверждается ЛИТЕРАЛОМ, а не через экспортируемую константу: иначе,
// пока префикс не заведён, файл не собрался бы, и «префикс не объявлен» стало бы
// неотличимо от «файл не компилируется».
func TestSuppressionPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["nsp"]; !ok {
		t.Errorf("дефис-префикс %q отсутствует в KnownHyphenPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный %q- идентификатор "+
			"записи подавления (NTF-4, Х1)", "nsp", "nsp")
	}
}

// TestSuppressionPrefix_NotInLegacyCanon — префикс `nsp` живёт ТОЛЬКО в
// дефис-каноне: приёмка NTF-4 объявляет форму `nsp-<crockford>`, и слитная
// `nsp<17>` продуктом не чеканится. Запись в слитном каноне открыла бы
// маршрутизатору вторую форму того же `id`, которой продукт не выдаёт.
func TestSuppressionPrefix_NotInLegacyCanon(t *testing.T) {
	if _, ok := KnownPrefixes()["nsp"]; ok {
		t.Errorf("префикс %q записан в слитном каноне KnownPrefixes — "+
			"форма записи подавления только дефисная (nsp-<17>)", "nsp")
	}
}

// TestSuppressionPrefix_MintsHyphenForm — КОНТРОЛЬ: генератор принимает префикс
// `nsp`, длина чеканки — 21 знак (`nsp-` и 17), тело лежит в алфавите канона.
// Поэтому отказ маршрутизатора после записи в канон может означать только
// отсутствие записи, а не форму тела.
func TestSuppressionPrefix_MintsHyphenForm(t *testing.T) {
	id := NewHyphenID("nsp")
	if len(id) != 21 || id[:4] != "nsp-" {
		t.Fatalf("NewHyphenID(%q) = %q — ожидалась форма nsp-<17>", "nsp", id)
	}
	for i := 4; i < len(id); i++ {
		if !isCrockfordChar(id[i]) {
			t.Errorf("знак %q вне крокфордова алфавита в %q", id[i], id)
		}
	}
}
