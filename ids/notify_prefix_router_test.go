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

// TestNotifyPrefixes_RouterAcceptsTheMintedIDs — наблюдаемое свойство правки Х1
// приёмки NTF-4: идентификатор записи подавления (`nsp-<17>`) и идентификатор
// операции notify (`nop<17>`), выданные продуктом, проходят единую проверку
// формы `validate.ResourceID` — ту, что стоит первым стейтментом `Get`/`Delete`
// подавления (NTF4-24, NTF4-29, NTF4-82) и на ребре прав края.
//
// Проба идёт через потребителя канона, а не через сам канон: запись в
// `ids`, не дошедшая до маршрутизатора, здесь краснеет.
//
// Законные близнецы — `nsx-…` и `nox…` той же длины и того же алфавита: они
// отличаются от предмета ровно сегментом префикса и обязаны получать отказ с
// точным текстом `invalid <вид> id '<X>'` (NTF4-29 (а) называет текст
// `invalid suppression id '…'`). Без близнецов проба прошла бы и на
// маршрутизаторе, принимающем любую форму.
func TestNotifyPrefixes_RouterAcceptsTheMintedIDs(t *testing.T) {
	body := "0123456789abcdefg"
	cases := []struct {
		name     string
		resource string
		prefix   string
		id       string
		accept   bool
	}{
		{name: "подавление: пример формы приёмки", resource: "suppression", prefix: "nsp", id: "nsp-" + body, accept: true},
		{name: "подавление: выданный генератором", resource: "suppression", prefix: "nsp", id: ids.NewHyphenID("nsp"), accept: true},
		{name: "подавление: близнец с чужим префиксом той же формы", resource: "suppression", prefix: "nsp", id: "nsx-" + body, accept: false},
		{name: "подавление: неверный вход NTF4-29 (а)", resource: "suppression", prefix: "nsp", id: "not-an-id", accept: false},
		{name: "операция: пример слитной формы", resource: "operation", prefix: "nop", id: "nop" + body, accept: true},
		{name: "операция: выданная генератором", resource: "operation", prefix: "nop", id: ids.NewID("nop"), accept: true},
		{name: "операция: близнец с чужим префиксом той же формы", resource: "operation", prefix: "nop", id: "nox" + body, accept: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validate.ResourceID(tc.resource, tc.prefix, tc.id)
			if tc.accept {
				if err != nil {
					t.Fatalf("validate.ResourceID(%q) = %v — корректный id отвергнут", tc.id, err)
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
			want := "invalid " + tc.resource + " id '" + tc.id + "'"
			if st.Code() != codes.InvalidArgument || st.Message() != want {
				t.Fatalf("validate.ResourceID(%q) = %s %q — ожидалось InvalidArgument %q",
					tc.id, st.Code(), st.Message(), want)
			}
		})
	}
}

// TestNotifyPrefixes_DoNotCollide — оба префикса не совпадают ни с одним уже
// выданным префиксом другого предмета ни в одном из двух канонов. Совпадение
// сделало бы тип ресурса неотличимым по `id`, а маршрут операций края —
// неоднозначным. Проба читает оба канона целиком (перепись печатается), поэтому
// ловит и совпадение, внесённое позже чужим префиксом.
func TestNotifyPrefixes_DoNotCollide(t *testing.T) {
	legacy := ids.KnownPrefixes()
	hyphen := ids.KnownHyphenPrefixes()
	if len(legacy) == 0 || len(hyphen) == 0 {
		t.Fatalf("перепись пуста: слитный канон %d, дефис-канон %d — проба ничего не читает",
			len(legacy), len(hyphen))
	}
	if _, ok := hyphen["nop"]; ok {
		t.Errorf("префикс операций notify %q совпадает с записью дефис-канона", "nop")
	}
	if _, ok := legacy["nsp"]; ok {
		t.Errorf("префикс подавления %q совпадает с записью слитного канона", "nsp")
	}
	t.Logf("перепись: слитный канон %d записей, дефис-канон %d записей", len(legacy), len(hyphen))
}
