// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// stepup_rule_test.go — direct unit cover of THE step-up rule
// (grpcsrv.EvaluateStepUp), the single implementation both enforcement points
// call. The two verdict-parity guards
// (gateway/internal/middleware/stepup_verdict_parity_test.go and
// services/iam/internal/authzguard/acr_floor_stepup_parity_test.go) pin each
// REAL entrypoint to this function; these cases pin the function itself.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

var ruleNow = time.Unix(1_700_000_000, 0)

// The machine exemption lifts BOTH arms — a service account has neither an acr
// nor an auth_time and can never acquire either.
func TestEvaluateStepUp_MachinePrincipal_ExemptFromBothArms(t *testing.T) {
	require.Equal(t, grpcsrv.StepUpAllow, grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{
		PrincipalType: grpcsrv.PrincipalTypeServiceAccount,
		PresentedACR:  "", // ranks 0
		RequiredACR:   "3",
		MFAMaxAge:     time.Hour,
		AuthTime:      time.Time{}, // absent
		Now:           ruleNow,
	}))
}

// NARROWNESS: only an exact `service_account` exempts. Everything else — human,
// system, absent, case variants, near-misses — is held to the floor.
func TestEvaluateStepUp_ExemptionIsExactMatchOnly(t *testing.T) {
	for _, pType := range []string{
		"", "user", "system", "Service_Account", "SERVICE_ACCOUNT",
		"service_account ", " service_account", "serviceaccount", "service-account",
	} {
		require.Equalf(t, grpcsrv.StepUpDenyACR, grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{
			PrincipalType: pType,
			PresentedACR:  "",
			RequiredACR:   "2",
			Now:           ruleNow,
		}), "principal_type=%q must NOT be exempt", pType)
	}
}

func TestEvaluateStepUp_ACRArm(t *testing.T) {
	t.Run("no requirement passes any acr", func(t *testing.T) {
		for _, required := range []string{"", "0"} {
			for _, presented := range []string{"", "0", "1", "3", "garbage"} {
				require.Equal(t, grpcsrv.StepUpAllow, grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{
					PrincipalType: "user", PresentedACR: presented, RequiredACR: required, Now: ruleNow,
				}))
			}
		}
	})
	t.Run("at or above floor passes", func(t *testing.T) {
		for _, presented := range []string{"2", "3"} {
			require.Equal(t, grpcsrv.StepUpAllow, grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{
				PrincipalType: "user", PresentedACR: presented, RequiredACR: "2", Now: ruleNow,
			}))
		}
	})
	t.Run("below floor and unknown deny (fail-closed)", func(t *testing.T) {
		for _, presented := range []string{"", "0", "1", "garbage"} {
			require.Equal(t, grpcsrv.StepUpDenyACR, grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{
				PrincipalType: "user", PresentedACR: presented, RequiredACR: "2", Now: ruleNow,
			}))
		}
	})
}

func TestEvaluateStepUp_FreshnessArm(t *testing.T) {
	base := grpcsrv.StepUpInput{PrincipalType: "user", PresentedACR: "3", RequiredACR: "3", Now: ruleNow}

	t.Run("no window ignores auth_time entirely", func(t *testing.T) {
		in := base
		in.AuthTime = ruleNow.Add(-100 * time.Hour)
		require.Equal(t, grpcsrv.StepUpAllow, grpcsrv.EvaluateStepUp(in))
	})
	t.Run("fresh within window passes", func(t *testing.T) {
		in := base
		in.MFAMaxAge = time.Hour
		in.AuthTime = ruleNow.Add(-time.Minute)
		require.Equal(t, grpcsrv.StepUpAllow, grpcsrv.EvaluateStepUp(in))
	})
	t.Run("older than window is stale", func(t *testing.T) {
		in := base
		in.MFAMaxAge = time.Hour
		in.AuthTime = ruleNow.Add(-2 * time.Hour)
		require.Equal(t, grpcsrv.StepUpDenyMFAStale, grpcsrv.EvaluateStepUp(in))
	})
	t.Run("window required but auth_time absent", func(t *testing.T) {
		in := base
		in.MFAMaxAge = time.Hour
		require.Equal(t, grpcsrv.StepUpDenyAuthTimeMissing, grpcsrv.EvaluateStepUp(in))
	})
	t.Run("acr arm is evaluated before freshness", func(t *testing.T) {
		in := base
		in.PresentedACR = "1"
		in.MFAMaxAge = time.Hour
		require.Equal(t, grpcsrv.StepUpDenyACR, grpcsrv.EvaluateStepUp(in),
			"an insufficient acr must report the acr reason, not a freshness reason")
	})
}

// A zero Now with a live window must NOT hold the window open. Without the
// fail-closed default the age would come out hugely negative and every stale
// token would pass.
func TestEvaluateStepUp_ZeroNow_DoesNotHoldWindowOpen(t *testing.T) {
	require.Equal(t, grpcsrv.StepUpDenyMFAStale, grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{
		PrincipalType: "user",
		PresentedACR:  "3",
		RequiredACR:   "3",
		MFAMaxAge:     time.Hour,
		AuthTime:      time.Unix(1, 0), // ancient
		// Now deliberately zero.
	}))
}
