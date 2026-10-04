// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/feed"
)

// committedIDs — id всех строк ленты после коммита, по возрастанию.
func (f *fixture) committedIDs(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT id FROM probe_notification_outbox ORDER BY id`)
	require.NoError(t, err)
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return got
}

// PutID отвечает идентификатором строки, которую записал: две постановки в
// одной транзакции — два разных id, и ровно они лежат в ленте после коммита.
// Чтения ленты вызывающим не требуется.
func TestPutIDReturnsTheIDOfTheRowItWrote(t *testing.T) {
	f := newFixture(t, true)
	tx := f.begin(t)
	first, err := feed.PutID(f.ctx, tx, helloDesc(), "a@example.invalid", hello())
	require.NoError(t, err)
	second, err := feed.PutID(f.ctx, tx, helloDesc(), "b@example.invalid", hello())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.ctx))

	id1, ok := first.ID()
	require.True(t, ok, "строка записана — id есть")
	id2, ok := second.ID()
	require.True(t, ok, "строка записана — id есть")
	require.NotEqual(t, id1, id2)
	require.True(t, ids.IsValidHyphen(id1, ids.PrefixNotificationHyphen), id1)

	want := []string{id1, id2}
	sort.Strings(want)
	require.Equal(t, want, f.committedIDs(t))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE id = $1 AND recipient_address = 'a@example.invalid'`, id1))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE id = $1 AND recipient_address = 'b@example.invalid'`, id2))
}

// Параллельные постановки в своих транзакциях: каждая получает id своей
// строки, а не «последней видимой» — множество ответов равно множеству строк.
func TestPutIDParallelPutsGetTheirOwnIDs(t *testing.T) {
	const n = 8
	f := newFixture(t, true)
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		got  []string
		errs []error
	)
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := func() error {
				tx := f.begin(t)
				q, err := feed.PutID(f.ctx, tx, helloDesc(), "user@example.invalid", hello())
				if err != nil {
					return err
				}
				if err := tx.Commit(f.ctx); err != nil {
					return err
				}
				id, ok := q.ID()
				if !ok {
					return errors.New("строка записана, а id нет")
				}
				mu.Lock()
				got = append(got, id)
				mu.Unlock()
				return nil
			}()
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Empty(t, errs)
	sort.Strings(got)
	require.Len(t, got, n)
	require.Equal(t, got, f.committedIDs(t))
}

// Флаг выключен, класс notice: ничего не записано — и ответ говорит это
// типом (ID() → "", false), а не пустой строкой на месте id. Близнец —
// включено: та же постановка даёт id.
func TestPutIDFlagOffNoticeReportsNoRow(t *testing.T) {
	f := newFixture(t, false)
	tx := f.begin(t)
	q, err := feed.PutID(f.ctx, tx, helloDesc(), "user@example.invalid", hello())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.ctx))
	id, ok := q.ID()
	require.False(t, ok, "флаг выключен — строки нет")
	require.Empty(t, id)
	require.Equal(t, 0, f.rows(t))

	on := fixtureOn(t, f.pool, true)
	tx = on.begin(t)
	q, err = feed.PutID(on.ctx, tx, helloDesc(), "user@example.invalid", hello())
	require.NoError(t, err)
	require.NoError(t, tx.Commit(on.ctx))
	id, ok = q.ID()
	require.True(t, ok)
	require.Equal(t, []string{id}, on.committedIDs(t))
}

// Сторож PutID — без id: отказ не несёт строки.
func TestPutIDGuardReportsNoRow(t *testing.T) {
	f := newFixture(t, true)
	tx := f.begin(t)
	q, err := feed.PutID(f.ctx, tx, helloDesc(), "user@example.invalid", feed.Values{})
	require.ErrorIs(t, err, feed.ErrAttrsInvalid)
	_, ok := q.ID()
	require.False(t, ok)
	require.NoError(t, tx.Commit(f.ctx))
	require.Equal(t, 0, f.rows(t))
}

// PutID без привязанного источника — отказ проводки и нулевой ответ.
func TestPutIDWithoutABoundSourceIsAWiringError(t *testing.T) {
	q, err := feed.PutID(context.Background(), nil, helloDesc(), "user@example.invalid", hello())
	require.ErrorIs(t, err, feed.ErrSourceUnbound)
	_, ok := q.ID()
	require.False(t, ok)
}
