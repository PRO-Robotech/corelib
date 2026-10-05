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

	"github.com/PRO-Robotech/corelib/authz/proxytuple"
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

// TestHostInternalOnly_WithdrawnIdentityAxesAreRefused — у формы контур
// поднимает носитель, и звенья извлечения личности на её единственном слушателе
// стоят всегда. Изъятие круга пересылающих или домена доверия означало бы право
// говорить за пользователя у любого пира с проверенным сертификатом — отказ с
// именем поля. Законный близнец — то же изъятие у формы без gRPC-слушателей
// принимается (звеньев там нет вовсе).
func TestHostInternalOnly_WithdrawnIdentityAxesAreRefused(t *testing.T) {
	noCircle := internalOnlyLawful()
	noCircle.Forwarders = servicecontract.NotApplicable[grpcsrv.TrustedForwarders]("внутренним вызывающим верим")
	t.Logf("красный: %s", refuses(t, noCircle, "Forwarders"))

	noDomain := internalOnlyLawful()
	noDomain.TrustDomain = servicecontract.NotApplicable[grpcsrv.TrustDomain]("внутренним вызывающим верим")
	t.Logf("красный: %s", refuses(t, noDomain, "TrustDomain"))

	twin := noGRPCSpec()
	twin.Forwarders = servicecontract.NotApplicable[grpcsrv.TrustedForwarders]("внутренним вызывающим верим")
	twin.TrustDomain = servicecontract.NotApplicable[grpcsrv.TrustDomain]("внутренним вызывающим верим")
	if _, err := servicecontract.New(twin); err != nil {
		t.Fatalf("близнец без gRPC-слушателей с теми же изъятиями отвергнут — отрицание выше вакуумно: %v", err)
	}
}

// TestHostInternalOnly_CarriedContourIsJudgedLikeThePair — проводка формы
// судится тем же перечнем, что у пары: внутренний адрес и транспорт, источник
// решения, обязательная внутренняя половина потолка, О10. Каждая строка меняет
// против контроля ровно одно поле.
func TestHostInternalOnly_CarriedContourIsJudgedLikeThePair(t *testing.T) {
	for _, c := range []struct {
		field string
		drop  func(*servicecontract.Spec)
	}{
		{"Authz", func(s *servicecontract.Spec) {
			s.Authz = servicecontract.AuthzSource(0)
			s.CheckEdge = servicecontract.PeerEdge{}
			s.PeerCheck = nil
		}},
		{"InternalAddr", func(s *servicecontract.Spec) { s.InternalAddr = "" }},
		{"InternalCreds", func(s *servicecontract.Spec) { s.InternalCreds = nil }},
		{"Emits", func(s *servicecontract.Spec) { s.Emits = servicecontract.Axis[[]proxytuple.Relation]{} }},
		{"Admission", func(s *servicecontract.Spec) {
			s.Admission = servicecontract.Value(servicecontract.Admission{})
		}},
		{"Admission", func(s *servicecontract.Spec) {
			s.Admission = servicecontract.NotApplicable[servicecontract.Admission]("внутренним вызывающим верим")
		}},
	} {
		s := internalOnlyLawful()
		c.drop(&s)
		t.Logf("красный %s: %s", c.field, refuses(t, s, c.field))
	}
}

// TestHostInternalOnly_RefusesAnOwnContour — собственный контур объявляет, что
// контур поднимает не носитель, а форма — обратное: отказ с именем поля.
func TestHostInternalOnly_RefusesAnOwnContour(t *testing.T) {
	s := internalOnlyLawful()
	s.OwnContour = "контур собираю сам"
	t.Logf("красный: %s", refuses(t, s, "OwnContour"))
}

// TestHostFormReason_IsRefusedOnTheOtherForms — причина формы у пары и у формы
// без gRPC-слушателей — второе утверждение о форме рядом с осью: отказ с именем
// поля. Близнецы — те же дескрипторы без причины — принимаются (контроли групп).
func TestHostFormReason_IsRefusedOnTheOtherForms(t *testing.T) {
	pair := lawful()
	pair.HostFormReason = internalOnlyReason
	t.Logf("красный пара: %s", refuses(t, pair, "HostFormReason"))

	none := noGRPCSpec()
	none.HostFormReason = internalOnlyReason
	t.Logf("красный no-grpc: %s", refuses(t, none, "HostFormReason"))
}
