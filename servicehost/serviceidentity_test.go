// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// serviceidentity_test.go — звено идентичности служб в носителе (NTF-1 Р2, З13,
// CX1-34): звено стоит в `unaryChain` и `streamChain` рядом с извлечением
// личности и до слота решения о доступе, без ветки по слушателю; оба слушателя,
// собранные `serverPair`, несут его наблюдаемо; метод перечня вне каталога прав —
// отказ старта (NTF1-M09 (в)); служебный субъект не становится принципалом
// операций (NTF1-M06).
package servicehost

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

const (
	hostNotifySAN = "spiffe://kacho.cloud/ns/kacho/sa/kacho-notify"
	hostStraySAN  = "spiffe://kacho.cloud/ns/kacho/sa/kacho-stray"
	widgetGet     = "/kacho.cloud.demo.v1.WidgetService/Get"
	widgetList    = "/kacho.cloud.demo.v1.WidgetService/List"
)

func hostIdentity(t *testing.T, methods ...string) grpcsrv.ServiceIdentity {
	t.Helper()
	id, err := grpcsrv.NewServiceIdentity(methods, map[string]grpcsrv.ServiceName{hostNotifySAN: "notify"})
	if err != nil {
		t.Fatalf("звено: %v", err)
	}
	return id
}

// ── NTF1-M09 (в): метод перечня обязан быть в каталоге прав процесса ────────

func TestServiceIdentityMethodOutsideTheCatalogRefusesStart(t *testing.T) {
	s := naAxes()
	s.ServiceIdentity = servicecontract.Value(hostIdentity(t, "/kacho.cloud.demo.v1.WidgetService/Delete"))
	refusesAudit(t, s, lawfulServed(), lawfulCatalog(), lawfulMap(),
		"ServiceIdentity", "/kacho.cloud.demo.v1.WidgetService/Delete", "каталог")
}

func TestServiceIdentityMethodInTheCatalogIsAcceptedAndCounted(t *testing.T) {
	s := naAxes()
	s.ServiceIdentity = servicecontract.Value(hostIdentity(t, widgetGet, widgetList))
	c, err := audit(s, lawfulServed(), lawfulCatalog(), lawfulMap())
	if err != nil {
		t.Fatalf("перечень внутри каталога отвергнут: %v", err)
	}
	if c.identityMethods != 2 {
		t.Fatalf("перепись не назвала методов перечня звена: %+v", c)
	}
	t.Logf("перепись: %s", c)

	s.ServiceIdentity = servicecontract.NotApplicable[grpcsrv.ServiceIdentity]("служб-подписчиков нет")
	c, err = audit(s, lawfulServed(), lawfulCatalog(), lawfulMap())
	if err != nil || c.identityMethods != 0 {
		t.Fatalf("изъятие: err=%v, перепись %+v", err, c)
	}
}

// ── позиция звена в цепочке ────────────────────────────────────────────────

// recordingSlot — слот решения, чей решатель записывает субъекта вопроса и
// разрешает. Предмет проб — что ДОЕЗЖАЕТ до решения, а не как оно принимается.
func recordingSlot(t *testing.T, asked *subjectLog) *decisionSlot {
	t.Helper()
	var objects atomic.Int64
	var slot decisionSlot
	slot.install(authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-demo",
		Cache:       authz.NewCache(0),
		Map: authz.RPCMap{
			// Объект у каждого вопроса свой: положительный вердикт кэшируется, и
			// второй вопрос о том же объекте до модели не дошёл бы — проба мерила
			// бы кэш, а не звено.
			widgetGet: {Relation: "viewer", Extract: authz.StaticExtractor("widget",
				func(any) (string, error) { return fmt.Sprintf("w%d", objects.Add(1)), nil })},
		},
		Client: authz.CheckClientFunc(func(_ context.Context, subject, _, _ string) (bool, error) {
			asked.add(subject)
			return true, nil
		}),
	}))
	return &slot
}

type subjectLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *subjectLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, s)
}

func (l *subjectLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

type hostCtxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s hostCtxStream) Context() context.Context { return s.ctx }

