// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple_test

// notification_types_test.go — служебный принципал и объекты ленты уведомлений
// не являются ресурсом модуля (NTF-1, решение Р2 п.5; приёмка NTF1-M10 (б)).
//
// # Предмет
//
// Тип `service` — субъект служебного принципала, `notification_feed` и
// `notification_namespace` — объекты права отправлять и читать уведомления,
// `notification_recipient_directory` — объект права читать справочник
// адресатов (NTF-3, X4D). Все четыре заводит только манифест модели, и ни один
// не принадлежит домену эмитента. Отказ приходится утверждать в посадке, ради которой запретный
// перечень и существует: при НЕИЗВЕСТНОМ домене вызывающего связывание по домену
// отключено, и перечень — единственная оговорка между модулем и чужим объектом.
// Вторая посадка — словарь владения, ошибочно или злонамеренно приписавший тип
// модулю вызывающего: словарь судит ПОСЛЕ перечня и отказа снять не вправе.
//
// # Близнец
//
// Тот же кортеж, отличающийся ОДНИМ фактом — типом объекта, — принимается. Без
// близнеца отказ зеленел бы на правиле, отвергающем всё при пустом домене.
//
// Типы названы ЛИТЕРАЛАМИ, а не выводятся из ForbiddenObjectTypes(): вывод
// утверждал бы перечень им же самим.

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/authz/proxytuple"
)

// notificationTypes — три типа Р2 п.5 и справочник адресатов NTF-3 (X4D),
// каждый вне доменов-эмитентов.
var notificationTypes = []string{"service", "notification_feed", "notification_namespace", "notification_recipient_directory"}

// TestNotificationTypesAreRefusedWithoutADomain — ОТРИЦАНИЕ в посадке, где
// связывание по домену выключено (пустой домен вызывающего).
func TestNotificationTypesAreRefusedWithoutADomain(t *testing.T) {
	for _, typ := range notificationTypes {
		t.Run(typ, func(t *testing.T) {
			err := proxytuple.ValidateTuple("", "user:u1", "owner", typ+":probe")
			require.ErrorIs(t, err, proxytuple.ErrRefused,
				"тип %q записан через прокси при неизвестном домене вызывающего", typ)
		})
	}
}

// TestNotificationTypesAreRefusedEvenWhenTheDictionaryClaimsThem — ОТРИЦАНИЕ:
// словарь владения, приписавший тип модулю вызывающего, отказа не снимает.
func TestNotificationTypesAreRefusedEvenWhenTheDictionaryClaimsThem(t *testing.T) {
	claiming := tableOwner{}
	for _, typ := range notificationTypes {
		claiming[typ] = "vpc"
	}
	for _, typ := range notificationTypes {
		t.Run(typ, func(t *testing.T) {
			err := proxytuple.ValidateTuple("vpc", "user:u1", "project", typ+":vpc",
				proxytuple.WithTypeOwner(claiming))
			require.ErrorIs(t, err, proxytuple.ErrRefused,
				"тип %q записан через прокси по слову словаря владения", typ)
		})
	}
}

// TestModuleTypeIsAcceptedWithoutADomain — ЗАКОННЫЙ БЛИЗНЕЦ первого отрицания:
// тот же субъект, отношение и пустой домен, другой лишь тип объекта.
func TestModuleTypeIsAcceptedWithoutADomain(t *testing.T) {
	require.NoError(t, proxytuple.ValidateTuple("", "user:u1", "owner", "vpc_network:probe"))
}

// TestNotificationTypesAreInTheForbiddenCensus — перечень, который читает гейт
// дерева kacho `TestForbiddenProxyObjectTypesAgreeWithTheModel` (сторона Б),
// несёт все четыре типа: иначе гейт требовал бы от модели типов, которых запрет не
// называет.
func TestNotificationTypesAreInTheForbiddenCensus(t *testing.T) {
	census := proxytuple.ForbiddenObjectTypes()
	for _, typ := range notificationTypes {
		require.True(t, slices.Contains(census, typ),
			"тип %q отсутствует в ForbiddenObjectTypes() = %v", typ, census)
	}
}
