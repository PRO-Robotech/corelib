// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
)

// Supersede закрывает исходом SUPERSEDED(not_current) строки из ids,
// которые pending и без действующей аренды (замысел issue-2924 З8 п.5), в
// транзакции вызывающего tx, и возвращает закрытые. Строка под действующей
// арендой (взятая и ещё без исхода) доходит до своего исхода; терминальная и
// отсутствующая не трогаются, это не ошибка. Строка, отложенная Ack DEFER,
// аренды не несёт (Ack снимает её при любом виде) и закрывается; строка с
// истёкшей арендой — тоже.
//
// Один оператор, замки — в порядке id. Оператор ставит ровно семь столбцов,
// те же, что уборщик истечения: state, outcome_reason, обнуление столбцов
// записи исхода (повтор прежнего Ack получает LEASE_LOST, а не «успех без
// изменения») и секрета, outcome_at. Столбцов аренды не пишет: у строки без
// действующей аренды они уже не действуют. Наблюдатель исхода не зовётся:
// следствия исхода (резервы, счётчики) исполняет вызывающий по возвращённым
// id. Источник (префикс таблиц) — из контекста (Source.Bind), без него —
// ErrSourceUnbound.
func Supersede(ctx context.Context, tx pgx.Tx, ids []string) ([]string, error) {
	s, ok := sourceFrom(ctx)
	if !ok {
		return nil, ErrSourceUnbound
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`WITH c AS (
  SELECT id FROM %[1]s
   WHERE id = ANY($1) AND state = 'pending' AND `+noActiveLease+`
   ORDER BY id
     FOR UPDATE)
UPDATE %[1]s t
   SET state = 'superseded', outcome_reason = $2, outcome_token = NULL, recorded_kind = NULL,
       recorded_reason = NULL, secret_attrs = NULL, outcome_at = now()
  FROM c
 WHERE t.id = c.id
RETURNING t.id`, tablename.Of(s.service, tablename.Outbox)), ids, string(ReasonNotCurrent))
	if err != nil {
		return nil, fmt.Errorf("feed: закрытие неактуальных строк: %w", err)
	}
	closed, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("feed: закрытие неактуальных строк: %w", err)
	}
	return closed, nil
}

// DeleteUnleased удаляет из ids строки без действующей аренды в любом
// состоянии — pending, отложенную, терминальную — в транзакции вызывающего tx
// и возвращает удалённые и оставшиеся под арендой (замысел issue-2924 З24
// п.4). Решение «под арендой» и удаление — на одном чтении (CX5-57): первый
// оператор блокирует все переданные строки, что есть, в порядке id и считает
// свободу по их последней версии (строку, которую держит запись исхода, он
// дожидается); второй удаляет только свободные из заблокированных. Каждая
// найденная строка — ровно в одном из двух списков; id, строки которого нет,
// — ни в одном, и это не ошибка. Источник — из контекста (Source.Bind), без
// него — ErrSourceUnbound.
func DeleteUnleased(ctx context.Context, tx pgx.Tx, ids []string) (deleted, leased []string, err error) {
	s, ok := sourceFrom(ctx)
	if !ok {
		return nil, nil, ErrSourceUnbound
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id, `+noActiveLease+`
  FROM %s WHERE id = ANY($1) ORDER BY id FOR UPDATE`, tablename.Of(s.service, tablename.Outbox)), ids)
	if err != nil {
		return nil, nil, fmt.Errorf("feed: чтение аренды удаляемых строк: %w", err)
	}
	var free []string
	for rows.Next() {
		var (
			id     string
			isFree bool
		)
		if err := rows.Scan(&id, &isFree); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("feed: чтение аренды удаляемых строк: %w", err)
		}
		if isFree {
			free = append(free, id)
		} else {
			leased = append(leased, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("feed: чтение аренды удаляемых строк: %w", err)
	}
	if len(free) == 0 {
		return nil, leased, nil
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`,
		tablename.Of(s.service, tablename.Outbox)), free); err != nil {
		return nil, nil, fmt.Errorf("feed: удаление строк без аренды: %w", err)
	}
	return free, leased, nil
}
