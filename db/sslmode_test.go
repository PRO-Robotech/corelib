// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db_test

import (
	"strings"
	"testing"

	coredb "github.com/PRO-Robotech/corelib/db"
)

// Перечень безопасных режимов — обе стороны каждой оси.
//
// Односторонняя проба зеленела бы на предикате, отвергающем всё: «disable
// отвергнут» верно и для функции `return false`. Поэтому по каждой оси стоит
// пара, и положительная половина названа отдельным случаем, а не хвостом.
func TestSSLModeSecureCutsBothWays(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		if !coredb.SSLModeSecure(mode) {
			t.Errorf("режим %q обязан считаться защищённым — боевая посадка на нём законна", mode)
		}
	}
	// `prefer` и отсутствие значения — plaintext-fallback libpq, а не «наверное
	// зашифровано»; `allow` — то же самое с другой стороны.
	for _, mode := range []string{"disable", "allow", "prefer", "", "   ", "requrie", "verify"} {
		if coredb.SSLModeSecure(mode) {
			t.Errorf("режим %q принят как защищённый — до базы пошёл бы открытый канал", mode)
		}
	}
}

// Нормализация — ОДНА на весь дом: значение приходит из переменной окружения и
// из строки подключения, и оба источника допускают регистр и пробелы по краям.
func TestSSLModeNormalizationIsTheHomesJob(t *testing.T) {
	for _, mode := range []string{"REQUIRE", " require ", "Verify-Full", "\tverify-ca\n"} {
		if !coredb.SSLModeSecure(mode) {
			t.Errorf("режим %q отвергнут из-за регистра или пробелов — нормализация обязана "+
				"жить в доме, иначе её делает каждый вызывающий по-своему", mode)
		}
	}
}

// Словарь настройки шире боевого ровно на `disable` — и не шире.
//
// `allow`/`prefer` настройкой НЕ принимаются намеренно: оба молча деградируют
// до открытого канала, поэтому держать их в конфигурации значит предлагать
// ловушку.
func TestConfigurableSSLModesAdmitDisableAndNothingWeaker(t *testing.T) {
	if !coredb.SSLModeConfigurable("disable") {
		t.Error("`disable` обязан приниматься настройкой: вне боевого режима открытый канал до " +
			"локальной базы законен")
	}
	for _, mode := range []string{"allow", "prefer"} {
		if coredb.SSLModeConfigurable(mode) {
			t.Errorf("настройка принимает %q — значение молча деградирует до открытого канала", mode)
		}
	}
	if coredb.SSLModeConfigurable("") {
		t.Error("пустая строка принята как значение: «не задано» — отдельный исход, и решает про " +
			"него вызывающий (часть сервисов деривит пустое в disable, часть берёт режим из URL)")
	}
}

// Вложенность перечней — свойство, а не совпадение: безопасный режим обязан
// приниматься настройкой, иначе его нельзя задать вовсе, и словарь описывает
// не то, чем пользуются.
func TestSecureModesAreConfigurableAndBelongToTheVocabulary(t *testing.T) {
	vocabulary := map[string]bool{}
	for _, m := range coredb.SSLModes() {
		vocabulary[m] = true
	}
	for _, m := range coredb.SecureSSLModes() {
		if !coredb.SSLModeConfigurable(m) {
			t.Errorf("режим %q безопасен, но настройкой не принимается — задать его нечем", m)
		}
		if !vocabulary[m] {
			t.Errorf("режим %q вне словаря — распознаватели дерева его не увидят", m)
		}
	}
	for _, m := range coredb.ConfigurableSSLModes() {
		if !vocabulary[m] {
			t.Errorf("режим %q вне словаря — распознаватели дерева его не увидят", m)
		}
	}
	if len(coredb.SSLModes()) <= len(coredb.ConfigurableSSLModes()) {
		t.Error("словарь не шире принимаемого настройкой: значит `allow`/`prefer` из него выпали, " +
			"и распознаватель дерева перестанет видеть копию, которая их называет")
	}
}

// ПОРЯДОК — часть контракта, а не вкус: из перечней собираются тексты отказов,
// которые видит оператор, и тон сообщений меняется осознанно
// (`api-conventions.md` §Error-format).
func TestOrderedListsRenderTheRefusalTextsVerbatim(t *testing.T) {
	if got := strings.Join(coredb.SecureSSLModes(), "|"); got != "require|verify-ca|verify-full" {
		t.Errorf("текст боевого отказа собрался как %q — контракт сообщения изменён", got)
	}
	if got := strings.Join(coredb.SecureSSLModes(), ", "); got != "require, verify-ca, verify-full" {
		t.Errorf("текст отказа дескриптора собрался как %q — контракт сообщения изменён", got)
	}
	if got := strings.Join(coredb.ConfigurableSSLModes(), ", "); got != "disable, require, verify-ca, verify-full" {
		t.Errorf("текст отказа формы собрался как %q — контракт сообщения изменён", got)
	}
}

// Перечень отдаётся КОПИЕЙ: правило безопасности не должно быть переписываемо
// вызывающим.
func TestReturnedListsAreCopies(t *testing.T) {
	first := coredb.SecureSSLModes()
	first[0] = "disable"
	if again := coredb.SecureSSLModes(); again[0] != "require" {
		t.Fatalf("перечень безопасных режимов переписан вызывающим: %v", again)
	}
}

// Отсутствие `sslmode` в строке — это [DefaultSSLMode], и он НЕ безопасен.
// Пара держит связку двух домов: разбора строки и перечня.
func TestUnsetSSLModeInDSNIsNotSecure(t *testing.T) {
	if coredb.SSLModeSecure(coredb.SSLModeFromDSN("postgres://u:p@h:5432/db")) {
		t.Fatal("строка без sslmode принята как защищённая — libpq откатится на plaintext")
	}
	if !coredb.SSLModeSecure(coredb.SSLModeFromDSN("postgres://u:p@h:5432/db?sslmode=verify-full")) {
		t.Fatal("строка с verify-full не признана защищённой — положительный контроль не выполнен")
	}
}
