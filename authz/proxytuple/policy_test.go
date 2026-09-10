// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple

import (
	"errors"
	"testing"
)

// verdict — the two outcomes of the rule, as a table column. Named rather than a
// bare bool so a row reads as an assertion about the rule, not about a flag.
type verdict int

const (
	accepted verdict = iota
	refused
)

func (v verdict) String() string {
	if v == accepted {
		return "accepted"
	}
	return "refused"
}

// verdictOf classifies what ValidateTuple returned. An error that is NOT ErrRefused
// is not a refusal — it would be a bug in the rule, and collapsing it into "refused"
// would make that bug indistinguishable from correct behaviour.
func verdictOf(err error) verdict {
	switch {
	case err == nil:
		return accepted
	case errors.Is(err, ErrRefused):
		return refused
	default:
		panic("ValidateTuple returned an error that is not ErrRefused: " + err.Error())
	}
}

// TestValidateTuple проверяет least-privilege guard FGA-proxy write-path:
// модульная SA может писать только owner-hierarchy tuple в объект СВОЕГО домена,
// и никогда — privilege relation или платформенный/cluster объект.
func TestValidateTuple(t *testing.T) {
	tests := []struct {
		name         string
		callerDomain string
		subject      string
		relation     string
		object       string
		wantCode     verdict // accepted → ожидаем nil
	}{
		// Привилегия-эскалация: vpc-SA пытается выписать cluster-admin.
		{"vpc mints cluster system_admin", "vpc", "service_account:sva1", "system_admin", "cluster:cluster_root", refused},
		{"vpc mints cluster admin", "vpc", "service_account:sva1", "admin", "cluster:cluster_root", refused},
		// Privilege relation на своем же объекте — тоже запрещено (только hierarchy).
		{"vpc editor on own object", "vpc", "user:usr1", "editor", "vpc_network:net1", refused},
		{"vpc viewer on own object", "vpc", "user:usr1", "viewer", "vpc_network:net1", refused},
		{"vpc v_get on own object", "vpc", "user:usr1", "v_get", "vpc_network:net1", refused},
		{"vpc fga_writer on own object", "vpc", "service_account:sva1", "fga_writer", "vpc_network:net1", refused},
		// Foreign-domain object: vpc-SA пишет в iam/compute/nlb объект.
		{"vpc writes iam account object", "vpc", "user:usr1", "owner", "iam_account:acc1", refused},
		{"vpc writes account object", "vpc", "user:usr1", "owner", "account:acc1", refused},
		{"vpc writes compute object", "vpc", "user:usr1", "owner", "compute_instance:inst1", refused},
		{"vpc writes project object", "vpc", "project:prj1", "owner", "project:prj1", refused},
		// cluster object запрещен даже c hierarchy-relation и даже без известного домена (dev-mode).
		{"hierarchy relation but cluster object", "", "service_account:sva1", "project", "cluster:cluster_root", refused},
		// Легитимные owner-hierarchy tuple — проходят.
		{"vpc registers network under project", "vpc", "project:prj1", "project", "vpc_network:net1", accepted},
		{"compute registers instance under project", "compute", "project:prj1", "project", "compute_instance:inst1", accepted},
		// kacho-nlb владеет доменом loadbalancer, чьи FGA-object-типы после NLB-1a
		// префиксуются `nlb_` (nlb_network_load_balancer / nlb_listener /
		// nlb_target_group) — совпадают с SAN short-name "nlb" (domain-binding default).
		{"nlb registers listener under project", "nlb", "project:prj1", "project", "nlb_listener:lsn1", accepted},
		{"nlb registers load balancer under project", "nlb", "project:prj1", "project", "nlb_network_load_balancer:nlb1", accepted},
		{"nlb creator owner on load balancer", "nlb", "user:usr1", "owner", "nlb_network_load_balancer:nlb1", accepted},
		// nlb не вправе писать в чужой домен (vpc_*), даже с hierarchy-relation.
		{"nlb writes vpc object", "nlb", "user:usr1", "owner", "vpc_network:net1", refused},
		{"vpc creator owner tuple", "vpc", "user:usr1", "owner", "vpc_network:net1", accepted},
		// Empty inputs — InvalidArgument (грамматика проверяется отдельно, но guard fail-closed).
		{"empty relation", "vpc", "project:prj1", "", "vpc_network:net1", refused},
		{"empty object", "vpc", "project:prj1", "project", "", refused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTuple(tt.callerDomain, tt.subject, tt.relation, tt.object)
			gotCode := verdictOf(err)
			if tt.wantCode == accepted {
				if err != nil {
					t.Fatalf("ValidateTuple(%q,%q,%q,%q) = %v; want nil",
						tt.callerDomain, tt.subject, tt.relation, tt.object, err)
				}
				return
			}
			if gotCode != tt.wantCode {
				t.Fatalf("ValidateTuple(%q,%q,%q,%q) code = %v; want %v (err=%v)",
					tt.callerDomain, tt.subject, tt.relation, tt.object, gotCode, tt.wantCode, err)
			}
		})
	}
}
