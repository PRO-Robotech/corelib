// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// x5fixture_test.go — фикстура integration-пробы Х5: носитель, поднятый ЦЕЛИКОМ
// через [Serve], на настоящих слушателях loopback с mTLS.
//
// # Почему через Serve, а не через сборщик сервера
//
// Предмет Х5 — что носитель ПОДНИМАЕТ в форме «только внутренний слушатель»:
// сколько слушателей, какая цепочка на внутреннем, чем кончается падение
// слушателя. Сборщик сервера одного слушателя — механизм, имя и подпись которого
// принадлежат реализации; проба, собранная на нём, утверждала бы про функцию, а
// не про то, что видит вызывающий на проводе. Поэтому вход фикстуры — только
// [Serve] и принятый дескриптор.
//
// # Что фикстура несёт
//
//   - нейтральный дескриптор `corelib.servicehost.x5probe.v1` с двумя методами:
//     `Get` на объекте `cluster` (право спрашивается у решателя) и `Ping`
//     с изъятием (решатель не спрашивается). Регистрация в глобальном реестре —
//     один раз на процесс (`sync.Once`): его спрашивают `catalogderive` и отказы
//     старта. Имя пакета в дереве не встречается, столкнуться не с чем;
//   - решатель-счётчик: сколько вопросов `Check` дошло и с каким ответом;
//   - обработчик-счётчик: сколько вызовов дошло до метода;
//   - эфемерный удостоверяющий центр, сертификат сервера и два клиентских: с SAN
//     края (в круге) и с SAN M (вне круга), выпущенные ОДНИМ центром.
//
// Ничего из сертификатного материала не хранится: всё выпускается в памяти.
package servicehost

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/emptypb"

	authzv1 "github.com/PRO-Robotech/corelib/api/corelib/authz/v1"
	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/catalogderive"
	"github.com/PRO-Robotech/corelib/authz/proxytuple"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// Имена дескриптора пробы. Полные имена выписаны один раз и склейкой в пробах не
// собираются: склейка расходится с дескриптором молча.
const (
	x5Package     = "corelib.servicehost.x5probe.v1"
	x5ServiceName = x5Package + ".WidgetService"
	x5Get         = "/" + x5ServiceName + "/Get"
	x5Ping        = "/" + x5ServiceName + "/Ping"
)

// SAN-ы пиров. Край — в круге отправителей; M — вне круга, тот же центр.
const (
	x5TrustDomain = "kacho.cloud"
	x5EdgeSAN     = "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"
	x5OutsideSAN  = "spiffe://kacho.cloud/ns/kacho/sa/kacho-x5probe-m"
	x5ServerSAN   = "spiffe://kacho.cloud/ns/kacho/sa/kacho-x5probe"
)

var (
	x5RegisterOnce sync.Once
	x5RegisterErr  error
)

// x5Descriptor регистрирует дескриптор пробы один раз на процесс.
func x5Descriptor(t *testing.T) {
	t.Helper()
	x5RegisterOnce.Do(func() { x5RegisterErr = registerX5Descriptor() })
	if x5RegisterErr != nil {
		t.Fatalf("фикстура: дескриптор пробы Х5 не зарегистрирован — проба вакуумна: %v", x5RegisterErr)
	}
}

func registerX5Descriptor() error {
	getOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(getOpts, authzv1.E_Permission, "x5probe.widgets.get")
	proto.SetExtension(getOpts, authzv1.E_RequiredRelation, "admin")
	proto.SetExtension(getOpts, authzv1.E_ScopeExtractor, &authzv1.ScopeExtractor{
		ObjectType:       "cluster",
		FromRequestField: "*",
	})
	pingOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(pingOpts, authzv1.E_Permission, catalogderive.ExemptPermission)

	method := func(name string, opts *descriptorpb.MethodOptions) *descriptorpb.MethodDescriptorProto {
		return &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(name),
			InputType:  proto.String("." + x5Package + ".WidgetRequest"),
			OutputType: proto.String("." + x5Package + ".WidgetReply"),
			Options:    opts,
		}
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("corelib/servicehost/x5probe/v1/x5probe.proto"),
		Package:    proto.String(x5Package),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/descriptor.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("WidgetRequest")},
			{Name: proto.String("WidgetReply")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name:   proto.String("WidgetService"),
			Method: []*descriptorpb.MethodDescriptorProto{method("Get", getOpts), method("Ping", pingOpts)},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		return fmt.Errorf("сборка файла: %w", err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		return fmt.Errorf("регистрация файла: %w", err)
	}
	for i := 0; i < fd.Messages().Len(); i++ {
		md := fd.Messages().Get(i)
		if err := protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(md)); err != nil {
			return fmt.Errorf("регистрация типа %s: %w", md.FullName(), err)
		}
	}
	return nil
}

// x5Handled — сколько вызовов дошло до методов пробы (по полному имени).
type x5Handled struct{ get, ping atomic.Int64 }

