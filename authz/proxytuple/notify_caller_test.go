// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple_test

// notify_caller_test.go — объявление службы уведомлений в словаре модулей
// (Д73, Д79) не открывает ей запись через прокси ни одного чужого типа и ни
// одного типа ленты.
//
// # Предмет
//
// Запись `notify` в platformmodules несёт модуль каталога `notify` (пакет
// контрактов `kacho.cloud.notify.v1`) и ПУСТОЙ домен типов. Отсюда две ветви
// суда, и обе обязаны отказывать на всём, что notify не принадлежит:
//
//   - ветвь словаря владения сравнивает модуль типа с модулем вызывающего:
//     тип, отнесённый словарём к другому модулю или ни к какому (пустой ответ),
//     notify своим не становится;
//   - приставочная ветвь: пустой домен типов не даёт notify ни одного своего
//     типа, а типы ленты запрещены перечнем при любом вызывающем.
//
// # Близнец
//
// Тот же вызов с ответом словаря владения, совпавшим с модулем notify, —
// принимается: отказ выше держится несовпадением модуля, а не тем, что
// вызывающий — notify.

import (
	"errors"
	"testing"

	"github.com/PRO-Robotech/corelib/authz/proxytuple"
	"github.com/PRO-Robotech/corelib/platformmodules"
)

// TestNotifyCallerIsRefusedWhenTheDictionaryAnswersAnotherModule — ОТРИЦАНИЕ:
// тип, который словарь владения относит к другому модулю или ни к какому,
// notify через прокси не пишет.
func TestNotifyCallerIsRefusedWhenTheDictionaryAnswersAnotherModule(t *testing.T) {
	if module, known := platformmodules.CatalogModuleOfService("notify"); !known || module != "notify" {
		t.Fatalf("предпосылка пробы: notify объявлен с модулем каталога notify, а словарь "+
			"отвечает known=%v module=%q", known, module)
	}

	owner := tableOwner{"notify_thing": "", "vpc_thing": "vpc"}
	for _, obj := range []string{"notify_thing:t1", "vpc_thing:t1"} {
		err := proxytuple.ValidateTuple("notify", "user:u1", "owner", obj,
			proxytuple.WithTypeOwner(owner))
		if !errors.Is(err, proxytuple.ErrRefused) {
			t.Fatalf("notify записал %s через прокси при чужом модуле словаря владения: err=%v", obj, err)
		}
	}
}

// TestNotifyCallerIsAcceptedForATypeOfItsOwnModule — ЗАКОННЫЙ БЛИЗНЕЦ
// отрицания выше: меняется ровно ответ словаря владения — модуль типа совпал
// с модулем каталога notify.
func TestNotifyCallerIsAcceptedForATypeOfItsOwnModule(t *testing.T) {
	owner := tableOwner{"notify_thing": "notify"}
	if err := proxytuple.ValidateTuple("notify", "user:u1", "owner", "notify_thing:t1",
		proxytuple.WithTypeOwner(owner)); err != nil {
		t.Fatalf("тройка notify по слову словаря владения отвергнута: %v", err)
	}
}

// TestNotifyCallerIsRefusedByPrefix — ОТРИЦАНИЕ на приставочной ветви: пустой
// домен типов не даёт notify ни одного своего типа, типы ленты запрещены
// перечнем.
func TestNotifyCallerIsRefusedByPrefix(t *testing.T) {
	for _, obj := range []string{"notify_thing:t1", "notification_feed:vpc", "vpc_network:n1"} {
		err := proxytuple.ValidateTuple("notify", "user:u1", "owner", obj)
		if !errors.Is(err, proxytuple.ErrRefused) {
			t.Fatalf("notify записал %s через прокси: err=%v", obj, err)
		}
	}
}

// TestNotificationTypesAreRefusedEvenWhenTheDictionaryAnswersNotify —
// ОТРИЦАНИЕ: перечень запрещённых типов судит раньше словаря владения, поэтому
// тип ленты не открывается notify и тогда, когда словарь отнёс его к notify.
func TestNotificationTypesAreRefusedEvenWhenTheDictionaryAnswersNotify(t *testing.T) {
	owner := tableOwner{"notification_feed": "notify", "notification_namespace": "notify"}
	for _, obj := range []string{"notification_feed:vpc", "notification_namespace:vpc"} {
		err := proxytuple.ValidateTuple("notify", "user:u1", "owner", obj,
			proxytuple.WithTypeOwner(owner))
		if !errors.Is(err, proxytuple.ErrRefused) {
			t.Fatalf("notify записал %s через прокси: err=%v", obj, err)
		}
	}
}

// TestNonEmptyModuleMatchingTheDictionaryIsAccepted — ЗАКОННЫЙ БЛИЗНЕЦ для
// вызывающего с совпадающими написаниями службы и модуля.
func TestNonEmptyModuleMatchingTheDictionaryIsAccepted(t *testing.T) {
	owner := tableOwner{"vpc_thing": "vpc"}
	if err := proxytuple.ValidateTuple("vpc", "user:u1", "owner", "vpc_thing:t1",
		proxytuple.WithTypeOwner(owner)); err != nil {
		t.Fatalf("своя тройка vpc по слову словаря владения отвергнута: %v", err)
	}
}
