// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"testing"
	"time"
)

// TestListenInvalidator_InvalidateBySubject — revoke propagation must drop the
// subject's Check-cache entries; a dropped dispatch means a revoked grant keeps
// being honoured from a stale cache.
func TestListenInvalidator_InvalidateBySubject(t *testing.T) {
	const subject = "user:usr_alice"
	cache := NewCache(5 * time.Second)
	cache.SetAllowed(subject, "viewer", "vpc_network", "enp_a")

	li := &ListenInvalidator{Cache: cache}
	li.invalidateBySubject(subject)

	if _, ok := cache.Get(subject, "viewer", "vpc_network", "enp_a"); ok {
		t.Fatalf("Check-cache entry survived revoke invalidation")
	}
}

// TestListenInvalidator_InvalidateAll — empty NOTIFY payload routes to
// invalidateAll.
func TestListenInvalidator_InvalidateAll(t *testing.T) {
	cache := NewCache(5 * time.Second)
	cache.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_a")
	cache.SetAllowed("user:usr_bob", "viewer", "vpc_network", "enp_b")

	li := &ListenInvalidator{Cache: cache}
	li.invalidateAll()

	if s, e := cache.Size(); s != 0 || e != 0 {
		t.Fatalf("Check-cache not fully cleared: subjects=%d entries=%d", s, e)
	}
}

// TestListenInvalidator_NilSafe — dispatch helpers must not panic when no cache
// is wired.
func TestListenInvalidator_NilSafe(t *testing.T) {
	(&ListenInvalidator{}).invalidateBySubject("user:usr_x") // nil — no panic
	(&ListenInvalidator{}).invalidateAll()

	cache := NewCache(5 * time.Second)
	cache.SetAllowed("user:usr_x", "viewer", "vpc_network", "enp_a")
	li := &ListenInvalidator{Cache: cache}
	li.invalidateBySubject("user:usr_x")
	if _, ok := cache.Get("user:usr_x", "viewer", "vpc_network", "enp_a"); ok {
		t.Fatalf("Cache-only invalidation did not drop entry")
	}
}

// TestListenInvalidator_Run_NilCacheErrors — Run must refuse to start with no
// cache to invalidate (misconfiguration guard).
func TestListenInvalidator_Run_NilCacheErrors(t *testing.T) {
	li := &ListenInvalidator{ConnString: "postgres://unused"}
	if err := li.Run(context.Background()); err == nil {
		t.Fatalf("expected error when Cache is nil")
	}
}
