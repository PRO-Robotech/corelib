// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package validate

// Табличные unit-тесты для общих валидационных примитивов
// (Description/Labels/UpdateMask). Пробы имени ресурса живут отдельно, в
// name_test.go: у имени одна форма на всё дерево, и она проверяется в одном
// месте — см. #715.
// Эти функции несут security-/parity-критичные контракты (regex label-key,
// update_mask discipline: неизвестное поле → InvalidArgument).
//
// Прежняя редакция шапки называла ещё `DdosProvider` и `SmtpCapability` как
// покрытые — ни одной из этих функций в дереве нет, и проб на них в этом файле
// тоже не было. Перечень «покрытого» обязан сверяться с деревом, иначе он
// свидетельствует о покрытии, которого нет.
// Чистые функции, Postgres не нужен.

import (
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// requireInvalidArgument проверяет, что err — gRPC status InvalidArgument.
func requireInvalidArgument(t *testing.T, err error, ctx string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", ctx)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("%s: expected gRPC status, got: %v", ctx, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("%s: expected InvalidArgument, got %v", ctx, st.Code())
	}
}

func TestDescription(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "empty-ok", input: ""},
		{name: "at-limit-256", input: strings.Repeat("x", 256)},
		{name: "over-limit-257", input: strings.Repeat("x", 257), wantErr: true},
		{name: "unicode-256-runes-ok", input: strings.Repeat("é", 256)}, // rune count, not bytes
		{name: "unicode-257-runes-rejected", input: strings.Repeat("é", 257), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Description("description", tc.input)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.name)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

func TestLabels(t *testing.T) {
	// >64 уникальных валидных ключей → триггерит "too many labels".
	tooMany := make(map[string]string, MaxLabels+1)
	for i := 0; i < MaxLabels+1; i++ {
		tooMany["key"+strings.Repeat("a", i)] = "v"
	}

	cases := []struct {
		name    string
		labels  map[string]string
		wantErr bool
	}{
		{name: "empty-ok", labels: map[string]string{}},
		{name: "valid-simple", labels: map[string]string{"env": "prod", "team-a": "x"}},
		{name: "valid-special-key-chars", labels: map[string]string{"a-_./@0": ""}},
		{name: "value-at-63-ok", labels: map[string]string{"k": strings.Repeat("v", MaxLabelValueLen)}},
		{name: "too-many-labels", labels: tooMany, wantErr: true},
		{name: "empty-key-rejected", labels: map[string]string{"": "v"}, wantErr: true},
		{name: "key-starts-digit-rejected", labels: map[string]string{"1bad": "v"}, wantErr: true},
		{name: "key-uppercase-rejected", labels: map[string]string{"Bad": "v"}, wantErr: true},
		{name: "key-too-long-64", labels: map[string]string{strings.Repeat("a", 64): "v"}, wantErr: true},
		{name: "value-too-long-64", labels: map[string]string{"k": strings.Repeat("v", 64)}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Labels("labels", tc.labels)
			if tc.wantErr {
				requireInvalidArgument(t, err, tc.name)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

// TestLabels_InvalidKeyMessageListsAtSign локает contract-текст FieldViolation:
// labelKeyRe допускает '@' в ключе (см. valid-special-key-chars), поэтому
// user-facing allowed-set в сообщении обязан перечислять '@'. Без этого doc/msg
// расходятся с regex (doc-truthfulness): контрибьютор, сверяя код с сообщением,
// удалил бы '@' из regex и молча отверг ранее-валидные ключи вроде `owner@team`.
func TestLabels_InvalidKeyMessageListsAtSign(t *testing.T) {
	err := Labels("labels", map[string]string{"Bad": "v"}) // uppercase → invalid key
	requireInvalidArgument(t, err, "invalid-key")
	st, _ := status.FromError(err)
	var desc string
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, fv := range br.GetFieldViolations() {
				desc = fv.GetDescription()
			}
		}
	}
	if !strings.Contains(desc, "@") {
		t.Fatalf("label-key FieldViolation must list '@' in allowed set (regex allows it), got: %q", desc)
	}
}

// Здесь стояли TestIPAddress и TestDhcpDomainName — сняты вместе со своими
// функциями (vpc-миграция 0029: параметры DHCP подсети сняты с контракта, из
// Go-домена и из схемы, потребителей у обеих функций осталось ноль). Проба,
// переживающая свой предмет, — не покрытие: она держит вызов, которого больше
// нет ни в одном прод-пути, и создаёт вид проверенного контракта.

func TestUpdateMask(t *testing.T) {
	known := map[string]struct{}{"name": {}, "description": {}, "labels": {}}

	t.Run("empty-mask-ok", func(t *testing.T) {
		if err := UpdateMask("update_mask", nil, known); err != nil {
			t.Fatalf("empty mask must be OK, got: %v", err)
		}
		if err := UpdateMask("update_mask", []string{}, known); err != nil {
			t.Fatalf("zero-len mask must be OK, got: %v", err)
		}
	})

	t.Run("all-known-ok", func(t *testing.T) {
		if err := UpdateMask("update_mask", []string{"name", "labels"}, known); err != nil {
			t.Fatalf("known fields must pass, got: %v", err)
		}
	})

	t.Run("unknown-field-rejected", func(t *testing.T) {
		requireInvalidArgument(t, UpdateMask("update_mask", []string{"name", "bogus"}, known), "bogus")
	})

	t.Run("single-unknown-rejected", func(t *testing.T) {
		requireInvalidArgument(t, UpdateMask("update_mask", []string{"zone_id"}, known), "zone_id")
	})
}

// Пробы проверок защиты от распределённых атак и возможности исходящей почты СНЯТЫ
// вместе с самими проверками: их единственный потребитель — сервис сети — снял оба поля
// с контракта. Словарь допустимых значений первой называл имя конкретного ВНЕШНЕГО
// поставщика, то есть публичное поле опознавало конкретную оснастку, и при этом ни одна
// ветвь на значении не ветвилась. У второй не было ни одного законного непустого входа.
