// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// service_identity_test.go — звено идентичности служб (NTF-1 Р2, замысел З13):
// стражи старта (NTF1-M09), одна функция приведения SAN (CX1-02) и исход звена
// на каждом из входов, которые его судят (NTF1-M01…M04 на стороне звена).
//
// Каждое отрицание стоит рядом со своим законным близнецом в той же пробе:
// «субъекта нет» верно и на звене, которое не опознаёт НИКОГО, поэтому без
// близнеца отрицание зеленело бы на полностью сломанном звене.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

const (
	notifySAN      = "spiffe://kacho.cloud/ns/kacho/sa/kacho-notify"
	subscribeFQN   = "/corelib.subscription.InternalSubscriptionService/Subscribe"
	outOfListFQN   = "/kacho.cloud.demo.v1.WidgetService/List"
	probeBSAN      = "spiffe://kacho.cloud/ns/kacho/sa/kacho-probe-b"
	lawfulNotify   = grpcsrv.ServiceName("notify")
	lawfulProbeB   = grpcsrv.ServiceName("probe-b")
	refusalHeading = "звено идентичности служб"
)

// lawfulIdentity — согласное звено: перечень `{Subscribe}`, таблица двух служб.
func lawfulIdentity(t *testing.T) grpcsrv.ServiceIdentity {
	t.Helper()
	id, err := grpcsrv.NewServiceIdentity([]string{subscribeFQN}, map[string]grpcsrv.ServiceName{
		notifySAN: lawfulNotify,
		probeBSAN: lawfulProbeB,
	})
	if err != nil {
		t.Fatalf("согласное звено отвергнуто: %v", err)
	}
	return id
}

