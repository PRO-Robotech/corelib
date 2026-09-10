// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple_test

// type_owner_test.go — чей это тип, отвечает СЛОВАРЬ, а не приставка имени
// (задача продукта #1885).
//
// # Предмет
//
// Проксируемая запись связывала тип объекта с вызывающим приставкой его имени:
// `vpc` пишет на `vpc_*`. Приставка совпадает с коротким именем домена у всех
// сегодняшних писателей, поэтому проверка не меняла НИ ОДНОГО вердикта — и
// предмет был не в дыре, а в том, что совпадение оставалось УСЛОВИЕМ работы:
// тип, чьё имя в модели не начинается с домена его модуля, был невыразим.
//
// Наивная починка «имя службы == модуль каталога» отняла бы у балансировщика три
// живых типа: у него РАЗЛИЧНЫ три написания — служба `nlb`, модуль каталога
// `loadbalancer`, тип модели `nlb_listener`. Поэтому владельца подаёт СЛОВАРЬ, а
// сравнение идёт по модулю каталога, а не по имени службы.
//
// # Что утверждается ПАРОЙ, и обе половины обязательны
//
// Чужой тип отвергается — и СВОЙ принимается, включая тот, чьё имя приставке не
// подчиняется. Без второй половины проверка зеленела бы на реализации,
// отвергающей всё, и отняла бы живое право.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/authz/proxytuple"
)

// tableOwner — подставной словарь: тип → модуль каталога, его объявивший.
// Повторяет форму настоящего (`authzmap`), не его содержимое: содержимое
// порождается манифестами и здесь было бы второй его копией.
type tableOwner map[string]string

func (t tableOwner) CatalogModuleOfObjectType(objType string) (string, bool) {
	m, ok := t[objType]
	return m, ok
}

// owners — словарь пробы. Три записи, и каждая нужна: своя приставке подчиняется,
// чужая подчиняется тоже (иначе отказ был бы объясним приставкой), а у
// балансировщика написания различны.
var owners = tableOwner{
	"vpc_network":  "vpc",
	"vpc_subnet":   "vpc",
	"nlb_listener": "loadbalancer",
}

// TestTypeOwnedByAnotherModuleIsRefusedByTheDictionary — ОТРИЦАНИЕ: чужой тип
// отвергается, при том что приставке вызывающего он НЕ подчиняется.
func TestTypeOwnedByAnotherModuleIsRefusedByTheDictionary(t *testing.T) {
	err := proxytuple.ValidateTuple("compute", "user:u1", "owner", "vpc_network:net-1",
		proxytuple.WithTypeOwner(owners))
	require.ErrorIs(t, err, proxytuple.ErrRefused,
		"compute записал на тип, объявленный vpc")
}

// TestOwnServiceWritesItsOwnType — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: без него отрицание
// выше зеленело бы на реализации, отвергающей всё.
func TestOwnServiceWritesItsOwnType(t *testing.T) {
	require.NoError(t, proxytuple.ValidateTuple("vpc", "user:u1", "owner", "vpc_network:net-1",
		proxytuple.WithTypeOwner(owners)))
}

// TestSpellingsMayDiverge — ВТОРАЯ ПОЛОВИНА ПАРЫ, ради которой задача и заведена.
//
// У балансировщика служба `nlb`, модуль каталога `loadbalancer`, тип модели
// `nlb_listener`. Предикат «имя службы == модуль каталога» для него не совпадёт
// НИКОГДА и отнял бы три живых типа; наблюдаемо это было бы как «ресурс создан,
// доступа нет».
func TestSpellingsMayDiverge(t *testing.T) {
	require.NoError(t, proxytuple.ValidateTuple("nlb", "user:u1", "owner", "nlb_listener:lsn-1",
		proxytuple.WithTypeOwner(owners)),
		"балансировщик записал на СВОЙ тип — три различных написания это законно")
}

// TestUnknownTypeStillFallsBackToThePrefix — ГРАНИЦА, названная пробой, а не
// прозой.
//
// Тип, которого словарь не знает, судится по-прежнему приставкой. Иначе первый
// же тип, заведённый модулем в манифесте и ещё не доехавший до порождённой
// таблицы, отвергался бы ОПАКОВЫМ отказом — с худшей возможной диагностикой и на
// пути, где вызывающему нечего чинить.
func TestUnknownTypeStillFallsBackToThePrefix(t *testing.T) {
	require.NoError(t, proxytuple.ValidateTuple("vpc", "user:u1", "owner", "vpc_brandnew:x-1",
		proxytuple.WithTypeOwner(owners)),
		"своя приставка у неизвестного словарю типа обязана проходить")
	require.ErrorIs(t,
		proxytuple.ValidateTuple("compute", "user:u1", "owner", "vpc_brandnew:x-1",
			proxytuple.WithTypeOwner(owners)),
		proxytuple.ErrRefused,
		"чужая приставка у неизвестного словарю типа обязана отвергаться")
}

// TestWithoutADictionaryThePrefixStillDecides — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ на
// необязательность словаря: вызывающий, его не подавший, судится как раньше.
//
// Без этой пробы «словарь провязан» было бы неотличимо от «словарь обязателен», и
// первый же вызывающий без него получил бы отказ на всём.
func TestWithoutADictionaryThePrefixStillDecides(t *testing.T) {
	require.NoError(t, proxytuple.ValidateTuple("vpc", "user:u1", "owner", "vpc_network:net-1"))
	require.ErrorIs(t,
		proxytuple.ValidateTuple("compute", "user:u1", "owner", "vpc_network:net-1"),
		proxytuple.ErrRefused)
}
