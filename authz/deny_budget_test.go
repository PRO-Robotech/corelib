// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// The per-principal budget exists to bound REFUSAL storms, and both the option's
// godoc and the text of the error it produces say so. These tests hold the code
// to that statement from both sides:
//
//   - an authorised caller must never be refused by it, however many distinct
//     objects it touches while the positive cache is cold;
//   - a refusal storm must still be cut off, and cut off BEFORE the permission
//     model is asked again — otherwise the budget protects nothing.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
)

// TestInterceptor_AuthorizedTrafficDoesNotSpendTheDenyBudget — an authorised
// caller walking distinct objects misses the positive cache on every call (the
// cache is keyed per object). If those misses were charged, a legitimate tenant
// would receive ResourceExhausted on requests it is entitled to — a refusal the
// error text would attribute to checks that were never refused.
func TestInterceptor_AuthorizedTrafficDoesNotSpendTheDenyBudget(t *testing.T) {
	checks := 0
	stub := authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
		checks++
		return true, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache:               authz.NewCache(0),
		Map:                 makeMap(),
		Client:              stub,
		DenyRateLimitPerSec: 2, // burst = 4 — deliberately far below the traffic below
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")

	const requests = 20
	for i := 0; i < requests; i++ {
		// A distinct object each time → never a cache hit.
		_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get",
			&fakeReq{id: fmt.Sprintf("enp_%02d", i)})
		if err != nil {
			t.Fatalf("request %d of %d: authorised caller refused with %v (code %v)",
				i+1, requests, err, status.Code(err))
		}
	}
	if checks != requests {
		t.Fatalf("expected every authorised request to reach the model, got %d of %d", checks, requests)
	}
	if m := intr.Metrics(); m.RateLimited != 0 {
		t.Fatalf("authorised traffic must not be rate-limited, got %d", m.RateLimited)
	}
}

// TestInterceptor_RefusalStormIsStillCutOff — the protection itself. Refusals are
// never cached, so each one costs a round-trip to the permission model; once the
// budget is gone the interceptor must answer without asking again.
func TestInterceptor_RefusalStormIsStillCutOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome func() (bool, error)
	}{
		{"denied", func() (bool, error) { return false, nil }},
		// A refused-because-hidden object is a refusal too, and equally uncached.
		{"hidden", func() (bool, error) { return false, authz.ErrHideExistence }},
		// A miss with no path is not cached either, so enumeration of absent ids
		// is the same storm wearing a different answer.
		{"no path", func() (bool, error) { return false, authz.ErrNoPath }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			stub := authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
				checks++
				return tc.outcome()
			})
			intr := authz.NewInterceptor(authz.InterceptorOptions{
				Cache:               authz.NewCache(0),
				Map:                 makeMap(),
				Client:              stub,
				DenyRateLimitPerSec: 5, // burst = 10
			})
			ctx := ctxWithPrincipal(t, "usr_eve", "user")

			exhausted := 0
			for i := 0; i < 40; i++ {
				_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get",
					&fakeReq{id: fmt.Sprintf("enp_%02d", i)})
				if status.Code(err) == codes.ResourceExhausted {
					exhausted++
				}
			}
			if exhausted == 0 {
				t.Fatalf("a storm of %q must be cut off; none of 40 were", tc.name)
			}
			if checks > 11 {
				t.Fatalf("the cut-off must precede the model question: %d questions asked for burst 10", checks)
			}
		})
	}
}

// TestInterceptor_BudgetSpentByRefusalsDoesNotBlockAnotherPrincipal — the budget
// is per principal; one caller's storm must not refuse another caller.
func TestInterceptor_BudgetSpentByRefusalsDoesNotBlockAnotherPrincipal(t *testing.T) {
	stub := authz.CheckClientFunc(func(_ context.Context, subject, _, _ string) (bool, error) {
		return subject == "user:usr_alice", nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache:               authz.NewCache(0),
		Map:                 makeMap(),
		Client:              stub,
		DenyRateLimitPerSec: 2, // burst = 4
	})

	eve := ctxWithPrincipal(t, "usr_eve", "user")
	for i := 0; i < 20; i++ {
		_, _ = runUnary(intr, eve, "/kacho.cloud.vpc.v1.NetworkService/Get",
			&fakeReq{id: fmt.Sprintf("enp_%02d", i)})
	}
	if m := intr.Metrics(); m.RateLimited == 0 {
		t.Fatalf("expected the refusing caller to be cut off")
	}

	alice := ctxWithPrincipal(t, "usr_alice", "user")
	for i := 0; i < 20; i++ {
		if _, err := runUnary(intr, alice, "/kacho.cloud.vpc.v1.NetworkService/Get",
			&fakeReq{id: fmt.Sprintf("enp_%02d", i)}); err != nil {
			t.Fatalf("authorised caller refused after another principal's storm: %v", err)
		}
	}
}

// TestInterceptor_DenyBudgetErrorSaysWhatWasSpent — the message is part of the
// contract an operator reads; it must not attribute the refusal to denials if
// something else spent the budget.
func TestInterceptor_DenyBudgetErrorSaysWhatWasSpent(t *testing.T) {
	stub := authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache:               authz.NewCache(0),
		Map:                 makeMap(),
		Client:              stub,
		DenyRateLimitPerSec: 1, // burst = 2
	})
	ctx := ctxWithPrincipal(t, "usr_eve", "user")

	var last error
	for i := 0; i < 10; i++ {
		_, last = runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get",
			&fakeReq{id: fmt.Sprintf("enp_%02d", i)})
	}
	if status.Code(last) != codes.ResourceExhausted {
		t.Fatalf("expected the storm to end in ResourceExhausted, got %v", last)
	}
	if got, want := status.Convert(last).Message(), "too many authorization checks; retry later"; got != want {
		t.Fatalf("refusal message = %q, want %q", got, want)
	}
}

// TestInterceptor_UnavailableModelSpendsTheBudget — a model that does not answer
// is the case where shedding load matters most, and it is not absorbed by the
// cache either. CheckTimeout bounds how long ONE call waits; only the budget
// bounds how often the call is made.
func TestInterceptor_UnavailableModelSpendsTheBudget(t *testing.T) {
	checks := 0
	stub := authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
		checks++
		return false, errors.New("permission model unreachable")
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache:               authz.NewCache(0),
		Map:                 makeMap(),
		Client:              stub,
		DenyRateLimitPerSec: 5, // burst = 10
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")

	exhausted := 0
	for i := 0; i < 40; i++ {
		_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get",
			&fakeReq{id: fmt.Sprintf("enp_%02d", i)})
		if status.Code(err) == codes.ResourceExhausted {
			exhausted++
		}
	}
	if exhausted == 0 {
		t.Fatalf("an outage must be shed after the budget is gone; 40 of 40 still reached the model")
	}
	if checks > 11 {
		t.Fatalf("shedding must precede the model call: %d calls made for burst 10", checks)
	}
}
