// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/corelib/subscription"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/pgtest"
	"github.com/PRO-Robotech/corelib/subscription"
)

// emitNoPanic зовёт Emit и переводит панику в отказ пробы с её текстом: красное
// этой пробы — отказ, а не падение всего пакета.
func emitNoPanic(t *testing.T, j subscription.Journal, ctx context.Context, tx *journaltx.Tx, e subscription.Entry) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Emit паникует: %v", r)
		}
	}()
	return j.Emit(ctx, tx, e)
}

// TestEmitRefusesATransactionNotOpenedByBegin — Emit пишет только транзакцией
// помощника: nil, нулевой `journaltx.Tx` и `journaltx.Tx`, собранный поверх
// сырой транзакции в обход Begin, отвергаются [subscription.ErrNotHelperTx] до
// оператора, без паники и без строки журнала. Близнец — транзакция Begin той
// же записи — принимается.
func TestEmitRefusesATransactionNotOpenedByBegin(t *testing.T) {
	j := attributedJournal()
	pool := attributedPool(t, pgtest.NewDB(t))
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)})
	e := subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "CREATED", Payload: map[string]any{}}

	raw, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("сырая транзакция: %v", err)
	}
	defer func() { _ = raw.Rollback(ctx) }()

	forged := []struct {
		name string
		tx   *journaltx.Tx
	}{
		{"nil", nil},
		{"нулевой journaltx.Tx", &journaltx.Tx{}},
		{"journaltx.Tx поверх сырой транзакции", &journaltx.Tx{Tx: raw}},
	}
	for _, f := range forged {
		err := emitNoPanic(t, j, ctx, f.tx, e)
		if !errors.Is(err, subscription.ErrNotHelperTx) {
			t.Errorf("%s: отказ %v, ожидался ErrNotHelperTx", f.name, err)
		}
	}
	if n := journalRows(t, pool); n != 0 {
		t.Fatalf("строк журнала %d после отказов", n)
	}

	tx, err := journaltx.Begin(ctx, pool, journaltx.NewOptions(true))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := emitNoPanic(t, j, ctx, tx, e); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("близнец: транзакция Begin отвергнута: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if n := journalRows(t, pool); n != 1 {
		t.Fatalf("близнец дал строк %d, ожидалась 1", n)
	}
}

var errAnchorProbe = errors.New("anchor probe: payload carries no project")

// TestEmitAnchorFailureIsNamedAndWrapped — отображение, не сумевшее вывести
// якорь, и отображение, выведшее ДРУГОЙ якорь, — разные отказы: первый несёт
// ошибку отображения (`errors.Is`) и говорит «не выведен», второй говорит
// «расходится». Оба — [subscription.ErrEntryRefused], оба до оператора.
func TestEmitAnchorFailureIsNamedAndWrapped(t *testing.T) {
	j := attributedJournal()
	j.Storage.Project = subscription.ProjectFromMapping
	j.Storage.ProjectColumn = ""
	j.Mapping.Anchor = func(r subscription.Row) (string, error) {
		var m map[string]any
		if err := json.Unmarshal(r.Payload, &m); err != nil {
			return "", err
		}
		p, ok := m["projectId"].(string)
		if !ok {
			return "", fmt.Errorf("row %s: %w", r.ID, errAnchorProbe)
		}
		return p, nil
	}
	if err := j.Validate(); err != nil {
		t.Fatalf("объявление: %v", err)
	}
	pool := attributedPool(t, pgtest.NewDB(t))
	usr := operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)}

	err := writeVia(t, pool, j, usr, subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "CREATED",
		Payload: map[string]any{}})
	if !errors.Is(err, subscription.ErrEntryRefused) {
		t.Fatalf("якорь не выведен: отказ %v не несёт ErrEntryRefused", err)
	}
	if !errors.Is(err, errAnchorProbe) {
		t.Errorf("якорь не выведен: отказ %q теряет ошибку отображения", err)
	}
	if !strings.Contains(err.Error(), "не выведен") || strings.Contains(err.Error(), "расходится") {
		t.Errorf("якорь не выведен: отказ %q называет не ту причину", err)
	}

	err = writeVia(t, pool, j, usr, subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "CREATED",
		Payload: map[string]any{"projectId": "prj-2"}})
	if !errors.Is(err, subscription.ErrEntryRefused) || !strings.Contains(err.Error(), "расходится") {
		t.Errorf("якорь расходится: отказ %v", err)
	}
	if errors.Is(err, errAnchorProbe) {
		t.Errorf("якорь расходится: отказ %q несёт ошибку отображения, которой не было", err)
	}
	if n := journalRows(t, pool); n != 0 {
		t.Fatalf("строк журнала %d при отказах", n)
	}
}