// NTF1-M09 — стражи старта звена. Каждый отказ называет свою часть и значение;
// близнец — согласное звено — собирается.
func TestNTF1M09_ServiceIdentityStartGuards(t *testing.T) {
	t.Run("близнец: согласное звено собирается и отдаёт перечень и таблицу", func(t *testing.T) {
		id := lawfulIdentity(t)
		if got := id.Methods(); len(got) != 1 || got[0] != subscribeFQN {
			t.Fatalf("перечень методов звена = %v, ожидался [%s]", got, subscribeFQN)
		}
		rows := id.Rows()
		if len(rows) != 2 || rows[0].SAN != notifySAN || rows[0].Name != lawfulNotify ||
			rows[1].SAN != probeBSAN || rows[1].Name != lawfulProbeB {
			t.Fatalf("строки таблицы = %+v, ожидались обе в порядке SAN", rows)
		}
		if id.IsEmpty() {
			t.Fatal("звено с перечнем и таблицей объявило себя пустым")
		}
	})

	t.Run("пустое с обеих сторон — законно и пусто", func(t *testing.T) {
		id, err := grpcsrv.NewServiceIdentity(nil, nil)
		if err != nil {
			t.Fatalf("пустые перечень и таблица согласны друг с другом, а отвергнуты: %v", err)
		}
		if !id.IsEmpty() {
			t.Fatal("пустое звено не объявило себя пустым")
		}
	})

	for _, tc := range []struct {
		name     string
		methods  []string
		table    map[string]grpcsrv.ServiceName
		mustName []string
	}{
		{
			name:     "(а) перечень непуст, таблица пуста",
			methods:  []string{subscribeFQN},
			mustName: []string{"таблица", subscribeFQN},
		},
		{
			name:     "(б) таблица непуста, перечень пуст",
			table:    map[string]grpcsrv.ServiceName{notifySAN: lawfulNotify},
			mustName: []string{"перечень", notifySAN},
		},
		{
			name:    "(г) один SAN дважды — две записи одного SAN",
			methods: []string{subscribeFQN},
			table: map[string]grpcsrv.ServiceName{
				notifySAN: lawfulNotify,
				strings.Replace(notifySAN, "spiffe://kacho.cloud", "SPIFFE://KACHO.CLOUD", 1): "notify-two",
			},
			mustName: []string{"SAN повторён", notifySAN},
		},
		{
			name:    "(д) одно имя дважды",
			methods: []string{subscribeFQN},
			table: map[string]grpcsrv.ServiceName{
				notifySAN: lawfulNotify,
				probeBSAN: lawfulNotify,
			},
			mustName: []string{"имя службы повторено", `"notify"`},
		},
		{
			name:     "(е) имя не DNS label",
			methods:  []string{subscribeFQN},
			table:    map[string]grpcsrv.ServiceName{notifySAN: "Notify_Service"},
			mustName: []string{"DNS label", `"Notify_Service"`},
		},
		{
			name:     "ключ не канонический — отказ, а не молчаливое приведение",
			methods:  []string{subscribeFQN},
			table:    map[string]grpcsrv.ServiceName{"SPIFFE://kacho.cloud/ns/kacho/sa/kacho-notify": lawfulNotify},
			mustName: []string{"каноническ", "SPIFFE://kacho.cloud/ns/kacho/sa/kacho-notify", notifySAN},
		},
		{
			name:     "ключ не SPIFFE-идентификатор",
			methods:  []string{subscribeFQN},
			table:    map[string]grpcsrv.ServiceName{"https://kacho.cloud/ns/kacho/sa/kacho-notify": lawfulNotify},
			mustName: []string{"SPIFFE", "https://kacho.cloud/ns/kacho/sa/kacho-notify"},
		},
		{
			name:     "метод перечня не в форме полного имени",
			methods:  []string{"Subscribe"},
			table:    map[string]grpcsrv.ServiceName{notifySAN: lawfulNotify},
			mustName: []string{"полного имени", `"Subscribe"`},
		},
		{
			name:     "метод перечня дважды",
			methods:  []string{subscribeFQN, subscribeFQN},
			table:    map[string]grpcsrv.ServiceName{notifySAN: lawfulNotify},
			mustName: []string{"метод повторён", subscribeFQN},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := grpcsrv.NewServiceIdentity(tc.methods, tc.table)
			if err == nil {
				t.Fatal("звено собрано, а обязано быть отвергнуто")
			}
			msg := err.Error()
			if !strings.Contains(msg, refusalHeading) {
				t.Errorf("отказ не называет предмета %q: %s", refusalHeading, msg)
			}
			for _, want := range tc.mustName {
				if !strings.Contains(msg, want) {
					t.Errorf("отказ не называет %q: %s", want, msg)
				}
			}
		})
	}
}

// CX1-02 — одна функция приведения SAN. Приводит регистр схемы и узла; всё, что
// делает идентификатор неоднозначным, приводит к пустой строке, а не к «похожему».
func TestCanonicalSANIsOneFunctionForBothSides(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{notifySAN, notifySAN},
		{"SPIFFE://Kacho.Cloud/ns/kacho/sa/kacho-notify", notifySAN},
		// Путь регистрозависим: приведение его не трогает.
		{"spiffe://kacho.cloud/ns/Kacho/sa/kacho-notify", "spiffe://kacho.cloud/ns/Kacho/sa/kacho-notify"},
		{"spiffe://kacho.cloud/ns/kacho/sa/kacho-notify/", ""},
		{"spiffe://kacho.cloud/ns/kacho/../kacho/sa/kacho-notify", ""},
		{"spiffe://kacho.cloud/ns/kacho/./sa/kacho-notify", ""},
		{"spiffe://kacho.cloud/ns//sa/kacho-notify", ""},
		{"spiffe://kacho.cloud/ns/kacho/sa/kacho%2Dnotify", ""},
		{"spiffe://kacho.cloud:8443/ns/kacho/sa/kacho-notify", ""},
		{"spiffe://u@kacho.cloud/ns/kacho/sa/kacho-notify", ""},
		{"spiffe://kacho.cloud/ns/kacho/sa/kacho-notify?x=1", ""},
		{"spiffe://kacho.cloud/ns/kacho/sa/kacho-notify#f", ""},
		{"spiffe://kacho.cloud", ""},
		{"spiffe:///ns/kacho/sa/kacho-notify", ""},
		{"https://kacho.cloud/ns/kacho/sa/kacho-notify", ""},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("разбор %q: %v", tc.raw, err)
		}
		if got := grpcsrv.CanonicalSAN(u); got != tc.want {
			t.Errorf("CanonicalSAN(%q) = %q, ожидалось %q", tc.raw, got, tc.want)
		}
	}
	if got := grpcsrv.CanonicalSAN(nil); got != "" {
		t.Errorf("CanonicalSAN(nil) = %q, ожидалась пустая строка", got)
	}
}

