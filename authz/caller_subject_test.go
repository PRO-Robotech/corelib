// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// caller_subject_test.go — одна функция субъекта вызывающего (NTF-1 Р2, З13):
// пересланный доверенным принципал решает; иначе имя, положенное звеном
// идентичности служб, даёт `service:<имя>`; иначе субъекта нет. Звено решения о
// доступе видит служебный субъект ДО ветки ScopeFiltered, а корзина бюджета
// отказов и ключ кэша у `service:x` и `user:x` раздельны (CX1-03).
//
// Контекст со служебным именем производит НАСТОЯЩЕЕ звено `grpcsrv`: сеттера
// для проб у носителя нет, и заводить его значило бы проверять вход, которого в
// бою не бывает.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
)

const (
	callerNotifySAN = "spiffe://kacho.cloud/ns/kacho/sa/kacho-notify"
	callerStrayeSAN = "spiffe://kacho.cloud/ns/kacho/sa/kacho-stray"
	listFQN         = "/kacho.cloud.vpc.v1.NetworkService/List"
	getFQN          = "/kacho.cloud.vpc.v1.NetworkService/Get"
)

// throughServiceLink прогоняет ctx через звено идентичности с перечнем
// `methods` и таблицей `{notify}` и отдаёт контекст, который увидел бы
// следующий за звеном.
func throughServiceLink(t *testing.T, ctx context.Context, method string, methods ...string) context.Context {
	t.Helper()
	id, err := grpcsrv.NewServiceIdentity(methods, map[string]grpcsrv.ServiceName{callerNotifySAN: "notify"})
	if err != nil {
		t.Fatalf("звено: %v", err)
	}
	var out context.Context
	_, err = id.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(c context.Context, _ any) (any, error) { out = c; return nil, nil })
	if err != nil {
		t.Fatalf("звено вернуло ошибку: %v", err)
	}
	return out
}

func certPeer(t *testing.T, san string) context.Context {
	t.Helper()
	u, err := url.Parse(san)
	if err != nil {
		t.Fatalf("SAN: %v", err)
	}
	leaf := &x509.Certificate{URIs: []*url.URL{u}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{
		State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf}}},
	}})
}

func serviceCaller(t *testing.T, method string) context.Context {
	t.Helper()
	return throughServiceLink(t, certPeer(t, callerNotifySAN), method, method)
}

func TestServiceSubjectIsTheOneProducerOfTheServiceString(t *testing.T) {
	if got := authz.ServiceSubject("notify"); got != "service:notify" {
		t.Fatalf("ServiceSubject(notify) = %q", got)
	}
	// Имя вне формы субъекта не даёт: строка, собранная из него, сдвинула бы
	// границу «тип:идентификатор» либо стала бы ссылкой на набор.
	for _, bad := range []grpcsrv.ServiceName{"", "no:tify", "no#tify", "Notify", "no tify"} {
		if got := authz.ServiceSubject(bad); got != "" {
			t.Errorf("ServiceSubject(%q) = %q, ожидалась пустая строка", bad, got)
		}
	}
}

