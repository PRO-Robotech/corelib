// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// trust_domain_test.go — домен доверия объявляет установка, и по нему решается,
// чьи сертификаты признаются своими (приёмка KAN-WIRE-1, сценарии KAN-W4-02 и
// KAN-W4-03).
//
// # Пара, отличающаяся ОДНИМ фактом
//
// Отрицание («предъявитель чужого домена права говорить за пользователя не
// получает») зеленело бы на службе, не принимающей никого. Поэтому рядом стоит
// положительный близнец, и различие между ними — ровно домен в имени
// предъявителя: пространство, учётка, круг отправителей и переданная личность у
// обоих одни и те же.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
)

const (
	// declaredDomain — домен, который объявила установка.
	declaredDomain = "kaname.local"
	// foreignDomain — домен ЧУЖОЙ установки: сертификат настоящий, выпущен
	// настоящим удостоверяющим центром, но не нашим.
	foreignDomain = "other.example"
	// sanTail — общий хвост обоих имён. Вынесен затем, чтобы различие между
	// близнецами было ОДНИМ фактом и это было видно в исходнике, а не
	// восстанавливалось сравнением двух литералов.
	sanTail = "/ns/kaname/sa/kacho-api-gateway"
)

// forwardedIdentityCtx — ctx с проверенным mTLS-пиром, предъявившим заданное имя,
// и с переданной личностью конечного пользователя.
func forwardedIdentityCtx(t *testing.T, certSAN, princID string) context.Context {
	t.Helper()
	leaf := &x509.Certificate{URIs: mustURIs(t, certSAN)}
	p := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{leaf}},
	}}}
	return metadata.NewIncomingContext(peer.NewContext(context.Background(), p), metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, princID,
	))
}

// runPrincipalChain прогоняет пару звеньев извлечения личности и возвращает то,
// что увидел обработчик: доехала ли переданная личность.
func runPrincipalChain(t *testing.T, d grpcsrv.TrustDomain, ctx context.Context, forwarderSAN string) (
	principalID string, carried bool,
) {
	t.Helper()
	chain := grpcsrv.PrincipalExtractUnary(d, grpcsrv.NewTrustedForwarders(forwarderSAN))
	require.Len(t, chain, 2, "пара звеньев обязана остаться парой: сертификат доказывает, ЧЕЙ "+
		"это пир, и ничего не говорит о праве представляться другим")

	final := func(c context.Context, _ any) (any, error) {
		pr, ok := operations.PrincipalFromContextOK(c)
		principalID, carried = pr.ID, ok
		return nil, nil
	}
	inner := func(c context.Context, req any) (any, error) {
		return chain[1](c, req, &grpc.UnaryServerInfo{}, final)
	}
	_, err := chain[0](ctx, nil, &grpc.UnaryServerInfo{}, inner)
	require.NoError(t, err)
	return principalID, carried
}

// TestTrustDomain_DeclaredPresenterKeepsTheRightToSpeak — KAN-W4-03,
// ПОЛОЖИТЕЛЬНЫЙ близнец: предъявитель объявленного домена, чьё имя внесено в
// круг, передаёт личность, и она доезжает до обработчика.
func TestTrustDomain_DeclaredPresenterKeepsTheRightToSpeak(t *testing.T) {
	d := grpcsrv.NewTrustDomain(declaredDomain)
	san := "spiffe://" + declaredDomain + sanTail

	id, carried := runPrincipalChain(t, d, forwardedIdentityCtx(t, san, "usr-alice"), san)

	require.True(t, carried, "личность, переданная предъявителем ОБЪЯВЛЕННОГО домена из круга, "+
		"обязана доехать до обработчика — иначе отрицание ниже зеленело бы на службе, "+
		"не принимающей никого")
	require.Equal(t, "usr-alice", id)
}

// TestTrustDomain_ForeignPresenterGetsNoRightToSpeak — KAN-W4-02, ОТРИЦАНИЕ.
// От близнеца выше отличается ОДНИМ фактом: домен в имени предъявителя.
func TestTrustDomain_ForeignPresenterGetsNoRightToSpeak(t *testing.T) {
	d := grpcsrv.NewTrustDomain(declaredDomain)
	foreignSAN := "spiffe://" + foreignDomain + sanTail
	// Круг отправителей суживается ТЕМ ЖЕ именем, что предъявляет пир: если бы
	// личность снималась из-за несовпадения круга, проба утверждала бы о круге,
	// а не о домене.
	id, carried := runPrincipalChain(t, d, forwardedIdentityCtx(t, foreignSAN, "usr-mallory"), foreignSAN)

	require.False(t, carried, "предъявитель ЧУЖОГО домена получил право говорить за пользователя: "+
		"сертификат настоящий, но выпущен не нашей установкой")
	require.NotEqual(t, "usr-mallory", id, "переданная личность не имеет права доехать до обработчика")
}

