// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// session_record_extract_test.go — номер записи сессии человека, пересланный
// краем (`x-kacho-token-session-id`), читается общим слоем слушателя под тем же
// решением о доверии, что и личность (приёмка Ф11, §9 п.7). Каждая проба
// меняет против законного близнеца ровно один факт.

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/principalwire"
)

const sessionForwarderSAN = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-api-gateway"

func verifiedForwarderCtx() context.Context {
	return grpcsrv.WithCertIdentityIn(context.Background(), grpcsrv.NewTrustDomain("kacho.cloud"),
		sessionForwarderSAN, true)
}

// sessionRecordAfterExtract прогоняет пару извлечения и возвращает то, что
// увидел обработчик.
func sessionRecordAfterExtract(t *testing.T, ctx context.Context, withCertExtract bool) (string, bool) {
	t.Helper()
	got, gotTrusted := "<unset>", false
	final := func(c context.Context, _ any) (any, error) {
		got, gotTrusted = grpcsrv.TrustedSessionRecordFromContext(c)
		return nil, nil
	}
	chain := chainUnary(grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(grpcsrv.NewTrustedForwarders(sessionForwarderSAN))))
	if withCertExtract {
		chain = chainUnary(
			grpcsrv.UnaryCertIdentityExtract(grpcsrv.NewTrustDomain("kacho.cloud")),
			grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(grpcsrv.NewTrustedForwarders(sessionForwarderSAN))),
		)
	}
	_, err := chain(ctx, nil, nil, final)
	require.NoError(t, err)
	return got, gotTrusted
}

// Ключ — каталога, а не своей руки: второе написание разошлось бы молча.
func TestSessionRecordKeyIsTheCatalogues(t *testing.T) {
	require.Equal(t, principalwire.MetaTokenSessionID, grpcsrv.MDKeyTokenSessionID)
}

// Законный близнец: доверенный отправитель, личность и один номер — номер
// доезжает до обработчика.
func TestTrustedSessionRecord_VerifiedForwarder_Carried(t *testing.T) {
	ctx := metadata.NewIncomingContext(verifiedForwarderCtx(), metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyTokenSessionID, "hss-0001",
	))
	got, trusted := sessionRecordAfterExtract(t, ctx, false)
	require.True(t, trusted)
	require.Equal(t, "hss-0001", got)
}

// Один факт: отправитель без проверенного сертификата — номер отброшен вместе
// с личностью (подделка не называет чужую запись).
func TestTrustedSessionRecord_UnverifiedPeer_Dropped(t *testing.T) {
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}}}
	ctx := metadata.NewIncomingContext(peer.NewContext(context.Background(), tlsPeer), metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-mallory",
		grpcsrv.MDKeyTokenSessionID, "hss-0001",
	))
	got, trusted := sessionRecordAfterExtract(t, ctx, true)
	require.False(t, trusted)
	require.Equal(t, "", got, "номер от непроверенного собеседника обязан быть отброшен")
}

// withSessionRecord дописывает номер записи к входящим метаданным контекста.
func withSessionRecord(ctx context.Context, record string) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Append(grpcsrv.MDKeyTokenSessionID, record)
	return metadata.NewIncomingContext(ctx, md)
}

// Законный близнец на настоящем mTLS-собеседнике: сертификат пересылающего из
// перечня — номер доезжает.
func TestTrustedSessionRecord_TLSForwarder_Carried(t *testing.T) {
	ctx := withSessionRecord(verifiedTLSPeerCtx(t, sessionForwarderSAN, "usr-alice"), "hss-0001")
	got, trusted := sessionRecordAfterExtract(t, ctx, true)
	require.True(t, trusted)
	require.Equal(t, "hss-0001", got)
}

// Один факт против близнеца выше: проверенный сертификат, но не из перечня
// пересылающих — номер отброшен (внутренняя служба с годным сертификатом не
// называет чужую запись).
func TestTrustedSessionRecord_NonForwarderPeer_Dropped(t *testing.T) {
	ctx := withSessionRecord(verifiedTLSPeerCtx(t, "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-vpc", "usr-alice"), "hss-0001")
	got, trusted := sessionRecordAfterExtract(t, ctx, true)
	require.False(t, trusted)
	require.Equal(t, "", got)
}

// Один факт: личность не переслана — номер записи сессии человека ни о ком не
// говорит и не несётся.
func TestTrustedSessionRecord_NoPrincipal_NotCarried(t *testing.T) {
	ctx := metadata.NewIncomingContext(verifiedForwarderCtx(), metadata.Pairs(
		grpcsrv.MDKeyTokenSessionID, "hss-0001",
	))
	got, trusted := sessionRecordAfterExtract(t, ctx, false)
	require.True(t, trusted, "доверие — свойство отправителя и от личности не зависит")
	require.Equal(t, "", got)
}

// Один факт: два значения — «не названо», а не догадка, какое из них верное.
func TestTrustedSessionRecord_TwoValues_NotCarried(t *testing.T) {
	ctx := metadata.NewIncomingContext(verifiedForwarderCtx(), metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyTokenSessionID, "hss-0001",
		grpcsrv.MDKeyTokenSessionID, "hss-0002",
	))
	got, trusted := sessionRecordAfterExtract(t, ctx, false)
	require.True(t, trusted)
	require.Equal(t, "", got)
}

// Контекст, не прошедший извлечение, номера не несёт и доверия не объявляет.
func TestTrustedSessionRecord_NoCarrier(t *testing.T) {
	got, trusted := grpcsrv.TrustedSessionRecordFromContext(context.Background())
	require.Equal(t, "", got)
	require.False(t, trusted)
}

// Помощники теста перезаписывают свои поля и номер не теряют.
func TestTrustedSessionRecord_SurvivesTestHelpers(t *testing.T) {
	ctx := metadata.NewIncomingContext(verifiedForwarderCtx(), metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyTokenSessionID, "hss-0001",
	))
	var got string
	final := func(c context.Context, _ any) (any, error) {
		c = grpcsrv.WithTrustedACR(c, "2", true)
		got, _ = grpcsrv.TrustedSessionRecordFromContext(c)
		return nil, nil
	}
	_, err := chainUnary(grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(grpcsrv.NewTrustedForwarders(sessionForwarderSAN))))(ctx, nil, nil, final)
	require.NoError(t, err)
	require.Equal(t, "hss-0001", got)
}
