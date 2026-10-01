// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package platformmodules_test

// platformmodules_test.go — пробы объявления модулей платформы.
//
// # Предмет
//
// Служба уведомлений `notify` (замысел NTF-1, решение Д73) — модуль платформы:
// каталог `services/notify` в дереве kacho есть, и гейт дерева
// TestPlatformModuleVocabularyMatchesTheTree требует его записи. Но ни одно из
// двух других написаний у неё НЕ существует:
//
//   - модуля каталога нет: её контракт — `corelib.notify` под нейтральным
//     корнем `corelib`, у которого доменов `<корень>/cloud/<домен>` нет и не
//     предполагается (contractroot.Roots); слушателя с контрактом домена у
//     службы нет по построению (Д17);
//   - домена типов нет: типы ленты `notification_feed` и
//     `notification_namespace` заводит манифест модели, ни один не принадлежит
//     модулю, и запись через прокси им запрещена перечнем
//     (authz/proxytuple, NTF1-M10 (б)).
//
// Оба «нет» — проверяемые факты, а не пропуски. Пустой модуль каталога — новое
// для объявления состояние, поэтому пробы ниже судят и ЧИТАТЕЛЕЙ этой колонки:
// карты расхождений и обратный поиск обязаны не превращать пустую строку в
// написание.

import (
	"slices"
	"testing"

	"github.com/PRO-Robotech/corelib/platformmodules"
)

// TestNotifyIsDeclaredWithNoCatalogModuleAndNoObjectDomain — запись notify
// есть, и оба её «нет» отвечены как «знаю, и этого нет», а не «не знаю такого».
func TestNotifyIsDeclaredWithNoCatalogModuleAndNoObjectDomain(t *testing.T) {
	if !slices.Contains(platformmodules.Services(), "notify") {
		t.Fatalf("служба notify словарём не объявлена: объявлены %v", platformmodules.Services())
	}

	module, known := platformmodules.CatalogModuleOfService("notify")
	if !known || module != "" {
		t.Fatalf("модуль каталога notify: known=%v module=%q, ожидалось known=true и пустое "+
			"написание — у службы нет домена контрактов", known, module)
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

// TestEmptyCatalogModuleIsNotAnAlias — пустое написание не попадает в карты
// расхождений. Читатели карт трактуют присутствие записи как «модуль каталога
// называется иначе» и подставили бы пустую строку вместо имени каталога.
func TestEmptyCatalogModuleIsNotAnAlias(t *testing.T) {
	byService := platformmodules.AliasesByService()
	if v, ok := byService["notify"]; ok {
		t.Fatalf("AliasesByService несёт notify → %q: пустое написание стало расхождением", v)
	}
	byCatalog := platformmodules.AliasesByCatalogModule()
	if v, ok := byCatalog[""]; ok {
		t.Fatalf("AliasesByCatalogModule несёт ключ пустой строки → %q", v)
	}

	// Близнец: настоящее расхождение остаётся в обеих картах.
	if byService["nlb"] != "loadbalancer" || byCatalog["loadbalancer"] != "nlb" {
		t.Fatalf("настоящее расхождение nlb ↔ loadbalancer выпало из карт: %v / %v",
			byService, byCatalog)
	}
}

// TestEmptyCatalogModuleResolvesToNoService — обратный поиск по пустой строке
// отвечает «не знаю», а не службой, у которой модуля каталога нет: иначе
// вызывающий, не нашедший имени каталога, получил бы в ответ notify.
func TestEmptyCatalogModuleResolvesToNoService(t *testing.T) {
	if svc, ok := platformmodules.ServiceOfCatalogModule(""); ok {
		t.Fatalf("ServiceOfCatalogModule(\"\") = %q, ok=true — пустое написание "+
			"разрешилось в службу", svc)
	}
	// Близнец: непустое написание разрешается.
	if svc, ok := platformmodules.ServiceOfCatalogModule("loadbalancer"); !ok || svc != "nlb" {
		t.Fatalf("ServiceOfCatalogModule(\"loadbalancer\") = %q, ok=%v, ожидалось nlb", svc, ok)
	}
}

// TestDeclarationIsWellFormed — объявление однозначно: короткие имена и
// непустые модули каталога не повторяются, и порядок объявления — по имени
// службы, как обещает шапка. Повтор сделал бы ответ поиска зависящим от
// порядка строк.
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
			continue
		}
		if catalog[m.CatalogModule] {
			t.Fatalf("модуль каталога %q объявлен дважды", m.CatalogModule)
		}
		catalog[m.CatalogModule] = true
	}
	if !slices.IsSorted(platformmodules.Services()) {
		t.Fatalf("Services() не отсортирован: %v", platformmodules.Services())
	}
	t.Logf("перепись: записей %d · непустых модулей каталога %d", len(all), len(catalog))
}
