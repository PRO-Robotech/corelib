// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

// authz_integration_test.go — сервер ленты за боевой цепочкой проверки
// прав (NTF1-C03 … C06): личность пира и круг пересылающих → звено
// идентичности служб (Р2) → проверка прав по карте, выведенной из аннотаций
// контракта, с объектом, к которому привязан сервер (З14). Решатель модели
// отвечает по посеянным кортежам.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/catalogderive"
	"github.com/PRO-Robotech/corelib/grpcsrv"
)

const (
	feedTrustDomain = "kacho.cloud"
	notifySAN       = "spiffe://kacho.cloud/ns/kacho/sa/kacho-notify"
	probeBSAN       = "spiffe://kacho.cloud/ns/kacho/sa/kacho-probe-b"
	strangerSAN     = "spiffe://kacho.cloud/ns/kacho/sa/kacho-stranger"
	gatewaySAN      = "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"
)

// tuples — посеянная модель: «субъект отношение объект». Записывает вопросы.
type tuples struct {
	allow map[string]bool
	mu    sync.Mutex
	asked []string
}

func (m *tuples) Check(_ context.Context, subject, relation, object string) (bool, error) {
	q := subject + " " + relation + " " + object
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, q)
	return m.allow[q], nil
}

func certPeer(t *testing.T, san string) context.Context {
	t.Helper()
	u, err := url.Parse(san)
	require.NoError(t, err)
	leaf := &x509.Certificate{URIs: []*url.URL{u}}
	st := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: st}})
}

// guarded — цепочка носителя над сервером ленты стенда. Кэш вердиктов свежий
// на каждую цепочку: отказ C05 судится без разрешения в кэше (УК22).
type guarded struct {
	chain []grpc.UnaryServerInterceptor
	model *tuples
	cache *authz.Cache
}

func guard(t *testing.T, model *tuples) *guarded {
	t.Helper()
	id, err := grpcsrv.NewServiceIdentity(
		[]string{
			notifyv1.InternalNotificationFeedService_Claim_FullMethodName,
			notifyv1.InternalNotificationFeedService_Ack_FullMethodName,
		},
		map[string]grpcsrv.ServiceName{notifySAN: "notify", probeBSAN: "probe-b"})
	require.NoError(t, err)
	derived, err := catalogderive.Derive("corelib.notify")
	require.NoError(t, err)
	bound, err := catalogderive.Bind(derived, map[string]string{"notification_feed": "probe"})
	require.NoError(t, err)
	cache := authz.NewCache(time.Minute)
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "probe", Map: bound, Client: model, Cache: cache,
	})
	chain := grpcsrv.PrincipalExtractUnary(grpcsrv.NewTrustDomain(feedTrustDomain), grpcsrv.NewTrustedForwarders(gatewaySAN))
	chain = append(chain, id.Unary(), intr.Unary())
	return &guarded{chain: chain, model: model, cache: cache}
}

func (g *guarded) claim(ctx context.Context, s *stand) (*notifyv1.ClaimResponse, error) {
	info := &grpc.UnaryServerInfo{FullMethod: notifyv1.InternalNotificationFeedService_Claim_FullMethodName}
	final := func(ctx context.Context, req any) (any, error) {
		return s.server.Claim(ctx, req.(*notifyv1.ClaimRequest))
	}
	h := grpc.UnaryHandler(final)
	for i := len(g.chain) - 1; i >= 0; i-- {
		link, next := g.chain[i], h
		h = func(ctx context.Context, req any) (any, error) { return link(ctx, req, info, next) }
	}
	resp, err := h(ctx, &notifyv1.ClaimRequest{Max: 10, Classes: []notifyv1.NotificationClass{notice}})
	if err != nil {
		return nil, err
	}
	return resp.(*notifyv1.ClaimResponse), nil
}

const readerTuple = "service:notify reader notification_feed:probe"

func wire(t *testing.T, err error) []byte {
	t.Helper()
	b, mErr := proto.Marshal(status.Convert(err).Proto())
	require.NoError(t, mErr)
	return b
}

func requireNotLeased(t *testing.T, s *stand, id string) {
	t.Helper()
	r := s.row(t, id)
	require.Empty(t, r.LeaseToken, "строка арендована отказанным вызовом")
	require.False(t, r.Claimed)
}

// NTF1-C03: service:notify с reader забирает строку; объект вопроса — лента,
// к которой привязан сервер.
func TestNTF1C03_NotifyWithReaderClaims(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), rcpt, hello())
	g := guard(t, &tuples{allow: map[string]bool{readerTuple: true}})
	resp, err := g.claim(certPeer(t, notifySAN), s)
	require.NoError(t, err)
	require.Equal(t, []string{id}, idsOf(resp.GetNotifications()))
	require.Equal(t, []string{readerTuple}, g.model.asked)
}

// NTF1-C04, C05, C06: отказы — PERMISSION_DENIED «permission denied», строка
// не арендована; ответ C05 побайтово равен C04; C05 судится без разрешения в
// кэше (УК22).
func TestNTF1C04C05C06_RefusalsLeaseNothing(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), rcpt, hello())

	gB := guard(t, &tuples{allow: map[string]bool{readerTuple: true}})
	_, errC04 := gB.claim(certPeer(t, probeBSAN), s)
	require.Equal(t, codes.PermissionDenied, status.Code(errC04), "%v", errC04)
	require.Equal(t, "permission denied", status.Convert(errC04).Message())
	require.Equal(t, []string{"service:probe-b reader notification_feed:probe"}, gB.model.asked)
	requireNotLeased(t, s, id)

	gN := guard(t, &tuples{allow: map[string]bool{}})
	require.Zero(t, gN.cache.Stats().Entries, "УК22: в кэше C05 нет разрешения")
	_, errC05 := gN.claim(certPeer(t, notifySAN), s)
	require.Equal(t, codes.PermissionDenied, status.Code(errC05), "%v", errC05)
	require.Equal(t, wire(t, errC04), wire(t, errC05), "C05 побайтово равен C04")
	require.Equal(t, []string{readerTuple}, gN.model.asked, "C05 спросил модель, а не кэш")
	requireNotLeased(t, s, id)

	gS := guard(t, &tuples{allow: map[string]bool{readerTuple: true}})
	_, errC06 := gS.claim(certPeer(t, strangerSAN), s)
	require.Equal(t, codes.PermissionDenied, status.Code(errC06), "%v", errC06)
	require.Equal(t, "permission denied", status.Convert(errC06).Message())
	requireNotLeased(t, s, id)

	// Близнец: следующий Claim notify с reader строку получает.
	resp, err := guard(t, &tuples{allow: map[string]bool{readerTuple: true}}).claim(certPeer(t, notifySAN), s)
	require.NoError(t, err)
	require.Equal(t, []string{id}, idsOf(resp.GetNotifications()))
}