func TestCallerSubjectDecidesInTheDeclaredOrder(t *testing.T) {
	t.Run("служба без пересланного принципала — service:<имя>", func(t *testing.T) {
		c, ok := authz.CallerSubject(serviceCaller(t, getFQN))
		if !ok {
			t.Fatal("служебный субъект не выведен")
		}
		if c.Subject() != "service:notify" || c.PrincipalID() != "service:notify" {
			t.Fatalf("субъект %q, корзина %q — ожидалось service:notify в обоих (CX1-03)",
				c.Subject(), c.PrincipalID())
		}
		if name, isSvc := c.Service(); !isSvc || name != "notify" {
			t.Fatalf("Service() = %q %v", name, isSvc)
		}
		if _, fwd := c.Forwarded(); fwd {
			t.Fatal("служебный субъект объявил себя пересланным")
		}
	})

	t.Run("пересланный принципал решает, даже когда звено положило имя", func(t *testing.T) {
		ctx := operations.WithPrincipal(certPeer(t, callerNotifySAN),
			operations.Principal{Type: "user", ID: "usr_u"})
		ctx = throughServiceLink(t, ctx, getFQN, getFQN)
		c, ok := authz.CallerSubject(ctx)
		if !ok || c.Subject() != "user:usr_u" || c.PrincipalID() != "usr_u" {
			t.Fatalf("пересланный пользователь не решил: %+v %v", c, ok)
		}
		if _, isSvc := c.Service(); isSvc {
			t.Fatal("субъект из сертификата смешан с пересланным")
		}
	})

	t.Run("пересланный service при сертификате вне таблицы — субъекта нет", func(t *testing.T) {
		ctx := operations.WithPrincipal(certPeer(t, callerStrayeSAN),
			operations.Principal{Type: authz.ServiceSubjectType, ID: "notify"})
		ctx = throughServiceLink(t, ctx, getFQN, getFQN)
		if c, ok := authz.CallerSubject(ctx); ok {
			t.Fatalf("пересланный принципал типа service дал субъект %q", c.Subject())
		}
	})

	t.Run("пересланный service решает и при сертификате из таблицы — субъекта нет", func(t *testing.T) {
		ctx := operations.WithPrincipal(certPeer(t, callerNotifySAN),
			operations.Principal{Type: authz.ServiceSubjectType, ID: "notify"})
		ctx = throughServiceLink(t, ctx, getFQN, getFQN)
		if c, ok := authz.CallerSubject(ctx); ok {
			t.Fatalf("пересланный service не решил отказом, а уступил сертификату: %q", c.Subject())
		}
	})

	t.Run("пересланная анонимность решает — субъекта нет", func(t *testing.T) {
		ctx := operations.WithPrincipal(certPeer(t, callerNotifySAN),
			operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID})
		ctx = throughServiceLink(t, ctx, getFQN, getFQN)
		if c, ok := authz.CallerSubject(ctx); ok {
			t.Fatalf("анонимность уступила сертификату: %q", c.Subject())
		}
	})

	t.Run("никого — субъекта нет", func(t *testing.T) {
		if c, ok := authz.CallerSubject(context.Background()); ok {
			t.Fatalf("пустой контекст дал субъект %q", c.Subject())
		}
		// Сертификат из таблицы, но метод вне перечня — тоже никого.
		ctx := throughServiceLink(t, certPeer(t, callerNotifySAN), listFQN, getFQN)
		if c, ok := authz.CallerSubject(ctx); ok {
			t.Fatalf("метод вне перечня дал субъект %q", c.Subject())
		}
	})
}

// NTF1-M01 (пол acr): служебный субъект оценивается EvaluateStepUp как
// машинный принципал; пересланный пользователь — как пользователь.
func TestServiceCallerIsAMachinePrincipalForTheStepUpFloor(t *testing.T) {
	c, ok := authz.CallerSubject(serviceCaller(t, getFQN))
	if !ok {
		t.Fatal("служебный субъект не выведен")
	}
	if v := grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{PrincipalType: c.StepUpType(), RequiredACR: "2"}); v != grpcsrv.StepUpAllow {
		t.Fatalf("служебный субъект не освобождён от пола acr как машинный: %v", v)
	}
	u, ok := authz.CallerSubject(operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr_u"}))
	if !ok {
		t.Fatal("пользователь не выведен")
	}
	if v := grpcsrv.EvaluateStepUp(grpcsrv.StepUpInput{PrincipalType: u.StepUpType(), RequiredACR: "2"}); v != grpcsrv.StepUpDenyACR {
		t.Fatalf("пользователь без acr освобождён от пола: %v", v)
	}
}

// NTF1-M02 / M08 (сторона corelib): извлекатель звена прав видит служебный
// субъект до ветки ScopeFiltered. Метод вне перечня — «субъекта нет» и отказ
// `permission denied` без вызова обработчика; тот же метод в перечне — обработчик
// вызван.
func TestInterceptorSeesTheServiceSubjectBeforeTheScopeFilteredBranch(t *testing.T) {
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache: authz.NewCache(0),
		Map:   makeMap(),
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return false, nil
		}),
	})
	run := func(ctx context.Context) (bool, error) {
		called := false
		_, err := intr.Unary()(ctx, &fakeReq{id: "enp_x"}, &grpc.UnaryServerInfo{FullMethod: listFQN},
			func(context.Context, any) (any, error) { called = true; return nil, nil })
		return called, err
	}

	t.Run("вне перечня — отказ звена прав, обработчик не вызван", func(t *testing.T) {
		called, err := run(throughServiceLink(t, certPeer(t, callerNotifySAN), listFQN, getFQN))
		if called {
			t.Fatal("обработчик вызван без субъекта")
		}
		if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "permission denied" {
			t.Fatalf("ожидался PERMISSION_DENIED «permission denied», получено %v", err)
		}
	})
	t.Run("в перечне — обработчик вызван", func(t *testing.T) {
		called, err := run(throughServiceLink(t, certPeer(t, callerNotifySAN), listFQN, listFQN))
		if err != nil || !called {
			t.Fatalf("служебный субъект не прошёл до обработчика: called=%v err=%v", called, err)
		}
	})
}

