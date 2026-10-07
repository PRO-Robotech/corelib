// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

import (
	"strings"
	"testing"
)

// forwarder_no_optin_test.go — процесс, у которого опт-ина «доверять любому
// пересыльщику» НЕТ (NTF-4 Р20), объявляет это стражу, а не держит ручку со
// значением false.
//
// Ручка, которую нельзя законно взвести, — второе написание одного состояния:
// «опт-ина нет» и «опт-ин есть, не испрошен» выглядели бы одинаково, а текст
// отказа звал бы оператора взвести то, чего у процесса быть не должно.

const (
	noOptInSANsKnob = "KACHO_DEMO_AUTHZ_TRUSTED_FORWARDER_SANS"
	gatewaySAN      = "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"
)

// TestNoOptIn_UnnarrowedCircleIsRefusedInEveryMode — без опт-ина пустой круг —
// отказ и вне боевого режима, и текст не зовёт взвести несуществующую ручку.
func TestNoOptIn_UnnarrowedCircleIsRefusedInEveryMode(t *testing.T) {
	for _, production := range []bool{false, true} {
		err := TrustedForwarders{}.Require(ForwarderGate{
			Production: production,
			NoOptIn:    true,
			SANsKnob:   noOptInSANsKnob,
		})
		if err == nil {
			t.Fatalf("production=%v: пустой круг у процесса без опт-ина принят", production)
		}
		msg := err.Error()
		if !strings.Contains(msg, noOptInSANsKnob) {
			t.Errorf("production=%v: отказ не называет ручку круга: %q", production, msg)
		}
		if strings.Contains(msg, "to true") || strings.Contains(msg, "dev opt-in") {
			t.Errorf("production=%v: отказ зовёт взвести опт-ин, которого у процесса нет: %q", production, msg)
		}
		if !strings.Contains(msg, "no trust-any opt-in") {
			t.Errorf("production=%v: отказ не говорит, что опт-ина у процесса нет: %q", production, msg)
		}
	}
}

// TestNoOptIn_NarrowedCircleIsAccepted — законный близнец: суженный круг у
// процесса без опт-ина принимается в обоих режимах.
func TestNoOptIn_NarrowedCircleIsAccepted(t *testing.T) {
	circle := NewTrustedForwarders(gatewaySAN)
	for _, production := range []bool{false, true} {
		if err := circle.Require(ForwarderGate{Production: production, NoOptIn: true, SANsKnob: noOptInSANsKnob}); err != nil {
			t.Fatalf("production=%v: суженный круг отвергнут: %v", production, err)
		}
	}
}

// TestNoOptIn_ContradictsAnOptInRequest — «опт-ина нет» и «опт-ин испрошен»
// одновременно — самопротиворечие, а не опт-ин: отказ даже при суженном круге.
func TestNoOptIn_ContradictsAnOptInRequest(t *testing.T) {
	for _, circle := range []TrustedForwarders{{}, NewTrustedForwarders(gatewaySAN)} {
		err := circle.Require(ForwarderGate{NoOptIn: true, DevTrustAny: true, SANsKnob: noOptInSANsKnob})
		if err == nil {
			t.Fatalf("narrowed=%v: объявление «опт-ина нет» вместе с испрошенным опт-ином принято", circle.IsNarrowed())
		}
	}
}