// NTF1-M09 (е) по ту сторону: форма имени — DNS label, и это одна функция.
func TestServiceNameIsADNSLabel(t *testing.T) {
	for _, ok := range []string{"notify", "probe-b", "a", "n0", strings.Repeat("a", 63)} {
		if !grpcsrv.ServiceName(ok).Valid() {
			t.Errorf("%q — DNS label, а отвергнуто", ok)
		}
	}
	for _, bad := range []string{"", "-notify", "notify-", "Notify", "no_tify", "no.tify", "no tify",
		"service:notify", strings.Repeat("a", 64)} {
		if grpcsrv.ServiceName(bad).Valid() {
			t.Errorf("%q — не DNS label, а принято", bad)
		}
	}
}

// verifiedPeer — пир с ПРОВЕРЕННЫМ сертификатом, несущим переданные URI-SAN.
func verifiedPeer(t *testing.T, sans ...string) context.Context {
	t.Helper()
	return peerWithSANs(t, true, sans...)
}

func peerWithSANs(t *testing.T, verified bool, sans ...string) context.Context {
	t.Helper()
	leaf := &x509.Certificate{}
	for _, s := range sans {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatalf("разбор SAN %q: %v", s, err)
		}
		leaf.URIs = append(leaf.URIs, u)
	}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if verified {
		state.VerifiedChains = [][]*x509.Certificate{{leaf}}
	}
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: state},
	})
}

// recognizedUnary прогоняет unary-звено и возвращает то, что увидел обработчик.
func recognizedUnary(t *testing.T, id grpcsrv.ServiceIdentity, ctx context.Context, method string) (grpcsrv.ServiceName, bool) {
	t.Helper()
	var name grpcsrv.ServiceName
	var ok bool
	_, err := id.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(c context.Context, _ any) (any, error) {
			name, ok = grpcsrv.ServiceNameFromContext(c)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("звено вернуло ошибку: %v", err)
	}
	return name, ok
}

type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ctxStream) Context() context.Context { return s.ctx }

func recognizedStream(t *testing.T, id grpcsrv.ServiceIdentity, ctx context.Context, method string) (grpcsrv.ServiceName, bool) {
	t.Helper()
	var name grpcsrv.ServiceName
	var ok bool
	err := id.Stream()(nil, ctxStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: method, IsServerStream: true},
		func(_ any, ss grpc.ServerStream) error {
			name, ok = grpcsrv.ServiceNameFromContext(ss.Context())
			return nil
		})
	if err != nil {
		t.Fatalf("звено вернуло ошибку: %v", err)
	}
	return name, ok
}