// NTF1-M08 (сторона corelib): вопрос модели задаётся от `service:notify`.
func TestInterceptorAsksTheModelAsTheService(t *testing.T) {
	var asked string
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache: authz.NewCache(0),
		Map:   makeMap(),
		Client: authz.CheckClientFunc(func(_ context.Context, subject, _, _ string) (bool, error) {
			asked = subject
			return true, nil
		}),
	})
	if _, err := runUnary(intr, serviceCaller(t, getFQN), getFQN, &fakeReq{id: "enp_x"}); err != nil {
		t.Fatalf("служебный вызов отвергнут: %v", err)
	}
	if asked != "service:notify" {
		t.Fatalf("модель спрошена от %q, ожидалось service:notify", asked)
	}
}

// CX1-03 — корзина бюджета отказов и ключ кэша у `service:x` и у `user:x`
// раздельны: шторм отказов пользователя `notify` не отнимает бюджета у службы
// `notify`, а положительный вердикт службы не отдаётся пользователю.
func TestServiceAndUserOfTheSameNameShareNeitherBudgetNorCache(t *testing.T) {
	t.Run("бюджет отказов", func(t *testing.T) {
		intr := authz.NewInterceptor(authz.InterceptorOptions{
			Cache: authz.NewCache(0),
			Map:   makeMap(),
			Client: authz.CheckClientFunc(func(_ context.Context, subject, _, _ string) (bool, error) {
				return subject == "service:notify", nil
			}),
			DenyRateLimitPerSec: 1, // burst 2
		})
		user := ctxWithPrincipal(t, "notify", "user")
		exhausted := false
		for i := 0; i < 10; i++ {
			_, err := runUnary(intr, user, getFQN, &fakeReq{id: "enp_x"})
			if status.Code(err) == codes.ResourceExhausted {
				exhausted = true
				break
			}
		}
		if !exhausted {
			t.Fatal("шторм отказов пользователя не исчерпал его корзину — проба ничего не меряет")
		}
		if _, err := runUnary(intr, serviceCaller(t, getFQN), getFQN, &fakeReq{id: "enp_x"}); err != nil {
			t.Fatalf("служба notify отвергнута после шторма пользователя notify: %v — корзины общие", err)
		}
	})
	t.Run("кэш вердиктов", func(t *testing.T) {
		checks := map[string]int{}
		intr := authz.NewInterceptor(authz.InterceptorOptions{
			Cache: authz.NewCache(0),
			Map:   makeMap(),
			Client: authz.CheckClientFunc(func(_ context.Context, subject, _, _ string) (bool, error) {
				checks[subject]++
				return subject == "service:notify", nil
			}),
		})
		if _, err := runUnary(intr, serviceCaller(t, getFQN), getFQN, &fakeReq{id: "enp_x"}); err != nil {
			t.Fatalf("служба отвергнута: %v", err)
		}
		_, err := runUnary(intr, ctxWithPrincipal(t, "notify", "user"), getFQN, &fakeReq{id: "enp_x"})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("пользователь notify получил вердикт службы notify из кэша: %v", err)
		}
		if checks["user:notify"] != 1 {
			t.Fatalf("вопрос о пользователе не дошёл до модели (%v) — ключ кэша общий", checks)
		}
	})
}

// Строка `service:` не собирается ни одним кодеком тенантского словаря: ни
// FormatSubject, ни TenantSubject слова `service` не знают (CX1-01 (а)).
func TestTenantCodecsDoNotKnowTheServiceType(t *testing.T) {
	if got := authz.FormatSubject(authz.ServiceSubjectType, "notify"); strings.HasPrefix(got, "service:") {
		t.Fatalf("FormatSubject собрал %q", got)
	}
	if got, ok := authz.TenantSubject(authz.ServiceSubjectType, "notify"); ok {
		t.Fatalf("TenantSubject собрал %q", got)
	}
}
