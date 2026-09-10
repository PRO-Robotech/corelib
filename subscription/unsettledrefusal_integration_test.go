// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

// unsettledrefusal_integration_test.go — неоткрытый поток НАЗЫВАЕТ отказ
// (kacho#1386).
//
// # Предмет
//
// Подтвердить границу можно не всегда: незавершающийся писатель держит журнал, и
// его невыпущенный номер может оказаться ниже наблюдаемого максимума. Садить на
// такую границу нельзя — это объявленный размен «не потерять» против «доставить
// сейчас».
//
// Но закрыть поток МОЛЧА нельзя тоже. До служебного сообщения клиент не получал
// ничего: ни позиции, с которой возобновляться, ни кода. Чистый конец он читает
// как «событий нет» и переоткрывает поток в тишине сколько угодно долго —
// отказ, у которого нет ни клиентского, ни операторского признака.

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
)

// isEOF — закрыт ли поток ЧИСТО, без названной причины. Чистый возврат сервера
// доезжает до клиента ровно как io.EOF: ни кода, ни текста.
func isEOF(err error) bool { return errors.Is(err, io.EOF) }

// TestUnsettledStreamNamesItsRefusal — ЗАКОННЫЙ БЛИЗНЕЦ.
//
// Журнал держит писатель, который не завершится за срок потока. Подтверждать
// границу нечем по существу, и садить подписчика нельзя — это объявленный
// размен («не потерять» против «доставить сейчас»).
//
// Но закрыть поток МОЛЧА тоже нельзя. Служебного сообщения клиент не получал,
// позиции у него нет, возобновляться ему не с чего: чистый конец читается им как
// «событий нет», и он переоткрывает поток в тишине сколько угодно долго. Отказ
// обязан быть НАЗВАН — тогда клиент знает, что делать дальше, а «сервер занят»
// отличимо от «журнал пуст».
//
// Без этой стороны починка несущего случая зеленела бы на сервере, который
// по-прежнему нем в единственном режиме, где он честно не может ответить.
func TestUnsettledStreamNamesItsRefusal(t *testing.T) {
	s := newStand(t, standOpts{budget: 3 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s.exec(t, `INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
	           VALUES ('Network','net00000000000000001','CREATED','{"projectId":"prj-a"}'::jsonb)`)

	// Писатель держит журнал дольше срока потока и не отпускает его.
	writerConn := mustConnect(t, ctx, s.dsn)
	tx, err := writerConn.Begin(ctx)
	mustNoErr(t, err)
	insertInTx(t, ctx, tx, "net00000000000000004")
	defer func() { _ = tx.Rollback(ctx) }()

	strm, err := s.client.Subscribe(ctx, &subscriptionv1.SubscriptionRequest{})
	if err != nil {
		t.Fatalf("подписка не открылась: %v", err)
	}
	_, recvErr := strm.Recv()
	if recvErr == nil {
		t.Fatal("поток отдал служебное сообщение, хотя граница не подтверждена: " +
			"подписчик посажен на неподтверждённый ноль")
	}
	if isEOF(recvErr) {
		t.Errorf("поток закрыт МОЛЧА (%v): клиент не получал ни позиции, ни отказа — "+
			"«сервер не смог» неотличимо от «событий нет», и переоткрытие идёт в тишине", recvErr)
	}
}
