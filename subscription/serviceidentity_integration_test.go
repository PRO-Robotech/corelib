// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

// serviceidentity_integration_test.go — сервер подписки за звеном идентичности
// служб (NTF-1 Р2, NTF1-M01, M03, M04, M05). Контекст вызывающего — пир с
// сертификатом и заголовками личности; за подстановкой стоят те же звенья, что
// в боевой цепочке носителя: извлечение личности с кругом пересылающих, затем
// звено идентичности. Субъект сужения читает решатель модели — проба утверждает
// то, что он спросил, и то, что приехало в поток.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/corelib/subscription"
	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/listnarrow/narrowtest"
)

const (
	sidDomain     = "kacho.cloud"
	sidNotifySAN  = "spiffe://kacho.cloud/ns/kacho/sa/kacho-notify"
	sidProbeBSAN  = "spiffe://kacho.cloud/ns/kacho/sa/kacho-probe-b"
	sidGatewaySAN = "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"
	sidObjectO    = "net0000000000000000o"
)

// subjectPeer — решатель модели, отвечающий ПО СУБЪЕКТУ: право на объект есть у
// перечисленных субъектов и больше ни у кого. Записывает, от кого спрашивали.
type subjectPeer struct {
	allow map[string]map[string]bool // субъект → объект → право

	mu    sync.Mutex
	asked []string
}

func (p *subjectPeer) BatchCheck(_ context.Context, checks []listnarrow.Check) ([]bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]bool, 0, len(checks))
	for _, c := range checks {
		p.asked = append(p.asked, c.Subject)
		out = append(out, p.allow[c.Subject][c.ResourceID])
	}
	return out, nil
}

func (p *subjectPeer) subjects() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	for _, s := range p.asked {
		seen[s] = true
	}
	return seen
}

func sidPeer(t *testing.T, verified bool, sans ...string) context.Context {
	t.Helper()
	leaf := &x509.Certificate{}
	for _, s := range sans {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatalf("SAN %q: %v", s, err)
		}
		leaf.URIs = append(leaf.URIs, u)
	}
	st := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if verified {
		st.VerifiedChains = [][]*x509.Certificate{{leaf}}
	}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: st}})
}

func withUserHeaders(ctx context.Context, id string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, id,
	))
}

// stepUpLog — пол acr, вынесенный общей EvaluateStepUp вызывающему, которого
// назвала одна функция субъекта.
type stepUpLog struct {
	mu       sync.Mutex
	verdicts []grpcsrv.StepUpVerdict
}

func (l *stepUpLog) link() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if c, ok := authz.CallerSubject(ss.Context()); ok {
			l.mu.Lock()
			l.verdicts = append(l.verdicts, grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{
				PrincipalType: c.StepUpType(), RequiredACR: "2",
			}))
			l.mu.Unlock()
		}
		return h(srv, ss)
	}
}

// identityLinks — боевой порядок: личность пира → переданная личность, сужённая
// кругом → звено идентичности служб. Таблица знает notify и probe-b; перечень —
// только подписку.
func identityLinks(t *testing.T, extra ...grpc.StreamServerInterceptor) []grpc.StreamServerInterceptor {
	t.Helper()
	id, err := grpcsrv.NewServiceIdentity(
		[]string{subscriptionv1.InternalSubscriptionService_Subscribe_FullMethodName},
		map[string]grpcsrv.ServiceName{sidNotifySAN: "notify", sidProbeBSAN: "probe-b"})
	if err != nil {
		t.Fatalf("звено: %v", err)
	}
	links := grpcsrv.PrincipalExtractStream(grpcsrv.NewTrustDomain(sidDomain),
		grpcsrv.NewTrustedForwarders(sidGatewaySAN))
	links = append(links, id.Stream())
	return append(links, extra...)
}

func beginning() *subscriptionv1.SubscriptionRequest {
	return &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING},
	}
}

// refusedAtOpen — поток отвергнут до служебного сообщения; отдаёт статус отказа.
func refusedAtOpen(t *testing.T, s *stand) *status.Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	strm, err := s.client.Subscribe(ctx, beginning())
	if err != nil {
		return status.Convert(err)
	}
	msg, err := strm.Recv()
	if err == nil {
		t.Fatalf("поток открыт (%T), а обязан быть отвергнут", msg.GetMessage())
	}
	return status.Convert(err)
}

// NTF1-M01 — точный SAN на методе перечня даёт `service:notify` и открывает
// подписку; пол acr вынесен общей функцией как машинному принципалу.
func TestNTF1M01_ExactSANOnAListedMethodOpensTheStreamAsTheService(t *testing.T) {
	model := &subjectPeer{allow: map[string]map[string]bool{"service:notify": {sidObjectO: true}}}
	var acr stepUpLog
	s := newStand(t, standOpts{
		narrower: narrowtest.New(model),
		caller:   sidPeer(t, true, sidNotifySAN),
		links:    identityLinks(t, acr.link()),
	})
	s.emit(t, "Network", sidObjectO, "CREATED", "prj-a")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got := recvEvents(t, s.open(t, ctx, beginning()), 1)
	if got[0].GetResourceId() != sidObjectO {
		t.Fatalf("пришло событие %q", got[0].GetResourceId())
	}
	if seen := model.subjects(); len(seen) != 1 || !seen["service:notify"] {
		t.Fatalf("субъект сужения = %v, ожидался только service:notify", seen)
	}
	acr.mu.Lock()
	defer acr.mu.Unlock()
	if len(acr.verdicts) != 1 || acr.verdicts[0] != grpcsrv.StepUpAllow {
		t.Fatalf("пол acr вынесен %v — служба обязана оцениваться как машинный принципал", acr.verdicts)
	}
}

