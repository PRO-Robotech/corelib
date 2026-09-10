// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestResourceID_Membership — маршрутизатор обязан классифицировать дефис-форму
// идентификатора членства (IAM-ID-2-04: негодная форма отвергается синхронно,
// а well-formed и несуществующий уходит в полосу отсутствия).
//
// Половины идут ПАРОЙ намеренно: одно отрицание («негодное отвергается») зеленеет
// при незаведённом префиксе — тогда отвергается ВСЁ — и не утверждало бы ничего.
func TestResourceID_Membership(t *testing.T) {
	// Тело — то, что производит `kaname.membership_mirror_id`: 17 шестнадцатеричных цифр.
	const wellFormed = "mbr-0123456789abcdef0"

	// Положительная половина: корректный идентификатор членства проходит рубеж
	// формы и оставляется `repo.Get` — полоса NOT_FOUND по by-lane code-split.
	if err := ResourceID("membership", "mbr", wellFormed); err != nil {
		t.Errorf("ResourceID(%q) = %v, want nil — корректный дефис-идентификатор "+
			"членства обязан проходить рубеж формы", wellFormed, err)
	}

	// Отрицательная половина: строка, которая не может быть идентификатором ни
	// одного объявленного семейства, отвергается синхронно, контракт-тоном.
	const malformed = "не-идентификатор"
	err := ResourceID("membership", "mbr", malformed)
	if err == nil {
		t.Fatalf("ResourceID(%q) = nil, want InvalidArgument", malformed)
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("ResourceID(%q): code = %s, want InvalidArgument", malformed, st.Code())
	}
	if want := "invalid membership id '" + malformed + "'"; st.Message() != want {
		t.Errorf("ResourceID(%q): message = %q, want %q", malformed, st.Message(), want)
	}

	// Контроль собственной предпосылки: НЕзаведённый префикс той же дефис-формы
	// по-прежнему отвергается. Без него канон, принимающий всякую дефис-строку,
	// прошёл бы положительную половину, и она не утверждала бы про `mbr` ничего.
	const foreignShape = "zz-0123456789abcdef0"
	if err := ResourceID("membership", "mbr", foreignShape); err == nil {
		t.Errorf("ResourceID(%q) = nil — канон принимает НЕзаведённый дефис-префикс, "+
			"поэтому положительная половина ничего не доказывает про %q", foreignShape, "mbr")
	} else if st, _ := status.FromError(err); !strings.Contains(st.Message(), foreignShape) {
		t.Errorf("ResourceID(%q): сообщение %q не называет негодный идентификатор", foreignShape, st.Message())
	}

	// FAMILY-AGNOSTIC ПО КОНТРАКТУ — и это утверждается, а не подразумевается.
	// `expectedPrefix` не читается: ЧУЖОЙ, но ОБЪЯВЛЕННЫЙ префикс форму проходит.
	// Отсюда следует полоса ответа у одиночного чтения членства: такой вход обязан
	// дать «не найдено», а не «негодный аргумент», — и проба фиксирует именно то
	// свойство валидатора, из которого это следует.
	const foreignKnown = "usr-0123456789abcdef0"
	if err := ResourceID("membership", "mbr", foreignKnown); err != nil {
		t.Errorf("ResourceID(%q) = %v, want nil — валидатор family-agnostic: чужой "+
			"ОБЪЯВЛЕННЫЙ префикс проходит форму и обязан отвечать полосой отсутствия", foreignKnown, err)
	}
}
