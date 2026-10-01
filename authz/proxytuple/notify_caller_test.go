// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple_test

// notify_caller_test.go — объявление службы уведомлений в словаре модулей
// (Д73) не открывает ей запись через прокси.
//
// # Предмет
//
// До записи `notify` в platformmodules вызывающий `notify` был словарю
// неизвестен и отвергался целиком. Запись объявляет у него ПУСТЫЕ модуль
// каталога и домен типов. Пустое написание — новое для колонки модуля каталога
// состояние, и ветвь словаря владения сравнивает модуль типа с модулем
// вызывающего на равенство: словарь владения, ответивший о типе пустым модулем,
// совпал бы с пустым модулем notify, и чужая тройка была бы принята. Отказ
// обязан держаться при любом ответе словаря владения.
//
// # Близнец
//
// Тот же ответ словаря владения для вызывающего с непустым модулем каталога,
// совпавшим с модулем типа, — принимается (type_owner_test.go держит его для
// nlb); здесь — тот же вызов с `vpc`, отличающийся одним фактом: вызывающим.

import (
	"errors"
	"testing"

	"github.com/PRO-Robotech/corelib/authz/proxytuple"
	"github.com/PRO-Robotech/corelib/platformmodules"
)

// TestNotifyCallerIsRefusedEvenWhenTheDictionaryAnswersAnEmptyModule —
// ОТРИЦАНИЕ: пустой модуль словаря владения не совпадает с пустым модулем
// каталога вызывающего.
func TestNotifyCallerIsRefusedEvenWhenTheDictionaryAnswersAnEmptyModule(t *testing.T) {
	if module, known := platformmodules.CatalogModuleOfService("notify"); !known || module != "" {
		t.Fatalf("предпосылка пробы: notify объявлен с пустым модулем каталога, а словарь "+
			"отвечает known=%v module=%q", known, module)
	}

	emptyOwner := tableOwner{"notify_thing": ""}
	err := proxytuple.ValidateTuple("notify", "user:u1", "owner", "notify_thing:t1",
		proxytuple.WithTypeOwner(emptyOwner))
	if !errors.Is(err, proxytuple.ErrRefused) {
		t.Fatalf("notify записал тройку через прокси, совпав пустым модулем со словарём "+
			"владения: err=%v", err)
	}
}

// TestNotifyCallerIsRefusedByPrefix — ОТРИЦАНИЕ на приставочной ветви: пустой
// домен типов не даёт notify ни одного своего типа.
func TestNotifyCallerIsRefusedByPrefix(t *testing.T) {
	for _, obj := range []string{"notify_thing:t1", "notification_feed:vpc", "vpc_network:n1"} {
		err := proxytuple.ValidateTuple("notify", "user:u1", "owner", obj)
		if !errors.Is(err, proxytuple.ErrRefused) {
			t.Fatalf("notify записал %s через прокси: err=%v", obj, err)
		}
	}
}

// TestNonEmptyModuleMatchingTheDictionaryIsAccepted — ЗАКОННЫЙ БЛИЗНЕЦ первого
// отрицания: словарь владения отвечает модулем, совпавшим с непустым модулем
// каталога вызывающего.
func TestNonEmptyModuleMatchingTheDictionaryIsAccepted(t *testing.T) {
	owner := tableOwner{"vpc_thing": "vpc"}
	if err := proxytuple.ValidateTuple("vpc", "user:u1", "owner", "vpc_thing:t1",
		proxytuple.WithTypeOwner(owner)); err != nil {
		t.Fatalf("своя тройка vpc по слову словаря владения отвергнута: %v", err)
	}
}
