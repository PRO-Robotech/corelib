// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestNotifyOperationPrefix_Known — операции notify (NTF-4, правка Х1;
// PRO-Robotech/kacho#2919; приёмка NTF-4 DoD S3 п.4) чеканятся слитной формой
// `nop<17>` через NewID: `Delete` записи подавления возвращает `Operation`, и
// край маршрутизирует `OperationService.Get` по первым трём знакам `id`
// (`prefixToBackend["nop"] = "notify"`), как `enp`, `aop`, `sop`.
//
// Без записи в слитном каноне `HasKnownPrefix` и `validate.ResourceID`
// отвергали бы КОРРЕКТНЫЙ `id` операции, который служба сама и выдала.
//
// Префикс утверждается ЛИТЕРАЛОМ по той же причине, что в пробах дефис-канона:
// «префикс не объявлен» не должно становиться «файл не компилируется».
func TestNotifyOperationPrefix_Known(t *testing.T) {
	if _, ok := KnownPrefixes()["nop"]; !ok {
		t.Errorf("префикс операций %q отсутствует в KnownPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный id операции notify (NTF-4, Х1)", "nop")
	}
	id := NewID("nop")
	if !HasKnownPrefix(id) {
		t.Errorf("HasKnownPrefix(%q) = false — операция notify не классифицируется", id)
	}
	if !IsValid(id, "nop") {
		t.Errorf("IsValid(%q, %q) = false — форма чеканки нарушена", id, "nop")
	}
}

// TestNotifyOperationPrefix_NotInHyphenCanon — префиксы операций по доменам
// маршрутизируются слитной формой и в дефис-канон не входят (как
// `sop`/`enp`/`iop`/`rop`/`aop`/`epd`): запись там открыла бы вторую форму того
// же `id`, которой продукт не выдаёт.
func TestNotifyOperationPrefix_NotInHyphenCanon(t *testing.T) {
	if _, ok := KnownHyphenPrefixes()["nop"]; ok {
		t.Errorf("префикс операций %q записан в дефис-каноне — операции notify чеканятся слитной формой", "nop")
	}
}