// Звено стоит в ОБЕИХ цепочках, после извлечения личности и до слота решения:
// решатель спрашивает модель от `service:notify`, и так на каждом слушателе.
func TestCarrierChainsCarryTheServiceIdentityLinkBeforeTheDecision(t *testing.T) {
	spec := chainSpec()
	spec.ServiceIdentity = servicecontract.Value(hostIdentity(t, widgetGet))
	for _, on := range []grpcsrv.Listener{grpcsrv.ListenerPublic, grpcsrv.ListenerInternal} {
		var asked subjectLog
		slot := recordingSlot(t, &asked)
		unary := chainUnaryServer(unaryChain(spec, slot, probeLatency(t), nil, on)...)
		if _, err := unary(verifiedPeerCtx(t, hostNotifySAN), nil, &grpc.UnaryServerInfo{FullMethod: widgetGet},
			func(context.Context, any) (any, error) { return nil, nil }); err != nil {
			t.Fatalf("%v unary: служебный вызов отвергнут: %v", on, err)
		}

		stream := streamChain(spec, slot, probeLatency(t), nil, on)
		var h grpc.StreamHandler = func(any, grpc.ServerStream) error { return nil }
		for i := len(stream) - 1; i >= 0; i-- {
			link, next := stream[i], h
			h = func(srv any, ss grpc.ServerStream) error {
				return link(srv, ss, &grpc.StreamServerInfo{FullMethod: widgetGet, IsServerStream: true}, next)
			}
		}
		if err := h(nil, hostCtxStream{ctx: verifiedPeerCtx(t, hostNotifySAN)}); err != nil {
			t.Fatalf("%v stream: служебный вызов отвергнут: %v", on, err)
		}
		if got := asked.all(); len(got) != 2 || got[0] != "service:notify" || got[1] != "service:notify" {
			t.Fatalf("%v: модель спрошена от %v, ожидалось service:notify на обеих полосах", on, got)
		}
	}
}

// Близнец: у изъятой оси звено не опознаёт никого, и вызов того же пира
// отвергается звеном прав как прежде — «до Р2».
func TestCarrierChainWithoutAServiceIdentityRecognizesNobody(t *testing.T) {
	spec := chainSpec()
	spec.ServiceIdentity = servicecontract.NotApplicable[grpcsrv.ServiceIdentity]("служб-подписчиков нет")
	var asked subjectLog
	slot := recordingSlot(t, &asked)
	unary := chainUnaryServer(unaryChain(spec, slot, probeLatency(t), nil, grpcsrv.ListenerInternal)...)
	_, err := unary(verifiedPeerCtx(t, hostNotifySAN), nil, &grpc.UnaryServerInfo{FullMethod: widgetGet},
		func(context.Context, any) (any, error) { return nil, nil })
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("без звена пир без пересланной личности обязан быть отвергнут, получено %v", err)
	}
	if got := asked.all(); len(got) != 0 {
		t.Fatalf("модель спрошена от %v без субъекта", got)
	}
}

// ── наблюдаемо на проводе: оба слушателя, собранные serverPair ──────────────

type pki struct {
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	pool   *x509.CertPool
	server tls.Certificate
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ключ корня: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "servicehost-probe-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("корень: %v", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("корень: %v", err)
	}
	p := &pki{ca: ca, caKey: key, pool: x509.NewCertPool()}
	p.pool.AddCert(ca)
	p.server = p.leaf(t, "", true)
	return p
}

func (p *pki) leaf(t *testing.T, san string, server bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ключ листа: %v", err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatalf("серийный номер: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "servicehost-probe-leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		u, uerr := url.Parse(san)
		if uerr != nil {
			t.Fatalf("SAN: %v", uerr)
		}
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatalf("лист: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (p *pki) serverCreds() credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{p.server},
		ClientCAs:    p.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	})
}

