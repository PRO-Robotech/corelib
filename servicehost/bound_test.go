// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

import (
	"strings"
	"testing"

	_ "github.com/PRO-Robotech/corelib/api/corelib/notify"

	"github.com/PRO-Robotech/corelib/servicecontract"
)

// bound_test.go — носитель выводит карту прав ВМЕСТЕ с привязками дескриптора
// (З14): метод формы ScopeBound, для типа которого привязки нет, — отказ
// старта с именем метода, а не отказ голосом прав на каждом вызове.
//
// Вход — настоящий контракт ленты `corelib.notify`, единственный сегодняшний
// производитель формы: подставной дескриптор доказал бы связку вывода с
// привязкой, но не то, что процесс с сервером ленты поднимается.

const (
	feedClaim = "/corelib.notify.InternalNotificationFeedService/Claim"
	feedAck   = "/corelib.notify.InternalNotificationFeedService/Ack"
)

func boundDescriptor(t *testing.T, bound ...servicecontract.Bound) servicecontract.Descriptor {
	t.Helper()
	s := acceptableSpec()
	s.Bound = bound
	d, err := servicecontract.New(s)
	if err != nil {
		t.Fatalf("опорный дескриптор отвергнут — проба вакуумна: %v", err)
	}
	return d
}

// TestRightsMapRefusesAFeedServerWithoutItsBinding — сервер ленты служится, а
// привязки нет: старт отказывает и называет оба метода и тип.
func TestRightsMapRefusesAFeedServerWithoutItsBinding(t *testing.T) {
	_, err := rightsMap(boundDescriptor(t), []string{"corelib.notify"})
	if err == nil {
		t.Fatalf("процесс с непривязанным сервером ленты поднялся бы")
	}
	for _, want := range []string{feedClaim, feedAck, "notification_feed", "kacho-demo"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("отказ не называет %q:\n%v", want, err)
		}
	}
}

// TestRightsMapBindsTheFeedToTheDeclaredInstance — близнец: привязка объявлена,
// и проверка `Claim` спрашивает модель о `notification_feed:<модуль>`.
func TestRightsMapBindsTheFeedToTheDeclaredInstance(t *testing.T) {
	d := boundDescriptor(t, servicecontract.Bound{Type: "notification_feed", ID: "notify-probe"})
	m, err := rightsMap(d, []string{"corelib.notify"})
	if err != nil {
		t.Fatalf("привязанный сервер ленты не поднимается: %v", err)
	}
	for _, key := range []string{feedClaim, feedAck} {
		e, ok := m.Lookup(key)
		if !ok || e.Extract == nil {
			t.Fatalf("%s: записи нет либо она не привязана: %+v", key, e)
		}
		ot, id, xerr := e.Extract(nil)
		if xerr != nil || ot != "notification_feed" || id != "notify-probe" {
			t.Fatalf("%s: объект проверки %q:%q (%v), ожидался notification_feed:notify-probe", key, ot, id, xerr)
		}
	}
}