// TestEventOfARowWithNullInitiatorCarriesNone — строка журнала с NULL в колонке
// инициатора (журнал, получивший колонку поверх прежних строк, где колонка
// nullable) не останавливает поток: её событие доставляется с пустым
// `initiator`, как у журнала без колонки, а следующая строка — со своим
// инициатором.
func TestEventOfARowWithNullInitiatorCarriesNone(t *testing.T) {
	j := attributedJournal()
	s := newStand(t, standOpts{journal: &j})
	pool := attributedPool(t, s.dsn)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `ALTER TABLE attributed_outbox ALTER COLUMN initiator DROP NOT NULL`); err != nil {
		t.Fatalf("колонка инициатора nullable: %v", err)
	}
	// Прежняя строка: писатель без помощника, настройки инициатора нет.
	if _, err := pool.Exec(ctx, `INSERT INTO attributed_outbox (resource_kind, resource_id, project_id, event_type, payload)
		VALUES ('Network', 'net-old', 'prj-1', 'CREATED', '{}'::jsonb)`); err != nil {
		t.Fatalf("прежняя строка: %v", err)
	}
	var isNull bool
	if err := pool.QueryRow(ctx, `SELECT initiator IS NULL FROM attributed_outbox WHERE resource_id = 'net-old'`).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("предпосылка: инициатор прежней строки NULL = %v (%v)", isNull, err)
	}
	usr := operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)}
	if err := writeVia(t, pool, j, usr, subscription.Entry{Kind: "Network", ID: "net-new", ProjectID: "prj-1", Change: "CREATED",
		Payload: map[string]any{}}); err != nil {
		t.Fatalf("новая строка: %v", err)
	}

	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sb := s.open(t, sctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING},
	})
	got := recvEvents(t, sb, 2)
	if got[0].GetResourceId() != "net-old" || got[0].GetInitiator() != "" {
		t.Errorf("прежняя строка: событие %s initiator=%q, ожидалось net-old с пустым", got[0].GetResourceId(), got[0].GetInitiator())
	}
	if want := "user:" + usr.ID; got[1].GetResourceId() != "net-new" || got[1].GetInitiator() != want {
		t.Errorf("новая строка: событие %s initiator=%q, ожидалось net-new с %q", got[1].GetResourceId(), got[1].GetInitiator(), want)
	}
}

// TestEmitRollsBackWithTheMutation — журнал и мутация в ОДНОЙ транзакции: откат
// мутации не оставляет ни ресурсной строки, ни строки журнала; близнец с
// коммитом оставляет обе (sub-owner-test-transaction).
func TestEmitRollsBackWithTheMutation(t *testing.T) {
	j := attributedJournal()
	pool := attributedPool(t, pgtest.NewDB(t))
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)})
	if _, err := pool.Exec(ctx, `CREATE TABLE probe_resource (id text PRIMARY KEY)`); err != nil {
		t.Fatalf("ресурсная таблица: %v", err)
	}
	count := func(q string) int {
		var n int
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("подсчёт: %v", err)
		}
		return n
	}

	for _, commit := range []bool{false, true} {
		id := fmt.Sprintf("net-commit-%t", commit)
		tx, err := journaltx.Begin(ctx, pool, journaltx.NewOptions(true))
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO probe_resource (id) VALUES ($1)`, id); err != nil {
			t.Fatalf("мутация: %v", err)
		}
		if err := j.Emit(ctx, tx, subscription.Entry{Kind: "Network", ID: id, ProjectID: "prj-1", Change: "CREATED",
			Payload: map[string]any{}}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
		if commit {
			err = tx.Commit(ctx)
		} else {
			err = tx.Rollback(ctx)
		}
		if err != nil {
			t.Fatalf("завершение (commit=%t): %v", commit, err)
		}
		want := 0
		if commit {
			want = 1
		}
		if n := count(fmt.Sprintf(`SELECT count(*) FROM probe_resource WHERE id = '%s'`, id)); n != want {
			t.Errorf("commit=%t: ресурсов %d, ожидалось %d", commit, n, want)
		}
		if n := count(fmt.Sprintf(`SELECT count(*) FROM attributed_outbox WHERE resource_id = '%s'`, id)); n != want {
			t.Errorf("commit=%t: строк журнала %d, ожидалось %d", commit, n, want)
		}
	}
}

// complaintLog — журнал процесса пробы: записи уровня Warn с их полями.
type complaintLog struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *complaintLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *complaintLog) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}
func (c *complaintLog) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *complaintLog) WithGroup(string) slog.Handler      { return c }

func (c *complaintLog) find(msg string) (slog.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

// unnamedDeletionMsg — постоянное сообщение жалобы на снятие без годного имени.
const unnamedDeletionMsg = "subscription: deleted row of a named kind goes without a name"

// TestUnnamedDeletionComplaintIsAFieldOfAConstantMessage — жалоба на снятие вида
// с именем без годного имени печатается постоянным сообщением, а сама жалоба —
// полем `complaint`: сообщение, собранное из жалобы, не отбирается по имени.
// Событие при этом доставляется без имени.
func TestUnnamedDeletionComplaintIsAFieldOfAConstantMessage(t *testing.T) {
	j := attributedJournal()
	logs := &complaintLog{}
	s := newStand(t, standOpts{journal: &j, logger: slog.New(logs)})
	pool := attributedPool(t, s.dsn)
	ctx := context.Background()
	// Строку пишет не Emit (он такую отверг бы): снятие с именем вне формы.
	if _, err := pool.Exec(ctx, `INSERT INTO attributed_outbox (resource_kind, resource_id, project_id, event_type, payload, initiator)
		VALUES ('Network', 'net-x', 'prj-1', 'DELETED', '{"name":"Net_X"}'::jsonb, 'user:probe')`); err != nil {
		t.Fatalf("строка: %v", err)
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sb := s.open(t, sctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING},
	})
	if ev := recvEvents(t, sb, 1)[0]; ev.GetName() != "" {
		t.Errorf("событие несёт имя %q вне формы", ev.GetName())
	}
	r, ok := logs.find(unnamedDeletionMsg)
	if !ok {
		t.Fatalf("записи с сообщением %q нет", unnamedDeletionMsg)
	}
	var complaint string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "complaint" {
			complaint = a.Value.String()
		}
		return true
	})
	if !strings.Contains(complaint, "DNS-label") {
		t.Errorf("поле complaint = %q, ожидалась жалоба на имя", complaint)
	}
}