// NTF1-M03 — сравнение SAN — равенство полного URI. Каждый почти-ключ —
// PERMISSION_DENIED «subscription requires an authenticated caller»; равный
// ключу SAN в той же пробе открывает поток.
func TestNTF1M03_SANComparisonIsFullURIEquality(t *testing.T) {
	for _, san := range []string{
		"spiffe://other.cloud/ns/kacho/sa/kacho-notify",
		"spiffe://kacho.cloud/ns/other/sa/kacho-notify",
		"spiffe://kacho.cloud/ns/kacho/sa/kacho-notifyer",
		sidNotifySAN + "/tail",
	} {
		t.Run(san, func(t *testing.T) {
			s := newStand(t, standOpts{
				narrower: narrowtest.New(&subjectPeer{}),
				caller:   sidPeer(t, true, san),
				links:    identityLinks(t),
			})
			st := refusedAtOpen(t, s)
			if st.Code() != codes.PermissionDenied || st.Message() != "subscription requires an authenticated caller" {
				t.Fatalf("почти-ключ %q: получено %v «%s»", san, st.Code(), st.Message())
			}
		})
	}
	t.Run("равный ключу — поток открыт", func(t *testing.T) {
		s := newStand(t, standOpts{
			narrower: narrowtest.New(&subjectPeer{}),
			caller:   sidPeer(t, true, sidNotifySAN),
			links:    identityLinks(t),
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.open(t, ctx, beginning())
	})
}

// NTF1-M04 — пересланная личность без проверенного сертификата — отказ; тот же
// запрос с проверенным сертификатом из таблицы открывает поток как служба.
func TestNTF1M04_UnverifiedPeerWithIdentityHeadersIsRefused(t *testing.T) {
	s := newStand(t, standOpts{
		narrower: narrowtest.New(&subjectPeer{}),
		caller:   withUserHeaders(sidPeer(t, false, sidNotifySAN), "usr_u"),
		links:    identityLinks(t),
	})
	st := refusedAtOpen(t, s)
	if st.Code() != codes.PermissionDenied || st.Message() != "subscription requires an authenticated caller" {
		t.Fatalf("непроверенный пир: получено %v «%s»", st.Code(), st.Message())
	}

	model := &subjectPeer{allow: map[string]map[string]bool{"service:notify": {sidObjectO: true}}}
	twin := newStand(t, standOpts{
		narrower: narrowtest.New(model),
		caller:   sidPeer(t, true, sidNotifySAN),
		links:    identityLinks(t),
	})
	twin.emit(t, "Network", sidObjectO, "CREATED", "prj-a")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = recvEvents(t, twin.open(t, ctx, beginning()), 1)
	if seen := model.subjects(); !seen["service:notify"] {
		t.Fatalf("проверенный пир из таблицы сужается не как service:notify: %v", seen)
	}
}

// NTF1-M05 — пересланный принципал решает; вне круга пересылка снимается.
// probe-b (вне круга) с заголовками пользователя U: пересылка снята, субъект —
// service:probe-b, событий по O нет. Те же заголовки от края — субъект user:U,
// событие по O приходит.
func TestNTF1M05_ForwardedPrincipalDecidesAndIsDroppedOutsideTheCircle(t *testing.T) {
	allow := map[string]map[string]bool{"user:usr_u": {sidObjectO: true}}

	t.Run("пир вне круга — субъект из сертификата, событий по O нет", func(t *testing.T) {
		model := &subjectPeer{allow: allow}
		s := newStand(t, standOpts{
			narrower: narrowtest.New(model),
			caller:   withUserHeaders(sidPeer(t, true, sidProbeBSAN), "usr_u"),
			links:    identityLinks(t),
		})
		s.emit(t, "Network", sidObjectO, "CREATED", "prj-a")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sb := s.open(t, ctx, beginning())
		requireQuiet(t, sb)
		if seen := model.subjects(); len(seen) != 1 || !seen["service:probe-b"] {
			t.Fatalf("субъект сужения = %v, ожидался только service:probe-b", seen)
		}
	})

	t.Run("край из круга — субъект user:U, событие по O приходит", func(t *testing.T) {
		model := &subjectPeer{allow: allow}
		s := newStand(t, standOpts{
			narrower: narrowtest.New(model),
			caller:   withUserHeaders(sidPeer(t, true, sidGatewaySAN), "usr_u"),
			links:    identityLinks(t),
		})
		s.emit(t, "Network", sidObjectO, "CREATED", "prj-a")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		got := recvEvents(t, s.open(t, ctx, beginning()), 1)
		if got[0].GetResourceId() != sidObjectO {
			t.Fatalf("пришло событие %q", got[0].GetResourceId())
		}
		if seen := model.subjects(); len(seen) != 1 || !seen["user:usr_u"] {
			t.Fatalf("субъект сужения = %v, ожидался только user:usr_u — служебный смешан с пересланным", seen)
		}
	})
}
