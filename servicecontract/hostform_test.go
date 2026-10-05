// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// hostform_test.go — ось формы хоста: одна закрытая ось о том, какие
// gRPC-слушатели поднимает процесс.
//
// Каждая проба — ПАРА: инъекция настоящим входом (дескриптор краснеет и НАЗЫВАЕТ
// поле) и законный близнец, отличающийся ровно одним фактом (дескриптор молчит).
package servicecontract_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/proxytuple"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// noGRPCSpec — законный дескриптор процесса БЕЗ gRPC-слушателей: посадка
// целиком, ни одного поля слушателя и проводки носителя.
func noGRPCSpec() servicecontract.Spec {
	return servicecontract.Spec{
		Service:     "kacho-demo-sender",
		Mode:        servicecontract.ModeProduction,
		HostForm:    servicecontract.HostNoGRPC,
		Forwarders:  servicecontract.NotApplicable[grpcsrv.TrustedForwarders]("gRPC-слушателей нет — переданную личность принимать нечем"),
		TrustDomain: servicecontract.NotApplicable[grpcsrv.TrustDomain]("gRPC-слушателей нет — личность сертификата разбирать нечем"),
		DBSSLMode:   servicecontract.Value("require"),
	}
}

// TestHostForm_ZeroValueIsThePair — нулевое значение оси означает пару слушателей,
// как у всех служб сегодня. Иначе каждый существующий дескриптор, не знающий об
// оси, перестал бы приниматься.
func TestHostForm_ZeroValueIsThePair(t *testing.T) {
	var zero servicecontract.HostForm
	if zero != servicecontract.HostPair {
		t.Fatalf("нулевое значение оси = %v, ожидалась пара слушателей", zero)
	}
	d, err := servicecontract.New(lawful())
	if err != nil {
		t.Fatalf("законный дескриптор пары отвергнут: %v", err)
	}
	if d.HostForm() != servicecontract.HostPair {
		t.Fatalf("принятый дескриптор без оси отвечает формой %v, ожидалась пара", d.HostForm())
	}
	if d.NoServedServices() {
		t.Fatal("дескриптор пары отвечает «сервисов нет» — производное значение разошлось с осью")
	}
}

// TestHostForm_WireNamesAreTheParsedContract — написания значений оси в текстах
// отказов конструктора и носителя: по ним оператор находит форму в дескрипторе.
func TestHostForm_WireNamesAreTheParsedContract(t *testing.T) {
	for _, c := range []struct {
		form servicecontract.HostForm
		want string
	}{
		{servicecontract.HostPair, "pair"},
		{servicecontract.HostNoGRPC, "no-grpc"},
		{servicecontract.HostInternalOnly, "internal-only"},
	} {
		if got := c.form.String(); got != c.want {
			t.Fatalf("форма %d печатается как %q, гейт посадки ждёт %q", uint8(c.form), got, c.want)
		}
	}
}

// TestHostNoGRPC_LawfulSpecIsAccepted — положительный контроль группы. Стоит
// первым: пока он красный, каждое отрицание ниже отказывает по чужой причине.
func TestHostNoGRPC_LawfulSpecIsAccepted(t *testing.T) {
	d, err := servicecontract.New(noGRPCSpec())
	if err != nil {
		t.Fatalf("процесс без gRPC-слушателей не принят — все отрицания ниже вакуумны: %v", err)
	}
	if d.HostForm() != servicecontract.HostNoGRPC {
		t.Fatalf("принятый дескриптор отвечает формой %v, объявлена no-grpc", d.HostForm())
	}
	if !d.NoServedServices() {
		t.Fatal("дескриптор no-grpc не отвечает «сервисов нет» — производное значение разошлось с осью")
	}
	if got := d.HostForm().String(); got != "no-grpc" {
		t.Fatalf("самоотчёт напечатал бы форму %q, ожидалось no-grpc", got)
	}
}

// TestHostNoGRPC_ListenerFieldsAreRefusedByName — при «gRPC-слушателей нет»
// адрес, транспорт слушателя и собственный контур — сочетания, которых не
// бывает. Каждое отвергается с именем поля; близнец — то же без поля — принят
// (TestHostNoGRPC_LawfulSpecIsAccepted).
func TestHostNoGRPC_ListenerFieldsAreRefusedByName(t *testing.T) {
	for _, c := range []struct {
		field string
		spoil func(*servicecontract.Spec)
	}{
		{"PublicAddr", func(s *servicecontract.Spec) { s.PublicAddr = ":9090" }},
		{"InternalAddr", func(s *servicecontract.Spec) { s.InternalAddr = ":9091" }},
		{"PublicCreds", func(s *servicecontract.Spec) { s.PublicCreds = tlsLike{} }},
		{"InternalCreds", func(s *servicecontract.Spec) { s.InternalCreds = tlsLike{} }},
		{"OwnContour", func(s *servicecontract.Spec) { s.OwnContour = "контур собран в своём корне" }},
	} {
		t.Run(c.field, func(t *testing.T) {
			s := noGRPCSpec()
			c.spoil(&s)
			refuses(t, s, c.field, "no-grpc")
		})
	}
}

