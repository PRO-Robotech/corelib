// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// hostinternalonly_x5_test.go — правка Х5 (NTF-4 §1.1, DoD S3 п.8б; замысел
// issue-2919 З18): третье значение оси формы хоста — «только внутренний
// слушатель» — с обязательной причиной.
//
// Каждая проба — ПАРА: положительный контроль [TestHostInternalOnly_LawfulSpecIsAccepted]
// и отрицания, каждое из которых меняет против него РОВНО ОДИН факт и требует,
// чтобы отказ НАЗВАЛ поле.
package servicecontract_test

import (
	"testing"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// internalOnlyReason — причина формы, как её объявляет корень `notify-api`.
const internalOnlyReason = "notify-api служит только внутренних вызывающих: публичного входа у службы нет по построению"

// internalOnlyLawful — законный дескриптор формы «только внутренний слушатель»:
// посадка целиком, внутренний адрес и транспорт, ни одного поля публичного
// слушателя, потолок — только внутренняя половина.
func internalOnlyLawful() servicecontract.Spec {
	s := lawful()
	s.HostForm = servicecontract.HostInternalOnly
	s.HostFormReason = internalOnlyReason
	s.PublicAddr = ""
	s.PublicCreds = nil
	s.Admission = servicecontract.Value(servicecontract.Admission{
		Internal: grpcsrv.PlatformInternalAdmission(),
	})
	return s
}

// TestHostInternalOnly_LawfulSpecIsAccepted — положительный контроль группы.
// Стоит первым: пока он красный, каждое отрицание ниже отказывает по чужой
// причине.
func TestHostInternalOnly_LawfulSpecIsAccepted(t *testing.T) {
	d, err := servicecontract.New(internalOnlyLawful())
	if err != nil {
		t.Fatalf("законный дескриптор формы «только внутренний слушатель» отвергнут — отрицания ниже вакуумны: %v", err)
	}
	if d.HostForm() != servicecontract.HostInternalOnly {
		t.Fatalf("принятый дескриптор отвечает формой %v, объявлена «только внутренний слушатель»", d.HostForm())
	}
	if d.NoServedServices() {
		t.Fatal("дескриптор формы «только внутренний слушатель» отвечает «сервисов нет» — производное разошлось с осью")
	}
}

// TestHostInternalOnly_ZeroValueStaysThePair — нулевое значение оси по-прежнему
// пара: третье значение не сдвигает нуль, и семь служб, не знающих об оси, не
// меняются.
func TestHostInternalOnly_ZeroValueStaysThePair(t *testing.T) {
	if servicecontract.HostInternalOnly == servicecontract.HostPair {
		t.Fatal("форма «только внутренний слушатель» совпала с нулевым значением оси — пара перестала быть нулём")
	}
	if servicecontract.HostInternalOnly == servicecontract.HostNoGRPC {
		t.Fatal("форма «только внутренний слушатель» совпала с формой без gRPC-слушателей")
	}
}

// TestHostInternalOnly_RefusesWithoutAReason — форма без причины: отказ
// конструктора с именем поля причины. Дельта к контролю — одна: причина пуста.
func TestHostInternalOnly_RefusesWithoutAReason(t *testing.T) {
	s := internalOnlyLawful()
	s.HostFormReason = ""
	t.Logf("красный: %s", refuses(t, s, "HostFormReason"))
}

// TestHostInternalOnly_RefusesAPublicAddress — адрес публичного слушателя в
// форме, где публичного слушателя нет: отказ с именем поля.
func TestHostInternalOnly_RefusesAPublicAddress(t *testing.T) {
	s := internalOnlyLawful()
	s.PublicAddr = ":9090"
	t.Logf("красный: %s", refuses(t, s, "PublicAddr"))
}

// TestHostInternalOnly_RefusesAPublicTransport — транспорт публичного слушателя
// в той же форме: отказ с именем поля.
func TestHostInternalOnly_RefusesAPublicTransport(t *testing.T) {
	s := internalOnlyLawful()
	s.PublicCreds = tlsLike{}
	t.Logf("красный: %s", refuses(t, s, "PublicCreds"))
}

// TestHostInternalOnly_RefusesThePublicHalfOfAdmission — объявленная публичная
// половина потолка на вызывающего: величины слушателя, которого нет, — отказ с
// именем поля.
func TestHostInternalOnly_RefusesThePublicHalfOfAdmission(t *testing.T) {
	s := internalOnlyLawful()
	s.Admission = servicecontract.Value(servicecontract.Admission{
		Public:   grpcsrv.PlatformPublicAdmission(),
		Internal: grpcsrv.PlatformInternalAdmission(),
	})
	t.Logf("красный: %s", refuses(t, s, "Admission"))
}
