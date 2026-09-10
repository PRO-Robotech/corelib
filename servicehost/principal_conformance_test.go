// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// principal_conformance_test.go — поведенческий страж против confused-deputy:
// проверенный пир, НЕ входящий в круг отправителей, не может говорить за
// другого.
//
// # Что здесь на кону
//
// Личность — единственный субъект пообъектной проверки прав. Сертификат
// доказывает, ЧЕЙ это пир, и НИЧЕГО не говорит о праве представляться другим.
// Без сужения круга любой пир с валидным сертификатом (соседний внутренний
// сервис) мог бы переслать произвольную личность — в том числе администратора —
// и дойти до admin-CRUD на внутреннем слушателе.
//
// # Почему проба переехала сюда и стала СТРОЖЕ
//
// До носителя она жила в композиционном корне одного сервиса и прогоняла его
// локальный сборщик пары звеньев. Здесь она прогоняет ЦЕПОЧКУ НОСИТЕЛЯ целиком —
// ту самую, что поднимается в бою, — и поэтому утверждает не про пару звеньев, а
// про то, что доезжает до обработчика. Свойство стало общим на все сервисы
// вместе с цепочкой.
package servicehost

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

const (
	gatewaySAN   = "spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway"
	neighbourSAN = "spiffe://kacho.cloud/ns/kacho/sa/kacho-compute"
	probeMethod  = "/kacho.cloud.demo.v1.WidgetService/Get"
)

// TestForwardedIdentityIsHonouredOnlyFromTheCircle — обе стороны сразу.
//
// (а) сосед с ВАЛИДНЫМ сертификатом, но вне круга, пересылает личность
// администратора → носитель личности обязан остаться ПУСТЫМ, и проверка прав
// увидит безымянного вызывающего;
// (б) тот же запрос от шлюза, который в круге, → личность honored.
//
// Без (б) проба зеленела бы на цепочке, снимающей личность у ВСЕХ, — то есть на
// полностью сломанном контуре.
func TestForwardedIdentityIsHonouredOnlyFromTheCircle(t *testing.T) {
	chain := hostChainWithPermissiveDecision(t)

	t.Run("сосед вне круга не может говорить за другого", func(t *testing.T) {
		ctx := withForgedPrincipal(verifiedPeerCtx(t, neighbourSAN), "usr-admin")
		carrier, trusted, present := runHostChain(t, chain, ctx)
		if trusted {
			t.Error("пир вне круга объявлен доверенным отправителем личности")
		}
		if present {
			t.Errorf("носитель личности заполнен (%q) для пира вне круга: подделанная "+
				"личность доехала бы до проверки прав как субъект", carrier)
		}
	})

	t.Run("шлюз в круге honored", func(t *testing.T) {
		ctx := withForgedPrincipal(verifiedPeerCtx(t, gatewaySAN), "usr-admin")
		carrier, trusted, present := runHostChain(t, chain, ctx)
		if !trusted {
			t.Error("пир в круге не признан доверенным отправителем — законная передача сломана")
		}
		if !present || carrier != "usr-admin" {
			t.Errorf("личность от законного отправителя не доехала: carrier=%q present=%v",
				carrier, present)
		}
	})
}

// TestUnnarrowedCircleWouldHonourAnyVerifiedPeer — почему отказ старта О1
// необходим, показанное ПОВЕДЕНИЕМ, а не пересказом.
//
// На несужённом круге тот же сосед становится доверенным отправителем. Это
// действующая, намеренная семантика общей библиотеки, и тип её не меняет —
// поэтому «забыл заполнить» ловит отказ старта. Проба фиксирует, ЧТО именно
// этот отказ предотвращает: без неё утверждение «пустой круг доверяет всем»
// осталось бы прозой в комментарии.
func TestUnnarrowedCircleWouldHonourAnyVerifiedPeer(t *testing.T) {
	spec := chainSpec()
	spec.Forwarders = servicecontract.Value(grpcsrv.TrustedForwarders{}) // круг НЕ сужен
	chain := chainOf(t, spec)

	ctx := withForgedPrincipal(verifiedPeerCtx(t, neighbourSAN), "usr-admin")
	carrier, trusted, present := runHostChain(t, chain, ctx)
	if !trusted || !present || carrier != "usr-admin" {
		t.Fatalf("несужённый круг не пропустил чужую личность (carrier=%q trusted=%v present=%v). "+
			"Это опровергает основание О1: если бы пустой круг сам по себе запрещал, отказ старта "+
			"был бы не нужен — и тогда неверен либо этот вывод, либо комментарий у О1",
			carrier, trusted, present)
	}
	t.Log("подтверждено: на несужённом круге личность принимается от любого проверенного пира — " +
		"именно это и предотвращает отказ старта О1, а не сам тип")
}