// TestHostNoGRPC_CarrierWiringIsRefused — проводка носителя у процесса, которому
// носитель слушателей не поднимает: читать её некому, и объявление разошлось бы
// с действительностью молча.
func TestHostNoGRPC_CarrierWiringIsRefused(t *testing.T) {
	for _, c := range []struct {
		field string
		spoil func(*servicecontract.Spec)
	}{
		{"Authz", func(s *servicecontract.Spec) { s.Authz = servicecontract.AuthzSelf }},
		{"HandlingBudget", func(s *servicecontract.Spec) { s.HandlingBudget = 30 * time.Second }},
		{"Metrics", func(s *servicecontract.Spec) { s.Metrics = prometheus.NewRegistry() }},
		{"AuthzObserve", func(s *servicecontract.Spec) { s.AuthzObserve = func(func() authz.Metrics) {} }},
		{"Emits", func(s *servicecontract.Spec) {
			s.Emits = servicecontract.NotApplicable[[]proxytuple.Relation]("нечего")
		}},
		{"Admission", func(s *servicecontract.Spec) {
			s.Admission = servicecontract.Value(servicecontract.Admission{
				Public:   grpcsrv.PlatformPublicAdmission(),
				Internal: grpcsrv.PlatformInternalAdmission(),
			})
		}},
	} {
		t.Run(c.field, func(t *testing.T) {
			s := noGRPCSpec()
			c.spoil(&s)
			refuses(t, s, c.field, "no-grpc")
		})
	}
}

// TestHostNoGRPC_PostureIsStillJudged — изъятие слушателей не снимает посадку:
// она судится при любой форме хоста (ban #16).
func TestHostNoGRPC_PostureIsStillJudged(t *testing.T) {
	weak := noGRPCSpec()
	weak.DBSSLMode = servicecontract.Value("disable")
	refuses(t, weak, "DBSSLMode")

	noDB := noGRPCSpec()
	noDB.DBSSLMode = servicecontract.Axis[string]{}
	refuses(t, noDB, "DBSSLMode")

	withdrawnDB := noGRPCSpec()
	withdrawnDB.DBSSLMode = servicecontract.NotApplicable[string]("своей базы нет")
	refuses(t, withdrawnDB, "DBSSLMode")

	modeless := noGRPCSpec()
	modeless.Mode = servicecontract.Mode(0)
	refuses(t, modeless, "Mode")

	nameless := noGRPCSpec()
	nameless.Service = ""
	refuses(t, nameless, "Service")

	noCircle := noGRPCSpec()
	noCircle.Forwarders = servicecontract.Axis[grpcsrv.TrustedForwarders]{}
	refuses(t, noCircle, "Forwarders")

	noDomain := noGRPCSpec()
	noDomain.TrustDomain = servicecontract.Axis[grpcsrv.TrustDomain]{}
	refuses(t, noDomain, "TrustDomain")
}

// TestHostForm_ValueOutsideTheAxisIsRefused — значение, которого ось не
// объявляет, не является ни парой, ни «слушателей нет». Принять его значило бы
// поднять процесс по форме, которую не разбирает ни конструктор, ни носитель,
// ни самоотчёт. Близнец — тот же дескриптор с нулевой формой — принят
// (TestLawfulSpecIsAccepted).
func TestHostForm_ValueOutsideTheAxisIsRefused(t *testing.T) {
	s := lawful()
	s.HostForm = servicecontract.HostForm(200)
	refuses(t, s, "HostForm")

	own := ownContourSpec()
	own.HostForm = servicecontract.HostForm(200)
	refuses(t, own, "HostForm")
}

// TestHostForm_LiteralDescriptorAnswersNothing — дескриптор, собранный
// литералом, конструктора не проходил: производное «сервисов нет» на нём ложно.
func TestHostForm_LiteralDescriptorAnswersNothing(t *testing.T) {
	var d servicecontract.Descriptor
	if d.NoServedServices() {
		t.Fatal("непринятый дескриптор отвечает «сервисов нет» — решение читало бы непроверенное")
	}
	if d.HostForm() != servicecontract.HostPair {
		t.Fatalf("непринятый дескриптор отвечает формой %v", d.HostForm())
	}
}
