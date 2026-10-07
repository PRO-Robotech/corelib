// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// forwarder_no_optin_test.go — дескриптор умеет сказать «опт-ина доверия
// любому пересыльщику у процесса НЕТ» объявлением с причиной, а не ручкой со
// значением false (NTF-4 Р20).
package servicecontract_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

const noOptInReason = "NTF-4 Р20: доверие любому пересыльщику не включается ни на одной посадке"

// lawfulNoOptIn — законный дескриптор процесса без опт-ина: имени ручки
// опт-ина нет, есть причина его отсутствия.
func lawfulNoOptIn() servicecontract.Spec {
	s := lawful()
	s.ForwarderKnobs = servicecontract.ForwarderKnobs{
		SANs:           "KACHO_DEMO_AUTHZ_TRUSTED_FORWARDER_SANS",
		NoOptInBecause: noOptInReason,
	}
	return s
}

func TestNoOptIn_LawfulSpecIsAccepted(t *testing.T) {
	for _, mode := range []servicecontract.Mode{servicecontract.ModeProduction, servicecontract.ModeDev} {
		s := lawfulNoOptIn()
		s.Mode = mode
		if mode == servicecontract.ModeDev {
			s.DBSSLMode = servicecontract.Value("disable")
		}
		if _, err := servicecontract.New(s); err != nil {
			t.Fatalf("mode=%v: законный дескриптор без опт-ина отвергнут: %v", mode, err)
		}
	}
}

// TestNoOptIn_UnnarrowedCircleRefusedOutsideProduction — вне боевого режима
// пустой круг у процесса без опт-ина — отказ, и текст не зовёт опт-ин.
func TestNoOptIn_UnnarrowedCircleRefusedOutsideProduction(t *testing.T) {
	s := lawfulNoOptIn()
	s.Mode = servicecontract.ModeDev
	s.DBSSLMode = servicecontract.Value("disable")
	s.Forwarders = servicecontract.Value(grpcsrv.TrustedForwarders{})
	msg := refuses(t, s, "KACHO_DEMO_AUTHZ_TRUSTED_FORWARDER_SANS", "no trust-any opt-in")
	if strings.Contains(msg, "to true") {
		t.Fatalf("отказ зовёт взвести опт-ин, которого у процесса нет:\n%s", msg)
	}
}

// TestNoOptIn_TwoStatementsAboutOneSubjectAreRefused — объявление «опт-ина
// нет» рядом с именем ручки опт-ина или испрошенным опт-ином — два утверждения
// об одном предмете; верно из них одно.
func TestNoOptIn_TwoStatementsAboutOneSubjectAreRefused(t *testing.T) {
	s := lawfulNoOptIn()
	s.ForwarderKnobs.TrustAny = "KACHO_DEMO_AUTHZ_TRUST_ANY_FORWARDER"
	refuses(t, s, "ForwarderKnobs", "NoOptInBecause")

	s = lawfulNoOptIn()
	s.Mode = servicecontract.ModeDev
	s.DBSSLMode = servicecontract.Value("disable")
	s.ForwarderKnobs.OptIn = true
	refuses(t, s, "ForwarderKnobs", "NoOptInBecause")
}

// TestNoOptIn_MissingOptInKnobWithoutReasonStillRefused — законная форма
// отсутствия ровно одна: причина. Пустое имя ручки опт-ина без причины
// остаётся отказом, как и было.
func TestNoOptIn_MissingOptInKnobWithoutReasonStillRefused(t *testing.T) {
	s := lawful()
	s.ForwarderKnobs.TrustAny = ""
	refuses(t, s, "ForwarderKnobs")
}
