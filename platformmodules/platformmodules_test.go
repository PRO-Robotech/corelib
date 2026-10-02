// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package platformmodules_test

// platformmodules_test.go — пробы объявления модулей платформы.
//
// # Предмет
//
// Служба уведомлений `notify` (замысел NTF-1, решения Д73, Д79) — модуль
// платформы с ДВУМЯ написаниями из трёх:
//
//   - каталог службы `services/notify` и модуль каталога `notify` совпадают:
//     пакет контрактов `kacho.cloud.notify.v1` лежит в
//     `proto/kacho/cloud/notify/` (его заводит D4, kacho#2915);
//   - домена типов нет: типы ленты `notification_feed` и
//     `notification_namespace` заводит манифест модели, ни один не принадлежит
//     модулю, и запись через прокси им запрещена перечнем
//     (authz/proxytuple, NTF1-M10 (б)).
//
// Колонка модуля каталога — сегмент пакета контрактов, а не признак
// публичного слушателя. Пустое её значение утверждало бы «каталога контрактов
// нет», а он есть, — и гейт дерева kacho TestPlatformModuleVocabularyMatchesTheTree
// это и называл. Пустого модуля каталога в объявлении поэтому нет ни у одной
// записи, и проба формы требует этого для всех.

import (
	"slices"
	"testing"

	"github.com/PRO-Robotech/corelib/platformmodules"
)

// TestNotifyIsDeclaredWithItsCatalogModuleAndNoObjectDomain — запись notify
// есть, модуль каталога назван по пакету контрактов, а домен типов отвечен как
// «знаю, и этого нет», а не «не знаю такого».
func TestNotifyIsDeclaredWithItsCatalogModuleAndNoObjectDomain(t *testing.T) {
	if !slices.Contains(platformmodules.Services(), "notify") {
		t.Fatalf("служба notify словарём не объявлена: объявлены %v", platformmodules.Services())
	}

	module, known := platformmodules.CatalogModuleOfService("notify")
	if !known || module != "notify" {
		t.Fatalf("модуль каталога notify: known=%v module=%q, ожидалось known=true и "+
			"\"notify\" — пакет контрактов kacho.cloud.notify.v1", known, module)
	}
	if svc, ok := platformmodules.ServiceOfCatalogModule("notify"); !ok || svc != "notify" {
		t.Fatalf("ServiceOfCatalogModule(\"notify\") = %q, ok=%v, ожидалось notify", svc, ok)
	}

	domain, known := platformmodules.ObjectDomainOfService("notify")
	if !known || domain != "" {
		t.Fatalf("домен типов notify: known=%v domain=%q, ожидалось known=true и пустой "+
			"домен — типы ленты принадлежат манифесту модели, а не модулю", known, domain)
	}
}

// TestModuleWithACatalogModuleKeepsIt — ЗАКОННЫЙ БЛИЗНЕЦ: служба с доменом
// контрактов отвечает им, в том числе там, где написания различны.
func TestModuleWithACatalogModuleKeepsIt(t *testing.T) {
	for service, want := range map[string]string{"vpc": "vpc", "nlb": "loadbalancer"} {
		got, known := platformmodules.CatalogModuleOfService(service)
		if !known || got != want {
			t.Fatalf("модуль каталога %s: known=%v got=%q, ожидалось %q", service, known, got, want)
		}
	}
}

// TestCoincidingSpellingsAreNotAliases — карты расхождений несут только
// различные написания: notify (служба и модуль каталога совпадают) в них не
// попадает, настоящее расхождение nlb ↔ loadbalancer — попадает в обе.
func TestCoincidingSpellingsAreNotAliases(t *testing.T) {
	byService := platformmodules.AliasesByService()
	byCatalog := platformmodules.AliasesByCatalogModule()
	if v, ok := byService["notify"]; ok {
		t.Fatalf("AliasesByService несёт notify → %q: совпадающее написание стало расхождением", v)
	}
	if byService["nlb"] != "loadbalancer" || byCatalog["loadbalancer"] != "nlb" {
		t.Fatalf("настоящее расхождение nlb ↔ loadbalancer выпало из карт: %v / %v",
			byService, byCatalog)
	}
	if len(byService) != 1 || len(byCatalog) != 1 {
		t.Fatalf("расхождений ожидалось ровно одно (nlb): %v / %v", byService, byCatalog)
	}
}

// TestDeclarationIsWellFormed — объявление однозначно: короткие имена и
// модули каталога непусты и не повторяются, и порядок объявления — по имени
// службы, как обещает шапка. Повтор сделал бы ответ поиска зависящим от
// порядка строк; пустой модуль каталога — способом снять с записи сверку
// колонки с деревом контрактов.
func TestDeclarationIsWellFormed(t *testing.T) {
	all := platformmodules.All()
	if len(all) == 0 {
		t.Fatal("объявление пусто — судить нечего")
	}
	services := map[string]bool{}
	catalog := map[string]bool{}
	for _, m := range all {
		if m.Service == "" {
			t.Fatalf("запись без короткого имени службы: %+v", m)
		}
		if services[m.Service] {
			t.Fatalf("короткое имя %q объявлено дважды", m.Service)
		}
		services[m.Service] = true
		if m.CatalogModule == "" {
			t.Fatalf("запись %q без модуля каталога: колонка — сегмент пакета "+
				"контрактов, и у каждой службы платформы он есть", m.Service)
		}
		if catalog[m.CatalogModule] {
			t.Fatalf("модуль каталога %q объявлен дважды", m.CatalogModule)
		}
		catalog[m.CatalogModule] = true
	}
	if !slices.IsSorted(platformmodules.Services()) {
		t.Fatalf("Services() не отсортирован: %v", platformmodules.Services())
	}
	t.Logf("перепись: записей %d · модулей каталога %d", len(all), len(catalog))
}
