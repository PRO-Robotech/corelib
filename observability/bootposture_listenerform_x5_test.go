// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// bootposture_listenerform_x5_test.go — правка Х5 (NTF-4 §1.1; замысел
// issue-2919 З18, Е2 (а)): форма слушателя в самоотчёте посадки.
//
// Ключ формы ОДИН — `listener_form` с закрытым набором `pair` | `internal_only`
// | `none`. `none` печатается тогда и только тогда, когда запись несёт
// `NoServedServices`; запись с `NoServedServices` и ненулевой формой отвергает
// конструктор записи. Прежний ключ `host_form` (NTF-1) снимается: два ключа об
// одном предмете в одной строке — два утверждения, из которых верно одно.
package observability_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/PRO-Robotech/corelib/observability"
)

// postureLine печатает запись и возвращает разобранную строку самоотчёта.
func postureLine(t *testing.T, p observability.BootPosture) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	observability.LogBootPosture(observability.NewSlogger(&buf), p)
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("строка самоотчёта не JSON: %v (raw=%q)", err, buf.String())
	}
	return line
}

// TestLogBootPosture_ListenerFormHasThreeDeclaredStates — три написания ключа
// `listener_form`, и ни одной строки с ключом `host_form`.
func TestLogBootPosture_ListenerFormHasThreeDeclaredStates(t *testing.T) {
	for _, c := range []struct {
		name string
		give observability.BootPosture
		want string
	}{
		{"нулевое значение — пара", observability.BootPosture{Service: "vpc"}, "pair"},
		{"только внутренний слушатель", observability.BootPosture{
			Service: "notify", ListenerForm: observability.ListenerFormInternalOnly}, "internal_only"},
		{"сервисов нет", observability.BootPosture{Service: "notify", NoServedServices: true}, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			line := postureLine(t, c.give)
			got, ok := line["listener_form"]
			if !ok {
				t.Fatalf("ключа listener_form нет в строке самоотчёта: %v", line)
			}
			if got != c.want {
				t.Fatalf("listener_form = %v, ожидалось %q", got, c.want)
			}
			if _, dup := line["host_form"]; dup {
				t.Fatalf("в строке два ключа формы — listener_form и host_form: %v", line)
			}
		})
	}
}

// TestListenerFormZeroValueIsThePair — нулевое значение поля формы — пара: у
// каждой прочей службы это настоящее значение, а не «не объявлено».
func TestListenerFormZeroValueIsThePair(t *testing.T) {
	var zero observability.ListenerForm
	if zero != observability.ListenerFormPair {
		t.Fatalf("нулевое значение формы слушателя = %v, ожидалась пара", zero)
	}
}

// TestNewBootPosture_AcceptsNoServedServicesWithTheZeroForm — законный близнец:
// запись «сервисов нет» с нулевой формой конструктор принимает.
func TestNewBootPosture_AcceptsNoServedServicesWithTheZeroForm(t *testing.T) {
	if _, err := observability.NewBootPosture(observability.BootPosture{
		Service: "notify", NoServedServices: true,
	}); err != nil {
		t.Fatalf("законная запись «сервисов нет» отвергнута — отрицание ниже вакуумно: %v", err)
	}
}

// TestNewBootPosture_RefusesNoServedServicesWithANonZeroForm — дельта к близнецу
// одна: поле формы ненулевое. Запись утверждала бы и «слушателя нет», и
// «слушатель внутренний».
func TestNewBootPosture_RefusesNoServedServicesWithANonZeroForm(t *testing.T) {
	_, err := observability.NewBootPosture(observability.BootPosture{
		Service: "notify", NoServedServices: true, ListenerForm: observability.ListenerFormInternalOnly,
	})
	if err == nil {
		t.Fatal("запись с NoServedServices и формой internal_only принята — строка несла бы два утверждения о форме")
	}
	t.Logf("красный: %v", err)
}
