// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package grpcsrv — acr.go: THE step-up (ACR / MFA-freshness) rule. Single
// implementation, two callers.
//
// The platform enforces the per-RPC catalog `required_acr_min` at TWO points:
//
//   - the public front door — api-gateway `middleware.StepUpGate.Check`
//     (RFC 9470 `401` + `WWW-Authenticate: acr_values`);
//   - the cluster-internal listener — kaname `authzguard.ACRFloor` (:9091,
//     gateway-fronted internal RPCs → `PERMISSION_DENIED` + step-up detail),
//     because the gateway re-dials :9091 and "internal = trusted" is a forbidden
//     assumption.
//
// Both points call EvaluateStepUp below. They do NOT re-derive the rule: neither
// keeps its own ranking table and neither keeps its own machine-principal
// exemption. Divergence is prevented BY CONSTRUCTION (there is one function),
// not by agreement between two copies — a re-introduced local override is caught
// by the verdict-parity guards on both sides (gateway
// stepup_verdict_parity_test.go, iam acr_floor_stepup_parity_test.go), which
// drive each REAL enforcement entrypoint — including the machine branch —
// against this function.
//
// WHY HERE: `gateway/internal/...` and `services/iam/internal/...` cannot import
// each other (Go internal-package rule), so the shared rule has to live under
// pkg/. It belongs in grpcsrv specifically because grpcsrv already owns every
// INPUT of the decision — the ACR ranking (ACRRank/ACRSatisfies), the trusted
// carriers the iam side reads (TrustedACRFromContext / TrustedPrincipalFromContext)
// and the metadata key contract (MDKeyTokenACR / MDKeyPrincipalType). Putting
// the rule anywhere else would split the decision from its own inputs, i.e.
// re-create the very second home this consolidation removes.
//
// ACR ordering (normative):
//
//	"" / "0" (anonymous)  <  "1" (password-only, AAL1)  <
//	"2" (phishing-resistant / MFA, AAL2)  <  "3" (hardware-bound UV passkey, AAL3)
//
// An unknown value ranks 0 (fail-closed). `required==""` (or "0") means NO
// requirement.
package grpcsrv

import (
	"time"

	"github.com/PRO-Robotech/corelib/principalwire"
)

// MDKeyTokenACR is the trusted metadata key carrying the validated JWT `acr`
// claim, forwarded by the api-gateway on the mTLS-verified gateway→iam re-dial
// (alongside x-kacho-principal-*). It is read ONLY under the trust invariant
// (see UnaryTrustedPrincipalExtract) — on an unverified peer it is dropped with
// the principal (anti-spoof).
// Имя ключа объявлено ОДИН раз — в `pkg/principalwire`; здесь оно только
// переименовано под привычное вызывающему имя (см. разбор у MDKeyPrincipalType).
const MDKeyTokenACR = principalwire.MetaTokenACR

// PrincipalTypeServiceAccount is the `kaname_principal_type` value identifying a
// MACHINE principal — the claim value stamped by the iam token-hook on a
// client_credentials service-account token, and the
// MDKeyPrincipalType metadata value the api-gateway forwards for it.
//
// It is the ONLY value that lifts the interactive-authentication floor (see
// EvaluateStepUp). `user`, `system`, an empty/absent type and any unknown value
// are NOT exempt (fail-closed).
const PrincipalTypeServiceAccount = "service_account"

// ACRRank maps an ACR string to a comparable integer. Unknown / malformed
// values resolve to 0 (anonymous) — fail-closed when policy expects ≥ 1.
//
// This is the single ranking table for the whole platform: both enforcement
// points reach it through EvaluateStepUp.
func ACRRank(acr string) int {
	switch acr {
	case "3":
		return 3
	case "2":
		return 2
	case "1":
		return 1
	case "0", "":
		return 0
	default:
		return 0
	}
}

// ACRSatisfies reports whether a presented acr meets a required floor.
//
//   - required == "" or "0" → no requirement → always true (no-op floor).
//   - otherwise → ACRRank(presented) >= ACRRank(required).
//
// An absent / unknown presented acr ranks 0, so it fails any positive floor
// (fail-closed).
//
// This is the ACR arm ONLY. Enforcement points must call EvaluateStepUp, which
// additionally applies the machine-principal exemption and the MFA-freshness
// arm; calling ACRSatisfies directly from an enforcement path re-creates half of
// the rule and is what the parity guards exist to catch.
func ACRSatisfies(presented, required string) bool {
	if ACRRank(required) == 0 {
		return true
	}
	return ACRRank(presented) >= ACRRank(required)
}