// hostChainWithPermissiveDecision — цепочка носителя с законно сужённым кругом.
func hostChainWithPermissiveDecision(t *testing.T) grpc.UnaryServerInterceptor {
	t.Helper()
	spec := chainSpec()
	spec.Forwarders = servicecontract.Value(grpcsrv.NewTrustedForwarders(gatewaySAN))
	return chainOf(t, spec)
}

// chainOf собирает ПРОДОВУЮ цепочку носителя и ставит в её слот звено решения,
// освобождающее пробный метод.
//
// Освобождение здесь не ослабление: предмет пробы — что доезжает ДО решения о
// доступе, а не как решение принимается. Пустой слот отказал бы раньше, и проба
// меряла бы его, а не личность.
func chainOf(t *testing.T, spec servicecontract.Spec) grpc.UnaryServerInterceptor {
	t.Helper()
	var slot decisionSlot
	slot.install(authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-demo",
		Map:         authz.RPCMap{probeMethod: {Public: true}},
		Cache:       authz.NewCache(0),
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return false, nil
		}),
	}))
	return chainUnaryServer(unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic)...)
}

// runHostChain прогоняет контекст через цепочку и возвращает то, что увидел бы
// обработчик.
//
// `present` читается аксессором, который ОТЛИЧАЕТ «носитель вычищен» от «в
// носителе лежит системная личность». Обычный аксессор такого различения не
// даёт по своему контракту (оба состояния дают системную личность), поэтому
// утверждать им вычищенность нельзя: проверка осталась бы зелёной и тогда,
// когда недоверенному пиру выдали системную личность — а она и есть владелец
// каждой системно записанной операции.
func runHostChain(t *testing.T, chain grpc.UnaryServerInterceptor, ctx context.Context) (carrierID string, trusted, present bool) {
	t.Helper()
	final := func(c context.Context, _ any) (any, error) {
		p, ok := operations.PrincipalFromContextOK(c)
		carrierID, present = p.ID, ok
		_, trusted = grpcsrv.TrustedPrincipalFromContext(c)
		return nil, nil
	}
	if _, err := chain(ctx, nil, &grpc.UnaryServerInfo{FullMethod: probeMethod}, final); err != nil {
		t.Fatalf("цепочка вернула ошибку: %v", err)
	}
	return carrierID, trusted, present
}

func withForgedPrincipal(ctx context.Context, id string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, id,
		grpcsrv.MDKeyPrincipalDisplay, id+"@example.com",
	))
}

// verifiedPeerCtx — пир с ПРОВЕРЕННЫМ сертификатом, несущим переданный SAN.
// Именно «проверенный»: предмет пробы — что верного сертификата НЕДОСТАТОЧНО
// для права говорить за другого.
func verifiedPeerCtx(t *testing.T, san string) context.Context {
	t.Helper()
	u, err := url.Parse(san)
	if err != nil {
		t.Fatalf("разбор SAN %q: %v", san, err)
	}
	leaf := &x509.Certificate{URIs: []*url.URL{u}}
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{leaf}},
		}},
	})
}

// chainUnaryServer композирует звенья слева направо вокруг обработчика — та же
// семантика, что у `grpc.ChainUnaryInterceptor`.
func chainUnaryServer(interceptors ...grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		chained := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			ic := interceptors[i]
			next := chained
			chained = func(c context.Context, r any) (any, error) { return ic(c, r, info, next) }
		}
		return chained(ctx, req)
	}
}