// x5Registrar регистрирует испытательный сервис и считает дошедшие вызовы.
func x5Registrar(h *x5Handled) Registrar {
	input := func() (protoreflect.MessageType, error) {
		return protoregistry.GlobalTypes.FindMessageByName(x5Package + ".WidgetRequest")
	}
	handler := func(full string, n *atomic.Int64) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
		return func(_ any, ctx context.Context, dec func(any) error, chain grpc.UnaryServerInterceptor) (any, error) {
			mt, err := input()
			if err != nil {
				return nil, status.Error(codes.Internal, "фикстура: тип входа не найден")
			}
			in := mt.New().Interface()
			if err := dec(in); err != nil {
				return nil, err
			}
			h := func(context.Context, any) (any, error) {
				n.Add(1)
				return &emptypb.Empty{}, nil
			}
			if chain == nil {
				return h(ctx, in)
			}
			return chain(ctx, in, &grpc.UnaryServerInfo{FullMethod: full}, h)
		}
	}
	desc := grpc.ServiceDesc{
		ServiceName: x5ServiceName,
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Get", Handler: handler(x5Get, &h.get)},
			{MethodName: "Ping", Handler: handler(x5Ping, &h.ping)},
		},
		Metadata: "corelib/servicehost/x5probe/v1/x5probe.proto",
	}
	return func(r grpc.ServiceRegistrar) { r.RegisterService(&desc, struct{}{}) }
}

// noRegistrar — регистратор, который ничего не регистрирует (слушатель без служимого).
func noRegistrar() Registrar { return func(grpc.ServiceRegistrar) {} }

// x5Check — решатель-счётчик. Отвечает разрешением ровно администратору кластера.
type x5Check struct{ calls atomic.Int64 }

func (c *x5Check) Check(_ context.Context, subject, relation, _ string) (bool, error) {
	c.calls.Add(1)
	return subject == "user:usr_x5admin" && relation == "admin", nil
}

// x5PKI — эфемерный удостоверяющий центр и выпущенные им ключевые пары.
type x5PKI struct {
	pool    *x509.CertPool
	server  tls.Certificate
	edge    tls.Certificate
	outside tls.Certificate
}

func newX5PKI(t *testing.T) *x5PKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("фикстура: ключ центра: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "x5probe-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("фикстура: сертификат центра: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("фикстура: разбор центра: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	serial := int64(2)
	leaf := func(san string, server bool) tls.Certificate {
		key, kerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if kerr != nil {
			t.Fatalf("фикстура: ключ листа: %v", kerr)
		}
		u, uerr := url.Parse(san)
		if uerr != nil {
			t.Fatalf("фикстура: SAN %q: %v", san, uerr)
		}
		eku := x509.ExtKeyUsageClientAuth
		if server {
			eku = x509.ExtKeyUsageServerAuth
		}
		serial++
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: san},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
			URIs:         []*url.URL{u},
		}
		if server {
			tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, derr := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if derr != nil {
			t.Fatalf("фикстура: лист %q: %v", san, derr)
		}
		keyDER, merr := x509.MarshalECPrivateKey(key)
		if merr != nil {
			t.Fatalf("фикстура: ключ листа %q: %v", san, merr)
		}
		pair, perr := tls.X509KeyPair(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
		if perr != nil {
			t.Fatalf("фикстура: пара %q: %v", san, perr)
		}
		return pair
	}
	return &x5PKI{
		pool:    pool,
		server:  leaf(x5ServerSAN, true),
		edge:    leaf(x5EdgeSAN, false),
		outside: leaf(x5OutsideSAN, false),
	}
}

// serverCreds — mTLS слушателя: клиентский сертификат обязателен и проверяется.
func (p *x5PKI) serverCreds() credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{p.server},
		ClientCAs:    p.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	})
}

// clientCreds — транспорт пира с данной ключевой парой.
func (p *x5PKI) clientCreds(c tls.Certificate) credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{c},
		RootCAs:      p.pool,
		MinVersion:   tls.VersionTLS12,
	})
}

// freeAddr — адрес loopback, свободный в момент вызова.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("фикстура: свободный адрес: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// x5Fixture — всё, что проба передаёт носителю и снимает с провода.
type x5Fixture struct {
	pki     *x5PKI
	check   *x5Check
	handled *x5Handled
}

func newX5Fixture(t *testing.T) *x5Fixture {
	t.Helper()
	x5Descriptor(t)
	return &x5Fixture{pki: newX5PKI(t), check: &x5Check{}, handled: &x5Handled{}}
}

