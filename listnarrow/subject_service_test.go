// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listnarrow_test

// subject_service_test.go — субъект сужения зовёт ту же функцию, что звено прав
// (`authz.CallerSubject`, sec-one-predicate-three-readers): служба, опознанная
// звеном идентичности, сужает как `service:<имя>`; пересланный принципал решает
// по тенантскому словарю, как прежде.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/operations"
)

const (
	narrowNotifySAN = "spiffe://kacho.cloud/ns/kacho/sa/kacho-notify"
	narrowMethod    = "/corelib.subscription.InternalSubscriptionService/Subscribe"
)

func narrowServiceCtx(t *testing.T, base context.Context, method string) context.Context {
	t.Helper()
	u, err := url.Parse(narrowNotifySAN)
	if err != nil {
		t.Fatalf("SAN: %v", err)
	}
	leaf := &x509.Certificate{URIs: []*url.URL{u}}
	ctx := peer.NewContext(base, &peer.Peer{AuthInfo: credentials.TLSInfo{
		State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf}}},
	}})
	id, err := grpcsrv.NewServiceIdentity([]string{narrowMethod},
		map[string]grpcsrv.ServiceName{narrowNotifySAN: "notify"})
	if err != nil {
		t.Fatalf("звено: %v", err)
	}
	var out context.Context
	_, err = id.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(c context.Context, _ any) (any, error) { out = c; return nil, nil })
	if err != nil {
		t.Fatalf("звено: %v", err)
	}
	return out
}

func TestSubjectFromContextNamesTheServiceTheLinkRecognized(t *testing.T) {
	got, err := listnarrow.SubjectFromContext(narrowServiceCtx(t, context.Background(), narrowMethod))
	if err != nil || got != "service:notify" {
		t.Fatalf("субъект сужения = %q, %v; ожидалось service:notify", got, err)
	}
}

func TestSubjectFromContextStaysUnnamedOutsideTheList(t *testing.T) {
	_, err := listnarrow.SubjectFromContext(narrowServiceCtx(t, context.Background(), "/x.v1.Y/Z"))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("метод вне перечня: ожидался отказ «никого», получено %v", err)
	}
}

func TestSubjectFromContextLetsTheForwardedPrincipalDecide(t *testing.T) {
	t.Run("пересланный пользователь — пользователь, не служба", func(t *testing.T) {
		base := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: "usr_u"})
		got, err := listnarrow.SubjectFromContext(narrowServiceCtx(t, base, narrowMethod))
		if err != nil || got != "user:usr_u" {
			t.Fatalf("субъект = %q, %v; ожидался user:usr_u", got, err)
		}
	})
	t.Run("пересланный тип вне тенантского словаря — никого, сертификату не уступает", func(t *testing.T) {
		for _, typ := range []string{"system", "service", "group"} {
			base := operations.WithPrincipal(context.Background(), operations.Principal{Type: typ, ID: "notify"})
			if got, err := listnarrow.SubjectFromContext(narrowServiceCtx(t, base, narrowMethod)); err == nil {
				t.Fatalf("пересланный %q дал субъект %q", typ, got)
			}
		}
	})
}
