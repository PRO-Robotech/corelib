// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/proxytuple"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// countingRegistrar — регистратор, который только считает, звали ли его.
// Регистраций не делает: предмет пробы — дошёл ли носитель до регистрации.
func countingRegistrar(n *int) Registrar {
	return func(grpc.ServiceRegistrar) { *n++ }
}

// TestServeRefusesTheHostWithoutGRPCListeners — дескриптор «gRPC-слушателей
// нет» носителю поднимать нечем: [Serve] отказывает ДО сборки серверов, не
// зовёт ни одного регистратора и называет форму.
//
// Близнец отличается ровно формой хоста: принятый дескриптор пары того же
// сервиса проходит мимо этого отказа и доходит до регистрации (дальше он
// останавливается на своих отказах старта — пустой служимый набор, — но это уже
// не предмет пробы).
func TestServeRefusesTheHostWithoutGRPCListeners(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	noGRPC, err := servicecontract.New(servicecontract.Spec{
		Service:     "kacho-demo-sender",
		Mode:        servicecontract.ModeDev,
		HostForm:    servicecontract.HostNoGRPC,
		Forwarders:  servicecontract.NotApplicable[grpcsrv.TrustedForwarders]("gRPC-слушателей нет"),
		TrustDomain: servicecontract.NotApplicable[grpcsrv.TrustDomain]("gRPC-слушателей нет"),
		DBSSLMode:   servicecontract.Value("require"),
	})
	if err != nil {
		t.Fatalf("дескриптор no-grpc не принят — проба вакуумна: %v", err)
	}
	var calls int
	serr := Serve(ctx, noGRPC, countingRegistrar(&calls), countingRegistrar(&calls))
	if serr == nil {
		t.Fatal("носитель принял дескриптор без gRPC-слушателей — поднял бы серверы, которых форма не объявляет")
	}
	if !strings.Contains(serr.Error(), "no-grpc") {
		t.Fatalf("отказ не называет форму хоста:\n%v", serr)
	}
	if calls != 0 {
		t.Fatalf("носитель позвал регистратор %d раз(а) у дескриптора без gRPC-слушателей", calls)
	}

	// Близнец: та же служба формой пары.
	pair, err := servicecontract.New(servicecontract.Spec{
		Service:         "kacho-demo-sender",
		Mode:            servicecontract.ModeDev,
		Forwarders:      servicecontract.Value(grpcsrv.NewTrustedForwarders("spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway")),
		TrustDomain:     servicecontract.Value(grpcsrv.NewTrustDomain("kacho.cloud")),
		TrustDomainKnob: "KACHO_DEMO_AUTHZ_TRUST_DOMAIN",
		ForwarderKnobs: servicecontract.ForwarderKnobs{
			SANs:     "KACHO_DEMO_AUTHZ_TRUSTED_FORWARDER_SANS",
			TrustAny: "KACHO_DEMO_AUTHZ_TRUST_ANY_FORWARDER",
		},
		DBSSLMode:       servicecontract.Value("require"),
		Authz:           servicecontract.AuthzSelf,
		SelfCheck:       denyingClient(),
		HandlingBudget:  30 * time.Second,
		PublicAddr:      "127.0.0.1:0",
		InternalAddr:    "127.0.0.1:0",
		PublicCreds:     insecure.NewCredentials(),
		InternalCreds:   insecure.NewCredentials(),
		Emits:           servicecontract.NotApplicable[[]proxytuple.Relation]("проба ничего не эмитит"),
		Registers:       servicecontract.NotApplicable[[]servicecontract.ObjectType]("проба не владеет типами"),
		Narrowers:       servicecontract.NotApplicable[map[servicecontract.MethodFQN]servicecontract.ListNarrower]("сужаемых методов нет"),
		HideExistence:   servicecontract.NotApplicable[map[servicecontract.ObjectType]servicecontract.NotFoundFormat]("скрытия нет"),
		Delivery:        servicecontract.NotApplicable[servicecontract.DeliveryProvenance]("проба ничего не эмитит"),
		DenyBudget:      servicecontract.NotApplicable[float64]("решение у себя"),
		AuthzObserve:    func(func() authz.Metrics) {},
		Metrics:         prometheus.NewRegistry(),
		BootGate:        servicecontract.NotApplicable[servicecontract.BootGate]("проба ничего не эмитит"),
		ServiceIdentity: servicecontract.NotApplicable[grpcsrv.ServiceIdentity]("служб-подписчиков у пробы нет"),
		StreamBudget:    servicecontract.NotApplicable[time.Duration]("стримов нет"),
		Admission:       servicecontract.NotApplicable[servicecontract.Admission]("внутрипроцессная проба"),
	})
	if err != nil {
		t.Fatalf("близнец-пара не принят — проба вакуумна: %v", err)
	}
	calls = 0
	perr := Serve(ctx, pair, countingRegistrar(&calls), countingRegistrar(&calls))
	if perr != nil && strings.Contains(perr.Error(), "no-grpc") {
		t.Fatalf("носитель отказал паре по форме хоста:\n%v", perr)
	}
	if calls != 2 {
		t.Fatalf("носитель позвал регистраторы пары %d раз(а), ожидалось 2 — близнец не дошёл до регистрации", calls)
	}
}