// StepUpInput — every input of the step-up decision. Both enforcement points
// build one of these and read nothing else.
//
// The caller supplies raw values from its own transport (a verified JWT's claims
// at the gateway; the trust-filtered ctx carriers at iam) — extracting them is
// transport plumbing and stays with the caller; DECIDING on them is this
// package's job.
type StepUpInput struct {
	// PrincipalType — the caller's `kaname_principal_type`
	// ("user" | "service_account" | "system"). MUST already be trust-filtered by
	// the caller: pass "" whenever the type came from an unverified peer, so a
	// forged `service_account` can never buy the exemption (anti-spoof).
	PrincipalType string
	// PresentedACR — the `acr` the caller actually authenticated with. Absent /
	// unknown ranks 0 (fail-closed). Like PrincipalType it must be trust-filtered.
	PresentedACR string
	// AuthTime — the token's `auth_time`. Consulted only when MFAMaxAge > 0.
	AuthTime time.Time
	// RequiredACR — the catalog `required_acr_min` for the RPC being called.
	// "" / "0" → no step-up requirement.
	RequiredACR string
	// MFAMaxAge — sliding freshness window on AuthTime. 0 → no freshness
	// requirement.
	MFAMaxAge time.Duration
	// Now — the evaluation instant. Required when MFAMaxAge > 0; a zero value
	// there falls back to time.Now() rather than computing a negative age that
	// would pass the window open (fail-closed defaulting).
	Now time.Time
}

// StepUpVerdict — the outcome of EvaluateStepUp. Deny reasons are distinguished
// so each enforcement point can emit its own protocol-appropriate error
// (RFC 6750 challenge at the gateway, gRPC status detail at iam) WITHOUT
// re-deciding anything.
type StepUpVerdict uint8

const (
	// StepUpAllow — the call may proceed past the step-up floor. Grants no
	// permission: the authorization Check runs independently and is unaffected.
	StepUpAllow StepUpVerdict = iota
	// StepUpDenyACR — presented acr ranks below the required floor.
	StepUpDenyACR
	// StepUpDenyAuthTimeMissing — a freshness window is required but the token
	// carries no auth_time.
	StepUpDenyAuthTimeMissing
	// StepUpDenyMFAStale — auth_time is older than the freshness window.
	StepUpDenyMFAStale
)

// EvaluateStepUp is THE step-up rule. Both enforcement points call it and
// neither may re-implement any arm of it.
//
// Arms, in order:
//
//  1. MACHINE-PRINCIPAL EXEMPTION. A service-account principal is exempt from
//     BOTH the acr floor and the MFA-freshness window. This is not a courtesy:
//     a machine has no interactive authentication ceremony and can NEVER present
//     acr ≥ 1 or a fresh auth_time, so gating it on assurance level does not
//     protect the RPC — it makes the RPC permanently unreachable for machines
//     (including the bootstrap-admin service account on the acr-gated
//     credential/grant RPCs). Expressing "machines must not call X" belongs in
//     the authorization MODEL as a relation, not in an assurance floor that no
//     machine can satisfy.
//
//     The exemption lifts ONLY the assurance floor. It grants no permission
//     whatsoever: the per-RPC authorization Check (FGA/ReBAC) runs independently
//     and is untouched, and the machine path carries its own controls
//     (credential lifetime, sender-constrained binding, narrow grant).
//
//     It is NARROW: exactly PrincipalTypeServiceAccount exempts. A `user`, a
//     `system` principal, an empty/absent type (which is also what a caller must
//     pass for an untrusted peer) and any unknown value are NOT exempt.
//
//  2. ACR FLOOR — ACRSatisfies(PresentedACR, RequiredACR).
//
//  3. MFA FRESHNESS — when MFAMaxAge > 0, AuthTime must exist and be within the
//     window.
func EvaluateStepUp(in StepUpInput) StepUpVerdict {
	// 1. Machine principal — exempt from the interactive-authentication floor.
	if in.PrincipalType == PrincipalTypeServiceAccount {
		return StepUpAllow
	}

	// 2. ACR floor.
	if !ACRSatisfies(in.PresentedACR, in.RequiredACR) {
		return StepUpDenyACR
	}

	// 3. MFA freshness.
	if in.MFAMaxAge > 0 {
		if in.AuthTime.IsZero() {
			return StepUpDenyAuthTimeMissing
		}
		now := in.Now
		if now.IsZero() {
			// Fail-closed defaulting: a zero Now would make every age negative and
			// silently hold the window open.
			now = time.Now()
		}
		if now.Sub(in.AuthTime) > in.MFAMaxAge {
			return StepUpDenyMFAStale
		}
	}

	return StepUpAllow
}