// pairSpec — законный дескриптор ПАРЫ слушателей на loopback: положительный
// близнец формы «только внутренний слушатель». Внутренняя половина пары — тот
// же транспорт, тот же решатель, тот же круг, что у формы.
func (f *x5Fixture) pairSpec(publicAddr, internalAddr string) servicecontract.Spec {
	return servicecontract.Spec{
		Service:         "kacho-x5probe",
		Mode:            servicecontract.ModeDev,
		Logger:          slog.New(slog.DiscardHandler),
		Forwarders:      servicecontract.Value(grpcsrv.NewTrustedForwarders(x5EdgeSAN)),
		TrustDomain:     servicecontract.Value(grpcsrv.NewTrustDomain(x5TrustDomain)),
		TrustDomainKnob: "KACHO_X5PROBE_AUTHZ_TRUST_DOMAIN",
		ForwarderKnobs: servicecontract.ForwarderKnobs{
			SANs:     "KACHO_X5PROBE_AUTHZ_TRUSTED_FORWARDER_SANS",
			TrustAny: "KACHO_X5PROBE_AUTHZ_TRUST_ANY_FORWARDER",
		},
		DBSSLMode:       servicecontract.Value("require"),
		Authz:           servicecontract.AuthzSelf,
		SelfCheck:       f.check,
		HandlingBudget:  30 * time.Second,
		PublicAddr:      publicAddr,
		InternalAddr:    internalAddr,
		PublicCreds:     f.pki.serverCreds(),
		InternalCreds:   f.pki.serverCreds(),
		Emits:           servicecontract.NotApplicable[[]proxytuple.Relation]("проба ничего не эмитит"),
		Registers:       servicecontract.NotApplicable[[]servicecontract.ObjectType]("проба не владеет типами"),
		Narrowers:       servicecontract.NotApplicable[map[servicecontract.MethodFQN]servicecontract.ListNarrower]("сужаемых методов нет"),
		HideExistence:   servicecontract.NotApplicable[map[servicecontract.ObjectType]servicecontract.NotFoundFormat]("скрытия нет"),
		Delivery:        servicecontract.NotApplicable[servicecontract.DeliveryProvenance]("проба ничего не эмитит"),
		DenyBudget:      servicecontract.NotApplicable[float64]("проба меряет исход отказа, а не его темп"),
		AuthzObserve:    func(func() authz.Metrics) {},
		Metrics:         prometheus.NewRegistry(),
		BootGate:        servicecontract.NotApplicable[servicecontract.BootGate]("проба ничего не эмитит"),
		ServiceIdentity: servicecontract.NotApplicable[grpcsrv.ServiceIdentity]("служб-подписчиков у пробы нет"),
		StreamBudget:    servicecontract.NotApplicable[time.Duration]("серверных стримов проба не служит"),
		Admission: servicecontract.Value(servicecontract.Admission{
			Public:   grpcsrv.PlatformPublicAdmission(),
			Internal: grpcsrv.PlatformInternalAdmission(),
		}),
	}
}

// running — носитель, запущенный в фоне.
type running struct {
	done   chan error
	cancel context.CancelFunc
}

// startServe запускает [Serve] в фоне и возвращает его исход каналом.
func startServe(t *testing.T, d servicecontract.Descriptor, public, internal Registrar) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{done: make(chan error, 1), cancel: cancel}
	go func() { r.done <- Serve(ctx, d, public, internal) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Errorf("фикстура: носитель не вернул управление за 10 с после отмены")
		}
	})
	return r
}

// errServeReturned — носитель вернул управление раньше, чем начал слушать.
var errServeReturned = errors.New("носитель вернул управление до подъёма слушателя")

// awaitListening ждёт УСЛОВИЯ «адрес принимает соединения», а не паузы. Если
// носитель вернул управление раньше — это исход, и он возвращается как есть.
func awaitListening(t *testing.T, r *running, addr string) error {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-r.done:
			r.done <- err // исход остаётся читаемым для Cleanup
			return fmt.Errorf("%w: %v", errServeReturned, err)
		default:
		}
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("адрес %s не начал принимать соединения за 10 с", addr)
}

// outcome — то, что видит вызывающий на проводе.
type outcome struct {
	code codes.Code
	msg  string
}

func (o outcome) String() string { return fmt.Sprintf("%v %q", o.code, o.msg) }

// callAs зовёт метод пробы пиром с данной ключевой парой. adminHeaders —
// заголовки личности администратора кластера, те же, что пересылает край.
func (f *x5Fixture) callAs(t *testing.T, addr string, peer tls.Certificate, method string, adminHeaders bool) outcome {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(f.pki.clientCreds(peer)))
	if err != nil {
		t.Fatalf("фикстура: клиент %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if adminHeaders {
		ctx = metadata.AppendToOutgoingContext(ctx,
			grpcsrv.MDKeyPrincipalType, "user",
			grpcsrv.MDKeyPrincipalID, "usr_x5admin")
	}
	var out emptypb.Empty
	cerr := conn.Invoke(ctx, method, &emptypb.Empty{}, &out)
	st, _ := status.FromError(cerr)
	return outcome{code: st.Code(), msg: st.Message()}
}