// callWithCert поднимает сервер на эфемерном порту и зовёт демо-метод
// клиентским сертификатом с переданным SAN.
func callWithCert(t *testing.T, srv *grpc.Server, p *pki, san string) codes.Code {
	t.Helper()
	srv.RegisterService(&demoServiceDesc, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушатель: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{p.leaf(t, san, false)},
		RootCAs:      p.pool,
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	})
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(client))
	if err != nil {
		t.Fatalf("клиент: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out emptypb.Empty
	return status.Code(conn.Invoke(ctx, widgetGet, &emptypb.Empty{}, &out))
}

// TestBothListenersCarryTheServiceIdentityLinkOnTheWire — законный близнец
// `TestBothListenersRefuseIdenticallyOnTheWire` для звена идентичности: оба
// сервера, собранные `serverPair`, опознают службу по сертификату на проводе, и
// модель спрашивается от `service:notify` на каждом. Сертификат вне таблицы —
// одинаковый отказ на обоих.
func TestBothListenersCarryTheServiceIdentityLinkOnTheWire(t *testing.T) {
	p := newPKI(t)
	pair := func(asked *subjectLog) (*grpc.Server, *grpc.Server) {
		spec := chainSpec()
		spec.PublicCreds = p.serverCreds()
		spec.InternalCreds = p.serverCreds()
		spec.Metrics = prometheus.NewRegistry()
		spec.ServiceIdentity = servicecontract.Value(hostIdentity(t, widgetGet))
		public, internal, err := serverPair(spec, recordingSlot(t, asked))
		if err != nil {
			t.Fatalf("пара слушателей: %v", err)
		}
		return public, internal
	}

	var asked subjectLog
	public, internal := pair(&asked)
	pub, intl := callWithCert(t, public, p, hostNotifySAN), callWithCert(t, internal, p, hostNotifySAN)
	if pub != codes.OK || intl != codes.OK {
		t.Fatalf("служба из таблицы: публичный %v, внутренний %v — звено не на обоих слушателях", pub, intl)
	}
	if got := asked.all(); len(got) != 2 || got[0] != "service:notify" || got[1] != "service:notify" {
		t.Fatalf("модель спрошена от %v, ожидалось service:notify на каждом слушателе", got)
	}

	var strayAsked subjectLog
	public, internal = pair(&strayAsked)
	pub, intl = callWithCert(t, public, p, hostStraySAN), callWithCert(t, internal, p, hostStraySAN)
	if pub != intl || pub != codes.PermissionDenied {
		t.Fatalf("сертификат вне таблицы: публичный %v, внутренний %v — ожидался одинаковый PERMISSION_DENIED", pub, intl)
	}
	if got := strayAsked.all(); len(got) != 0 {
		t.Fatalf("модель спрошена без субъекта: %v", got)
	}
}

// ── NTF1-M06: служебный принципал не владелец операций ──────────────────────

// opsGet — метод операций. Как и у владельцев, он в карте прав `Public`:
// авторизуется на уровне данных — владельцем из носителя операций.
const opsGet = "/kacho.cloud.operation.v1.OperationService/Get"

// opsObservation — что увидел обработчик по форме `OperationService.Get`.
type opsObservation struct {
	subject      string
	hasSubject   bool
	hasPrincipal bool
}

// operationsGet — обработчик по форме `OperationService.Get` владельца: владелец
// операции берётся ТОЛЬКО из носителя операций, и «принципала нет» отвечает той
// же формой, что и чужая операция.
func operationsGet(recorded map[string]operations.Principal, seen *opsObservation) grpc.UnaryHandler {
	return func(ctx context.Context, req any) (any, error) {
		id, _ := req.(string)
		c, ok := authz.CallerSubject(ctx)
		seen.subject, seen.hasSubject = c.Subject(), ok
		_, seen.hasPrincipal = operations.PrincipalFromContextOK(ctx)
		owner, ok := operations.OwnerFromContext(ctx)
		if !ok {
			return nil, operations.NotFoundStatus(id)
		}
		rec, known := recorded[id]
		if !known || operations.OwnerFromPrincipal(rec) != owner {
			return nil, operations.NotFoundStatus(id)
		}
		return "op:" + id, nil
	}
}

func wireBytes(t *testing.T, err error) []byte {
	t.Helper()
	b, merr := proto.Marshal(status.Convert(err).Proto())
	if merr != nil {
		t.Fatalf("сериализация статуса: %v", merr)
	}
	return b
}

func TestServicePrincipalOwnsNoOperationEvenWhenTheMethodIsListed(t *testing.T) {
	recorded := map[string]operations.Principal{
		"op-s": operations.SystemPrincipal(),
		"op-u": {Type: "user", ID: "usr_u"},
	}
	spec := chainSpec()
	spec.ServiceIdentity = servicecontract.Value(hostIdentity(t, opsGet))
	var slot decisionSlot
	slot.install(authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-demo",
		Cache:       authz.NewCache(0),
		Map:         authz.RPCMap{opsGet: {Public: true}},
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return false, errors.New("проба: вопрос модели на методе уровня данных не задаётся")
		}),
	}))
	chain := chainUnaryServer(unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerInternal)...)
	call := func(ctx context.Context, id string) (any, error, opsObservation) {
		var seen opsObservation
		resp, err := chain(ctx, id, &grpc.UnaryServerInfo{FullMethod: opsGet}, operationsGet(recorded, &seen))
		return resp, err, seen
	}

	for _, id := range []string{"op-s", "op-u"} {
		resp, err, seen := call(verifiedPeerCtx(t, hostNotifySAN), id)
		if !seen.hasSubject || seen.subject != "service:notify" {
			t.Fatalf("%s: субъект звена прав = %q (%v), ожидалось service:notify — метод в перечне",
				id, seen.subject, seen.hasSubject)
		}
		if seen.hasPrincipal {
			t.Fatalf("%s: обработчик увидел принципала операций у службы", id)
		}
		if resp != nil || status.Code(err) != codes.NotFound {
			t.Fatalf("%s: операция отдана службе: %v %v", id, resp, err)
		}
		// Близнец по ответу: сертификат вне таблицы. Ответы обязаны совпасть
		// побайтово — служба в перечне не получает от операций ничего сверх того,
		// что получает никто.
		strayResp, strayErr, straySeen := call(verifiedPeerCtx(t, hostStraySAN), id)
		if straySeen.hasSubject {
			t.Fatalf("%s: сертификат вне таблицы дал субъект %q", id, straySeen.subject)
		}
		if strayResp != nil || string(wireBytes(t, err)) != string(wireBytes(t, strayErr)) {
			t.Fatalf("%s: ответ службе (%v) расходится с ответом сертификату вне таблицы (%v)", id, err, strayErr)
		}
	}

	// Законный близнец: пользователь U, пересланный краем, получает свою операцию.
	resp, err, seen := call(withForgedPrincipal(verifiedPeerCtx(t, gatewaySAN), "usr_u"), "op-u")
	if err != nil || resp != "op:op-u" || !seen.hasPrincipal {
		t.Fatalf("пересланный краем владелец не получил операцию: %v %v %+v", resp, err, seen)
	}
}
