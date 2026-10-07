// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package moduleselfgating_test

import (
	"reflect"
	"testing"

	"github.com/PRO-Robotech/corelib/moduleselfgating"
)

// TestNotifyReadsVGet — служба уведомлений гейтит v_get своим прод-кодом
// (notify-api: сужатель затронутых ресурсов спрашивает v_get, NTF-5 S1,
// kacho#2924). Истинность объявления держит гейт платформы
// TestModuleSelfGatingMatchesTheProdCodeOfEachModule; здесь — что объявление
// говорит ровно это и не больше.
func TestNotifyReadsVGet(t *testing.T) {
	rels, ok := moduleselfgating.Relations("notify")
	if !ok {
		t.Fatalf("про модуль notify объявление молчит; объявлены: %v", moduleselfgating.Modules())
	}
	if want := []string{"v_get"}; !reflect.DeepEqual(rels, want) {
		t.Fatalf("notify: объявлено %v, ожидалось %v", rels, want)
	}
	if !moduleselfgating.Reads("notify", "v_get") {
		t.Fatal("Reads(notify, v_get) ложно при объявленном отношении")
	}
	// Отрицательный близнец: отношение, которого notify не читает.
	if moduleselfgating.Reads("notify", "v_update") {
		t.Fatal("Reads(notify, v_update) истинно — объявление шире прод-кода")
	}
}

// TestDeclarationHasNoEmptyEntry — запись без предмета (пустой перечень) не
// бывает: ok=true с пустым перечнем запрещён шапкой Relations.
func TestDeclarationHasNoEmptyEntry(t *testing.T) {
	mods := moduleselfgating.Modules()
	if len(mods) == 0 {
		t.Fatal("объявление пусто — читатель третьей полосы остался бы без источника")
	}
	for _, m := range mods {
		rels, ok := moduleselfgating.Relations(m)
		if !ok || len(rels) == 0 {
			t.Errorf("модуль %s: ok=%v, отношений %d — запись без предмета", m, ok, len(rels))
		}
	}
	t.Logf("осмотрено модулей: %d", len(mods))
}