// TestTrustDomain_UndeclaredRecognizesNobody — необъявленный домен не признаёт
// своим НИКОГО, включая предъявителя, чьё имя внесено в круг.
//
// Это и есть смысл нулевого значения: «домен не назвали» не означает «принимаем
// всех». До транспорта такое значение доходить не должно (страж старта), и
// проба утверждает, что даже дойдя — оно фейл-клоуз.
func TestTrustDomain_UndeclaredRecognizesNobody(t *testing.T) {
	var d grpcsrv.TrustDomain
	san := "spiffe://" + declaredDomain + sanTail

	_, carried := runPrincipalChain(t, d, forwardedIdentityCtx(t, san, "usr-alice"), san)

	require.False(t, carried, "необъявленный домен признал своим предъявителя — тогда пропущенная "+
		"величина означала бы «принимаем любого», а не «не принимаем никого»")
}

// TestTrustDomain_RequireRefusesAnUndeclaredDomain — KAN-W4-05 по своей оси:
// величина, без которой процесс не работает, обязана останавливать старт и
// называть ручку, а не молча отвергать каждого отправителя.
func TestTrustDomain_RequireRefusesAnUndeclaredDomain(t *testing.T) {
	const knob = "authz.trust-domain (env KACHO_VPC_AUTHZ__TRUST_DOMAIN)"

	var undeclared grpcsrv.TrustDomain
	err := undeclared.Require(grpcsrv.TrustDomainGate{Knob: knob})
	require.Error(t, err, "необъявленный домен обязан останавливать старт")
	require.Contains(t, err.Error(), knob, "текст отказа читает оператор: сообщение, не называющее "+
		"ручку, оставляет стенд неподнятым и непонятным")

	declared := grpcsrv.NewTrustDomain(declaredDomain)
	require.NoError(t, declared.Require(grpcsrv.TrustDomainGate{Knob: knob}),
		"объявленный домен старт не останавливает — иначе отказ выше зеленел бы на всём")
}

// TestTrustDomain_NormalisesWhatTheOperatorWrote — одно написание домена, а не
// два: величина со схемой и величина без неё означают одно и то же.
func TestTrustDomain_NormalisesWhatTheOperatorWrote(t *testing.T) {
	want := grpcsrv.NewTrustDomain(declaredDomain)
	for _, raw := range []string{
		declaredDomain,
		"  " + declaredDomain + "  ",
		"spiffe://" + declaredDomain,
		"spiffe://" + declaredDomain + "/",
	} {
		got := grpcsrv.NewTrustDomain(raw)
		require.Equal(t, want, got, "написание %q дало другой домен — тогда у одного предмета "+
			"было бы два представления, и сравнение по одному разошлось бы с другим", raw)
	}

	var zero grpcsrv.TrustDomain
	for _, raw := range []string{"", "   ", "spiffe://", "///"} {
		require.Equal(t, zero, grpcsrv.NewTrustDomain(raw),
			"вырожденная величина %q обязана давать канонический ноль, а не второе "+
				"представление «не объявлен»", raw)
	}
}

// TestTrustDomain_MatchesIsTheOnlyPredicate — сверка идёт по домену целиком, а
// не по началу строки: домен `kaname.local` не вправе признавать своим
// `kaname.local.evil.example`.
func TestTrustDomain_MatchesIsTheOnlyPredicate(t *testing.T) {
	d := grpcsrv.NewTrustDomain(declaredDomain)

	require.True(t, d.Matches("spiffe://"+declaredDomain+sanTail))
	require.False(t, d.Matches("spiffe://"+declaredDomain+".evil.example"+sanTail),
		"домен, дописанный справа, признан своим — сверка идёт по началу строки без "+
			"разделителя, и чужой удостоверяющий центр выпустил бы себе наше имя")
	require.False(t, d.Matches("spiffe://"+foreignDomain+sanTail))
	require.False(t, d.Matches(""))

	var zero grpcsrv.TrustDomain
	require.False(t, zero.Matches("spiffe://"+declaredDomain+sanTail),
		"необъявленный домен признал своим кого-то")
	require.Empty(t, zero.URIPrefix(), "префикс необъявленного домена совпал бы с ЛЮБЫМ доменом")
	require.Empty(t, zero.NamespacePrefix())
}
