// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/auth"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/operations"
)

// TestInitiatorOf_Table — таблица З3 замысла NTF-3 построчно, у каждой строки
// отказа — близнец, отличающийся от неё ровно одним фактом.
func TestInitiatorOf_Table(t *testing.T) {
	usr := ids.NewID(ids.PrefixUser)
	usrHyphen := ids.NewHyphenID(ids.PrefixUser)
	sva := ids.NewID(ids.PrefixServiceAccount)
	svaHyphen := ids.NewHyphenID(ids.PrefixServiceAccount)

	accepted := []struct {
		name string
		in   operations.Principal
		want string
	}{
		{"пользователь, слитная форма", operations.Principal{Type: "user", ID: usr}, "user:" + usr},
		{"пользователь, дефисная форма", operations.Principal{Type: "user", ID: usrHyphen}, "user:" + usrHyphen},
		{"сервисный аккаунт, слитная форма", operations.Principal{Type: "service_account", ID: sva}, "service_account:" + sva},
		{"сервисный аккаунт, дефисная форма", operations.Principal{Type: "service_account", ID: svaHyphen}, "service_account:" + svaHyphen},
		// Форма SystemPrincipalFor (М7): тип user, признак `system.` в id.
		{"компонент x-y", operations.Principal{Type: "user", ID: "system.x-y"}, "system:x-y"},
		{"компонент SystemPrincipalFor", auth.SystemPrincipalFor("nlb", "free-ip-runner"), "system:nlb-free-ip-runner"},
	}
	for _, c := range accepted {
		got, err := auth.InitiatorOf(c.in)
		if err != nil {
			t.Errorf("%s: InitiatorOf(%+v) отказал: %v", c.name, c.in, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("%s: InitiatorOf(%+v) = %q, ожидалось %q", c.name, c.in, got.String(), c.want)
		}
	}

	refused := []struct {
		name string
		in   operations.Principal
	}{
		// Близнец «компонент x-y»: тот же id, тип system (форма SystemPrincipal).
		{"{system, bootstrap}", operations.SystemPrincipal()},
		{"{system, system.x-y}", operations.Principal{Type: "system", ID: "system.x-y"}},
		{"анонимный край", operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID}},
		// Близнец пользователя: тип вне трёх.
		{"тип вне словаря", operations.Principal{Type: "robot", ID: usr}},
		{"пустой тип", operations.Principal{Type: "", ID: usr}},
		{"пустой id пользователя", operations.Principal{Type: "user", ID: ""}},
		// Близнецы по семейству: id сервисного аккаунта у типа user и наоборот.
		{"пользователь с id сервисного аккаунта", operations.Principal{Type: "user", ID: sva}},
		{"сервисный аккаунт с id пользователя", operations.Principal{Type: "service_account", ID: usr}},
		// Признак `system.` судится только у типа user.
		{"сервисный аккаунт с признаком system.", operations.Principal{Type: "service_account", ID: "system.x-y"}},
		{"id рабочего процесса", operations.Principal{Type: "user", ID: "usr-A"}},
		// Компонент, чьё имя не DNS-метка: пустое, заглавные, подчёркивание, точка.
		{"пустой компонент", operations.Principal{Type: "user", ID: "system."}},
		{"компонент с заглавной", operations.Principal{Type: "user", ID: "system.X-y"}},
		{"компонент с подчёркиванием", operations.Principal{Type: "user", ID: "system.x_y"}},
		{"компонент с точкой", operations.Principal{Type: "user", ID: "system.x.y"}},
		{"компонент длиннее метки", operations.Principal{Type: "user", ID: "system." + strings.Repeat("a", 64)}},
	}
	for _, c := range refused {
		got, err := auth.InitiatorOf(c.in)
		if err == nil {
			t.Errorf("%s: InitiatorOf(%+v) = %q, ожидался отказ", c.name, c.in, got.String())
			continue
		}
		if !errors.Is(err, auth.ErrNoInitiator) {
			t.Errorf("%s: отказ %v не несёт auth.ErrNoInitiator", c.name, err)
		}
		if got.String() != "" {
			t.Errorf("%s: при отказе отдан непустой инициатор %q", c.name, got.String())
		}
	}
}

// TestInitiatorOf_RefusalDoesNotLeakTheID — текст отказа не несёт значение id:
// отказ уходит в журнал процесса и в ошибку транзакции, а id принципала —
// идентификатор личности.
func TestInitiatorOf_RefusalDoesNotLeakTheID(t *testing.T) {
	sva := ids.NewID(ids.PrefixServiceAccount)
	_, err := auth.InitiatorOf(operations.Principal{Type: "user", ID: sva})
	if err == nil {
		t.Fatal("ожидался отказ")
	}
	if strings.Contains(err.Error(), sva) {
		t.Errorf("текст отказа %q несёт id принципала", err.Error())
	}
}
