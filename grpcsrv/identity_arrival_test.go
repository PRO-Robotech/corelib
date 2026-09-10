// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// identity_arrival_test.go — приёмка KAN-WIRE-1, сценарии KAN-W2-01…KAN-W2-04,
// предмет `ПР-1`.
//
// # Предмет
//
// Рассинхрон написания ключей личности на проводе даёт НЕ отказ, а ПОТЕРЮ
// личности: приёмник, не найдя своих ключей, читал это как «личности нет» и шёл
// дальше. Отсутствие личности в этом тракте отказом не является намеренно —
// фоновые пути ходят без неё, и запасное значение существует по решению.
// Значит переход обязан принести СВОЙ отказ, а не полагаться на то, что
// несовпадение заметит следующий слой.
//
// # Отличие отказа от законной безымянности — ОДИН факт
//
// Отвергается запрос, в котором личность БЫЛА ОБЪЯВЛЕНА и не приехала: пир
// прислал ключ формы личности под пространством имён, которого эта сборка не
// читает, либо прислал часть нашего — и не назвал ни типа, ни идентификатора.
// Запрос, не приславший ничего похожего на личность, не отвергается: это
// законная безымянность, и на ней стоят фоновые согласователи.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
)

const arrivalGatewaySAN = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-api-gateway"

// forwarderCtx — контекст запроса от ДОВЕРЕННОГО отправителя: mTLS-verified пир,
// чей SAN стоит в суженном круге. Ровно та посадка, при которой край передаёт
// личность арендатора.
func forwarderCtx(t *testing.T, md metadata.MD) context.Context {
	t.Helper()
	leaf := &x509.Certificate{URIs: mustURIs(t, arrivalGatewaySAN)}
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{leaf}},
	}}}
	return metadata.NewIncomingContext(peer.NewContext(context.Background(), tlsPeer), md)
}

// throughPair прогоняет ctx через РЕАЛЬНУЮ пару звеньев и возвращает то, что
// увидел бы обработчик: дошёл ли вызов до него, какая личность у него на руках,
// и отказ звена, если он был.
func throughPair(t *testing.T, ctx context.Context) (called bool, p operations.Principal, hasP bool, err error) {
	t.Helper()
	final := func(c context.Context, _ any) (any, error) {
		called = true
		p, hasP = operations.PrincipalFromContextOK(c)
		return nil, nil
	}
	chained := chainUnary(grpcsrv.PrincipalExtractUnary(
		grpcsrv.NewTrustDomain("kacho.cloud"),
		grpcsrv.NewTrustedForwarders(arrivalGatewaySAN),
	)...)
	_, err = chained(ctx, nil, nil, final)
	return called, p, hasP, err
}

// TestIdentityArrival_BothSidesOfOneBuild_IdentityReaches — KAN-W2-01,
// ПОЛОЖИТЕЛЬНЫЙ БЛИЗНЕЦ. Без него отрицания ниже зеленели бы на службе,
// отвергающей всё подряд.
func TestIdentityArrival_BothSidesOfOneBuild_IdentityReaches(t *testing.T) {
	ctx := forwarderCtx(t, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyPrincipalDisplay, "alice@example.com",
	))
	called, p, hasP, err := throughPair(t, ctx)
	require.NoError(t, err, "личность приехала теми же ключами — отказывать не в чем")
	require.True(t, called, "обработчик обязан быть вызван")
	require.True(t, hasP, "личность обязана дойти до обработчика")
	require.Equal(t, "usr-alice", p.ID, "до обработчика доехала не та личность")
	require.Equal(t, "user", p.Type)
}

// TestIdentityArrival_NamespaceMismatch_Unauthenticated — KAN-W2-02, НЕСУЩИЙ
// ОТРИЦАТЕЛЬНЫЙ. Отличие от близнеца выше — ОДИН факт: ключи, которые кладёт
// отправитель, написаны под другим пространством имён.
func TestIdentityArrival_NamespaceMismatch_Unauthenticated(t *testing.T) {
	ctx := forwarderCtx(t, metadata.Pairs(
		"x-kaname-principal-type", "user",
		"x-kaname-principal-id", "usr-alice",
		"x-kaname-principal-display-name", "alice@example.com",
	))
	called, _, _, err := throughPair(t, ctx)
	require.Error(t, err, "личность объявлена и не приехала — это отказ, а не безымянность")
	require.Equal(t, codes.Unauthenticated, status.Code(err),
		"код отказа взят у родителя дословно (WIRE-2-02)")
	require.False(t, called,
		"обработчик не должен быть вызван: иначе операция запишется на чужую личность")
}

// TestIdentityArrival_BridgedNamespaceMismatch_Unauthenticated — тот же
// рассинхрон в МОСТОВОЙ форме. Отсутствия голой формы недостаточно: префикс
// моста снимается сам, и ключ пересекает его наравне с голым.
func TestIdentityArrival_BridgedNamespaceMismatch_Unauthenticated(t *testing.T) {
	ctx := forwarderCtx(t, metadata.Pairs(
		"grpc-metadata-x-kaname-principal-type", "user",
		"grpc-metadata-x-kaname-principal-id", "usr-alice",
	))
	called, _, _, err := throughPair(t, ctx)
	require.Error(t, err)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, called)
}

