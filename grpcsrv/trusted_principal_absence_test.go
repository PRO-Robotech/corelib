// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// Запрос, не предъявивший личности, не должен её ПОЛУЧИТЬ.
//
// Доверенная ветка trust-aware extract'а инициализирует значение системным
// принципалом и кладёт его в контекст безусловно. Личность в этой ветке —
// свойство ПЕРЕСЫЛАЮЩЕГО (peer доверен), а не свойство запроса: пересылающий
// доверен и тогда, когда пересылать нечего. Тогда запрос без единого
// identity-заголовка выходит из интерсептора с bootstrap-личностью — той самой,
// которую ownership-предикат считает владельцем каждой системно записанной
// операции, а authz-слой (при AllowSystemPrincipal) пропускает без Check'а.
//
// Замок — на наблюдаемом: после реальной цепочки интерсепторов запрос без
// identity-метаданных не имеет ключа владения. Недоверенная ветка и штатная
// пересылка настоящего принципала обязаны сохраниться без изменений.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
)

// ownerFromChain прогоняет ctx через реальную пару интерсепторов и возвращает
// то, что увидел бы handler: ключ владения и признак его наличия.
func ownerFromChain(t *testing.T, ctx context.Context, opts ...grpcsrv.TrustedPrincipalOption) (operations.Owner, bool) {
	t.Helper()
	var (
		owner operations.Owner
		has   bool
	)
	final := func(c context.Context, _ any) (any, error) {
		owner, has = operations.OwnerFromContext(c)
		return nil, nil
	}
	chained := chainUnary(
		grpcsrv.UnaryCertIdentityExtract(grpcsrv.NewTrustDomain("kacho.cloud")),
		grpcsrv.UnaryTrustedPrincipalExtract(opts...),
	)
	_, err := chained(ctx, nil, nil, final)
	require.NoError(t, err)
	return owner, has
}

// TestTrustedPrincipal_NoIdentityMetadata_NoOwnerKey — insecure-листенер
// (доверенная ветка по построению) + запрос БЕЗ identity-метаданных: личности
// не предъявлено, значит ключа владения быть не должно.
func TestTrustedPrincipal_NoIdentityMetadata_NoOwnerKey(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	owner, has := ownerFromChain(t, ctx)
	require.False(t, has,
		"запрос без identity-метаданных не предъявил личности — ключа владения быть не должно")
	require.Equal(t, operations.Owner{}, owner,
		"личность не должна фабриковаться: ожидается нулевой ключ, а не системный принципал")
}

// TestTrustedPrincipal_VerifiedForwarderNoIdentityMetadata_NoOwnerKey — то же
// на mTLS-листенере с verified форвардером из allow-list'а. Доверие к peer'у
// говорит «этому пересылающему можно верить», а не «личность предъявлена».
func TestTrustedPrincipal_VerifiedForwarderNoIdentityMetadata_NoOwnerKey(t *testing.T) {
	const gatewaySAN = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-api-gateway"
	leaf := &x509.Certificate{URIs: mustURIs(t, gatewaySAN)}
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{leaf}},
	}}}
	ctx := metadata.NewIncomingContext(peer.NewContext(context.Background(), tlsPeer), metadata.MD{})

	owner, has := ownerFromChain(t, ctx, grpcsrv.WithTrustedForwarders(grpcsrv.NewTrustedForwarders(gatewaySAN)))
	require.False(t, has,
		"доверенный форвардер без пересланной личности не наделяет запрос личностью")
	require.Equal(t, operations.Owner{}, owner)
}

// TestTrustedPrincipal_ForwardedIdentity_StillOwner — сужаем фабрикацию, а не
// штатную пересылку: настоящий пересланный принципал по-прежнему даёт свой ключ.
func TestTrustedPrincipal_ForwardedIdentity_StillOwner(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyPrincipalDisplay, "alice@example.com",
	))
	owner, has := ownerFromChain(t, ctx)
	require.True(t, has, "пересланный настоящий принципал обязан остаться владельцем")
	require.Equal(t, operations.Owner{PrincipalType: "user", PrincipalID: "usr-alice"}, owner)
}

// TestTrustedPrincipal_NoIdentityMetadata_TrustFlagUnchanged — признак доверия
// к peer'у описывает ПЕРЕСЫЛАЮЩЕГО и менять его нельзя: на нём завязаны
// tenant-интерсепторы compute/nlb/geo, читающие только этот флаг.
func TestTrustedPrincipal_NoIdentityMetadata_TrustFlagUnchanged(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	var trusted bool
	final := func(c context.Context, _ any) (any, error) {
		_, trusted = grpcsrv.TrustedPrincipalFromContext(c)
		return nil, nil
	}
	chained := chainUnary(
		grpcsrv.UnaryCertIdentityExtract(grpcsrv.NewTrustDomain("kacho.cloud")),
		grpcsrv.UnaryTrustedPrincipalExtract(),
	)
	_, err := chained(ctx, nil, nil, final)
	require.NoError(t, err)
	require.True(t, trusted,
		"insecure-листенер остаётся доверенным пересылающим — флаг доверия не меняется")
}
