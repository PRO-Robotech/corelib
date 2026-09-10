// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// trusted_forwarders_order_test.go — порядок звеньев внутри пары извлечения
// НЕСУЩИЙ, и это доказывается исходом, а не объявляется комментарием.
//
// Пока каждый композиционный корень выписывал пару вручную, порядок держали
// текстовые стражи в семи сервисах. Теперь пару отдаёт один конструктор, и
// свойство «сначала личность сертификата, потом переданная личность» обязано
// быть заперто ЗДЕСЬ — иначе стражи сняли, а замка на их месте не осталось.
//
// Проба ставит обе стороны: правильный порядок пропускает переданную личность
// законного отправителя, перевёрнутый — теряет её. Без второй половины проба
// была бы зелёной и на перевёрнутом порядке (решение о доверии принималось бы по
// ещё не извлечённой личности сертификата, то есть «не доверяем» вообще всем), и
// отличить работающую пару от сломанной она бы не могла.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
)

func TestPrincipalExtractPairOrderIsLoadBearing(t *testing.T) {
	const gatewaySAN = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-api-gateway"
	circle := grpcsrv.NewTrustedForwarders(gatewaySAN)

	seen := func(t *testing.T, chain []grpc.UnaryServerInterceptor) (string, bool) {
		t.Helper()
		var id string
		var present bool
		final := func(c context.Context, _ any) (any, error) {
			p, ok := operations.PrincipalFromContextOK(c)
			id, present = p.ID, ok
			return nil, nil
		}
		_, err := chainUnary(chain...)(verifiedTLSPeerCtx(t, gatewaySAN, "usr-alice"), nil, nil, final)
		require.NoError(t, err)
		return id, present
	}

	t.Run("constructor_order_lets_the_legitimate_sender_through", func(t *testing.T) {
		id, present := seen(t, grpcsrv.PrincipalExtractUnary(grpcsrv.NewTrustDomain("kacho.cloud"), circle))
		require.True(t, present, "пара в порядке конструктора обязана пропустить личность законного отправителя")
		require.Equal(t, "usr-alice", id)
	})

	t.Run("reversed_order_drops_it", func(t *testing.T) {
		pair := grpcsrv.PrincipalExtractUnary(grpcsrv.NewTrustDomain("kacho.cloud"), circle)
		require.Len(t, pair, 2, "пара обязана состоять ровно из двух звеньев")
		id, present := seen(t, []grpc.UnaryServerInterceptor{pair[1], pair[0]})
		require.False(t, present,
			"перевёрнутый порядок обязан ТЕРЯТЬ личность: решение о доверии принимается по ещё "+
				"не извлечённой личности сертификата. Если проба зелёная в обе стороны, она "+
				"не про порядок вовсе")
		require.NotEqual(t, "usr-alice", id)
	})
}