// TestIdentityArrival_HalfOfOurNamespace_Unauthenticated — половина НАШЕГО
// пространства: имя приехало, а типа и идентификатора нет. Личность объявлена и
// не приехала целиком — тот же отказ.
func TestIdentityArrival_HalfOfOurNamespace_Unauthenticated(t *testing.T) {
	ctx := forwarderCtx(t, metadata.Pairs(
		grpcsrv.MDKeyPrincipalDisplay, "alice@example.com",
	))
	called, _, _, err := throughPair(t, ctx)
	require.Error(t, err, "часть личности — это объявленная и не приехавшая личность")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, called)
}

// TestIdentityArrival_NamelessCallIsNotRefused — KAN-W2-04, КОНТРОЛЬ В ОБРАТНУЮ
// СТОРОНУ. Фоновый согласователь личности не объявляет и никогда не объявлял.
// Отличие от KAN-W2-02 — ОДИН факт: похожего на личность не приехало ничего.
func TestIdentityArrival_NamelessCallIsNotRefused(t *testing.T) {
	ctx := forwarderCtx(t, metadata.Pairs("x-request-id", "req-1"))
	called, _, hasP, err := throughPair(t, ctx)
	require.NoError(t, err,
		"законная безымянность не отвергается: иначе новый механизм остановил бы фоновые пути")
	require.True(t, called, "обработчик обязан быть вызван")
	require.False(t, hasP, "личность не предъявлена — фабриковать её нельзя")
}

// TestIdentityArrival_UntrustedPeerIsNotRefused — пир, чью пересылку мы не
// почитаем, отказом не наделяется: его личность и так снимается, и «объявлена и
// не приехала» о нём не утверждение. Отличие от KAN-W2-02 — ОДИН факт: SAN пира
// вне круга.
func TestIdentityArrival_UntrustedPeerIsNotRefused(t *testing.T) {
	leaf := &x509.Certificate{URIs: mustURIs(t, "spiffe://kacho.cloud/ns/kacho/sa/kacho-vpc")}
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{leaf}},
	}}}
	ctx := metadata.NewIncomingContext(peer.NewContext(context.Background(), tlsPeer),
		metadata.Pairs("x-kaname-principal-id", "usr-alice"))
	called, _, hasP, err := throughPair(t, ctx)
	require.NoError(t, err, "недоверенный пир и так теряет личность — отказ здесь не нов")
	require.True(t, called)
	require.False(t, hasP)
}