// NTF1-M01…M04 на стороне звена, на обеих полосах вызова (unary и stream).
func TestServiceIdentityLinkRecognizesOnlyTheExactSANOnAListedMethod(t *testing.T) {
	id := lawfulIdentity(t)
	for _, lane := range []struct {
		name string
		run  func(*testing.T, grpcsrv.ServiceIdentity, context.Context, string) (grpcsrv.ServiceName, bool)
	}{
		{"unary", recognizedUnary},
		{"stream", recognizedStream},
	} {
		t.Run(lane.name, func(t *testing.T) {
			t.Run("M01: точный SAN на методе перечня — имя службы", func(t *testing.T) {
				name, ok := lane.run(t, id, verifiedPeer(t, notifySAN), subscribeFQN)
				if !ok || name != lawfulNotify {
					t.Fatalf("звено не положило имя: %q %v", name, ok)
				}
			})
			t.Run("M01: приведённый SAN того же узла — то же имя", func(t *testing.T) {
				name, ok := lane.run(t, id, verifiedPeer(t, "SPIFFE://KACHO.cloud/ns/kacho/sa/kacho-notify"), subscribeFQN)
				if !ok || name != lawfulNotify {
					t.Fatalf("SAN, равный ключу после приведения, не опознан: %q %v", name, ok)
				}
			})
			t.Run("M02: метод вне перечня — имени нет", func(t *testing.T) {
				if name, ok := lane.run(t, id, verifiedPeer(t, notifySAN), outOfListFQN); ok {
					t.Fatalf("имя %q положено на методе вне перечня", name)
				}
			})
			for _, san := range []string{
				"spiffe://other.cloud/ns/kacho/sa/kacho-notify",   // только домен доверия
				"spiffe://kacho.cloud/ns/other/sa/kacho-notify",   // только пространство
				"spiffe://kacho.cloud/ns/kacho/sa/kacho-notifyer", // только учётка
				notifySAN + "/extra",                              // равен по началу, длиннее
			} {
				t.Run("M03: "+san, func(t *testing.T) {
					if name, ok := lane.run(t, id, verifiedPeer(t, san), subscribeFQN); ok {
						t.Fatalf("SAN %q опознан как %q: сравнение обязано быть равенством", san, name)
					}
				})
			}
			t.Run("M04: сертификат не проверен — имени нет", func(t *testing.T) {
				ctx := metadata.NewIncomingContext(peerWithSANs(t, false, notifySAN), metadata.Pairs(
					grpcsrv.MDKeyPrincipalType, "user", grpcsrv.MDKeyPrincipalID, "usr_x"))
				if name, ok := lane.run(t, id, ctx, subscribeFQN); ok {
					t.Fatalf("непроверенный сертификат дал имя %q", name)
				}
			})
			t.Run("два URI-SAN — имени нет", func(t *testing.T) {
				if name, ok := lane.run(t, id, verifiedPeer(t, notifySAN, probeBSAN), subscribeFQN); ok {
					t.Fatalf("сертификат с двумя SPIFFE-идентификаторами опознан как %q", name)
				}
			})
			t.Run("два URI-SAN с одним и тем же ключом — тоже имени нет", func(t *testing.T) {
				if name, ok := lane.run(t, id, verifiedPeer(t, notifySAN, notifySAN), subscribeFQN); ok {
					t.Fatalf("сертификат с двумя SPIFFE-идентификаторами опознан как %q", name)
				}
			})
			t.Run("пир без транспорта — имени нет", func(t *testing.T) {
				if name, ok := lane.run(t, id, context.Background(), subscribeFQN); ok {
					t.Fatalf("вызов без сертификата опознан как %q", name)
				}
			})
		})
	}
}

// Пустое звено не опознаёт никого — и это его единственная наблюдаемая примета.
func TestEmptyServiceIdentityRecognizesNobody(t *testing.T) {
	var zero grpcsrv.ServiceIdentity
	if name, ok := recognizedUnary(t, zero, verifiedPeer(t, notifySAN), subscribeFQN); ok {
		t.Fatalf("нулевое звено опознало %q", name)
	}
}

// Самоотчёт звена — перечень методов и строки таблицы — производит само звено;
// у звена без перечня — метка неприменимости, а не пустая строка.
func TestServiceIdentityReportNamesMethodsAndRows(t *testing.T) {
	rep := lawfulIdentity(t).Report()
	for _, want := range []string{subscribeFQN, notifySAN + "=notify", probeBSAN + "=probe-b"} {
		if !strings.Contains(rep, want) {
			t.Errorf("самоотчёт %q не называет %q", rep, want)
		}
	}
	var zero grpcsrv.ServiceIdentity
	if got := zero.Report(); got != grpcsrv.ServiceIdentityNotApplicable {
		t.Errorf("самоотчёт пустого звена = %q, ожидалось %q", got, grpcsrv.ServiceIdentityNotApplicable)
	}
}
