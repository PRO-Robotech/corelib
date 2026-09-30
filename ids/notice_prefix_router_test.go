// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids_test

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/validate"
)

// TestNtcPrefix_RouterAcceptsTheMintedID — наблюдаемое свойство Р3 приёмки
// NTF-5: идентификатор извещения, выданный продуктом, проходит единую проверку
// формы `validate.ResourceID`, на которой стоят первым стейтментом публичный и
// внутренний `Get` извещения.
//
// Проба идёт через потребителя канона, а не через сам канон: запись в
// `hyphenFormPrefixes`, не дошедшая до маршрутизатора, здесь краснеет.
//
// Законный близнец — `ntx-…` той же длины и того же алфавита: он отличается
// от предмета ровно сегментом префикса и обязан получать отказ с точным
// текстом `invalid notice id '<X>'`. Без близнеца проба прошла бы и на
// маршрутизаторе, принимающем любую дефисную форму.
func TestNtcPrefix_RouterAcceptsTheMintedID(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		accept bool
	}{
		{name: "пример приёмки", id: "ntc-0123456789abcdefg", accept: true},
		{name: "выданный генератором", id: ids.NewHyphenID("ntc"), accept: true},
		{name: "близнец: чужой префикс той же формы", id: "ntx-0123456789abcdefg", accept: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.ResourceID("notice", "ntc", tc.id)
			if tc.accept {
				if err != nil {
					t.Fatalf("validate.ResourceID(%q) = %v — корректный id извещения отвергнут", tc.id, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate.ResourceID(%q) = nil — ожидался отказ", tc.id)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("validate.ResourceID(%q): ошибка %v не несёт gRPC-статуса", tc.id, err)
			}
			want := "invalid notice id '" + tc.id + "'"
			if st.Code() != codes.InvalidArgument || st.Message() != want {
				t.Fatalf("validate.ResourceID(%q) = %s %q — ожидалось InvalidArgument %q",
					tc.id, st.Code(), st.Message(), want)
			}
		})
	}
}