// arrivalCount — значение счётчика по метке исхода, прочитанное из реестра.
func arrivalCount(t *testing.T, reg *prometheus.Registry, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "kacho_grpc_identity_arrival_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestIdentityArrival_RefusalIsDistinguishableFromLawfulNamelessness — «личность
// объявлена и не приехала» ОТЛИЧИМО в наблюдаемости от «вызов законно пришёл без
// личности» (KAN-W2-02, третье «И»).
//
// Утверждение стоит на счётчике, а не на коде ответа: у законной безымянности
// отказа нет и быть не должно, поэтому по кодам ответа два события неразличимы
// by construction — второе просто не появляется нигде.
func TestIdentityArrival_RefusalIsDistinguishableFromLawfulNamelessness(t *testing.T) {
	reg := prometheus.NewRegistry()
	arrival, err := grpcsrv.NewIdentityArrival(reg)
	require.NoError(t, err, "счётчик не завёлся в чистом реестре")

	chained := chainUnary(grpcsrv.PrincipalExtractUnary(
		grpcsrv.NewTrustDomain("kacho.cloud"),
		grpcsrv.NewTrustedForwarders(arrivalGatewaySAN),
		grpcsrv.WithIdentityArrival(arrival),
	)...)
	pass := func(_ context.Context, _ any) (any, error) { return nil, nil }

	// Три полосы, различающиеся ОДНИМ фактом каждая.
	_, errPresent := chained(forwarderCtx(t, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user", grpcsrv.MDKeyPrincipalID, "usr-alice")), nil, nil, pass)
	require.NoError(t, errPresent)

	_, errAbsent := chained(forwarderCtx(t, metadata.Pairs("x-request-id", "req-1")), nil, nil, pass)
	require.NoError(t, errAbsent)

	_, errForeign := chained(forwarderCtx(t, metadata.Pairs(
		"x-kaname-principal-type", "user", "x-kaname-principal-id", "usr-alice")), nil, nil, pass)
	require.Error(t, errForeign)

	require.Equal(t, float64(1), arrivalCount(t, reg, "present"))
	require.Equal(t, float64(1), arrivalCount(t, reg, "absent"),
		"законная безымянность обязана иметь СВОЮ полосу: слитая с отказом, она сделала бы "+
			"рассинхрон написания неотличимым от роста безымянных вызовов")
	require.Equal(t, float64(1), arrivalCount(t, reg, "foreign_namespace"))
	require.Equal(t, float64(0), arrivalCount(t, reg, "incomplete"),
		"полоса, которой не было, не должна рисоваться нулём — ряд заводится наблюдением")
}

// TestIdentityArrival_NilCollectorIsTransparent — нулевой измеритель ничего не
// стоит и ничего не меняет: пробе метрики не нужны, а поведение отказа от них
// не зависит.
func TestIdentityArrival_NilCollectorIsTransparent(t *testing.T) {
	chained := chainUnary(grpcsrv.PrincipalExtractUnary(
		grpcsrv.NewTrustDomain("kacho.cloud"),
		grpcsrv.NewTrustedForwarders(arrivalGatewaySAN),
		grpcsrv.WithIdentityArrival(nil),
	)...)
	called := false
	_, err := chained(forwarderCtx(t, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user", grpcsrv.MDKeyPrincipalID, "usr-alice")), nil, nil,
		func(_ context.Context, _ any) (any, error) { called = true; return nil, nil })
	require.NoError(t, err)
	require.True(t, called)
}

// TestIdentityArrival_StreamRefusesToo — оба слушателя служат обе формы вызова,
// и решение обязано быть одинаковым: подписка, открытая на потерянной личности,
// живёт дольше одиночного вызова.
func TestIdentityArrival_StreamRefusesToo(t *testing.T) {
	pair := grpcsrv.PrincipalExtractStream(
		grpcsrv.NewTrustDomain("kacho.cloud"),
		grpcsrv.NewTrustedForwarders(arrivalGatewaySAN),
	)
	require.Len(t, pair, 2, "пара звеньев обязана остаться парой")

	run := func(md metadata.MD) (bool, error) {
		called := false
		h := func(_ any, _ grpc.ServerStream) error { called = true; return nil }
		inner := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			return pair[1](srv, ss, info, next)
		}
		err := pair[0](nil, fakeStream{ctx: forwarderCtx(t, md)}, nil,
			func(srv any, ss grpc.ServerStream) error { return inner(srv, ss, nil, h) })
		return called, err
	}

	called, err := run(metadata.Pairs("x-kaname-principal-id", "usr-alice"))
	require.Error(t, err, "рассинхрон обязан отвергаться и на подписке")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, called)

	called, err = run(metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user", grpcsrv.MDKeyPrincipalID, "usr-alice"))
	require.NoError(t, err, "положительный близнец обязан пройти — иначе отрицание зелено на всём")
	require.True(t, called)
}

// TestIdentityArrival_RefusedBeforeAnyDecisionAboutTheSubject — KAN-W2-03: тот
// же рассинхрон на записи каталога, ОСВОБОЖДЁННОЙ от вопроса о субъекте.
//
// # Зачем отдельным сценарием
//
// KAN-W2-02, взятый один, был бы удовлетворён уже существующим отказом слоя
// прав — то есть не потребовал бы построить ничего. Здесь ниже по цепочке стоит
// звено, которое вопрос о субъекте НЕ ЗАДАЁТ и пропустило бы безымянный вызов:
// ровно то, чем живут освобождённые записи каталога. Наследовать отказ им не у
// кого, и проба утверждает, что до этого звена вызов не доходит вовсе.
func TestIdentityArrival_RefusedBeforeAnyDecisionAboutTheSubject(t *testing.T) {
	exemptReached := false
	// Звено, стоящее ЗА парой: освобождённая запись каталога вопроса о субъекте
	// не задаёт и безымянный вызов пропускает.
	exemptLane := func(ctx context.Context, req any, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (any, error) {
		exemptReached = true
		return handler(ctx, req)
	}
	pair := grpcsrv.PrincipalExtractUnary(
		grpcsrv.NewTrustDomain("kacho.cloud"),
		grpcsrv.NewTrustedForwarders(arrivalGatewaySAN),
	)
	chained := chainUnary(append(pair, exemptLane)...)

	handled := false
	_, err := chained(forwarderCtx(t, metadata.Pairs(
		"x-kaname-principal-type", "user",
		"x-kaname-principal-id", "usr-alice",
	)), nil, nil, func(context.Context, any) (any, error) { handled = true; return nil, nil })

	require.Error(t, err, "отказ обязан быть ПРОИЗВЕДЁН здесь, а не унаследован от слоя прав")
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, exemptReached,
		"освобождённая полоса не должна быть достигнута: наследовать отказ ей не у кого")
	require.False(t, handled)

	// ПОЛОЖИТЕЛЬНЫЙ БЛИЗНЕЦ той же полосы. Отличие — ОДИН факт: ключи написаны
	// так же, как их читает эта сборка. Без него отрицание выше зеленело бы на
	// цепочке, отвергающей всё.
	exemptReached, handled = false, false
	_, err = chained(forwarderCtx(t, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
	)), nil, nil, func(context.Context, any) (any, error) { handled = true; return nil, nil })
	require.NoError(t, err)
	require.True(t, exemptReached, "освобождённая полоса обязана работать как прежде")
	require.True(t, handled)
}
