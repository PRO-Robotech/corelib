// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/corelib/subscription"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/pgtest"
	"github.com/PRO-Robotech/corelib/subscription"
)

// attributedJournal — объявление под `attributedJournalSchema`: колонки
// инициатора и времени названы, у каждого вида объявлены форма имени и якорь.
func attributedJournal() subscription.Journal {
	return subscription.Journal{
		Channel: attributedChannel,
		Storage: subscription.Storage{
			Table:            "attributed_outbox",
			PositionColumn:   "sequence_no",
			KindColumn:       "resource_kind",
			IDColumn:         "resource_id",
			ChangeColumn:     "event_type",
			PayloadColumn:    "payload",
			ProjectColumn:    "project_id",
			Project:          subscription.ProjectInColumn,
			Retention:        subscription.RetainsEverything,
			InitiatorColumn:  "initiator",
			OccurredAtColumn: "created_at",
		},
		Mapping: subscription.Mapping{
			Kinds: map[string]subscription.Kind{
				"Network":     {ObjectType: "vpc_network", Action: "vpc.networks.get", NameForm: subscription.NameFormDNS, Scope: subscription.ScopeProject},
				"Repository":  {ObjectType: "registry_repository", Action: "registry.repositories.get", NameForm: subscription.NameFormNone, Scope: subscription.ScopeProject},
				"AddressPool": {ObjectType: "vpc_address_pool", Action: "vpc.addressPools.get", NameForm: subscription.NameFormDNS, Scope: subscription.ScopeCluster},
				"Legacy":      {ObjectType: "vpc_legacy", Action: "vpc.legacy.get"},
			},
			Changes: map[string]subscriptionv1.SubscriptionEvent_Change{
				"CREATED": subscriptionv1.SubscriptionEvent_CREATED,
				"UPDATED": subscriptionv1.SubscriptionEvent_UPDATED,
				"MOVED":   subscriptionv1.SubscriptionEvent_UPDATED,
				"DELETED": subscriptionv1.SubscriptionEvent_DELETED,
			},
			State: func(r subscription.Row) (*anypb.Any, subscription.StateAbsence, error) {
				if r.Change == "DELETED" {
					return nil, subscription.StateNotProduced, nil
				}
				var m map[string]any
				if err := json.Unmarshal(r.Payload, &m); err != nil {
					return nil, subscription.StateAbsenceUnnamed, err
				}
				st, err := structpb.NewStruct(m)
				if err != nil {
					return nil, subscription.StateAbsenceUnnamed, err
				}
				packed, err := anypb.New(st)
				return packed, subscription.StateAbsenceUnnamed, err
			},
		},
	}
}

func attributedPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, pool)
	return pool
}

// writeVia пишет одну строку журнала функцией фундамента в транзакции помощника
// от имени принципала p.
func writeVia(t *testing.T, pool *pgxpool.Pool, j subscription.Journal, p operations.Principal, e subscription.Entry) error {
	t.Helper()
	ctx := operations.WithPrincipal(context.Background(), p)
	tx, err := journaltx.Begin(ctx, pool, journaltx.NewOptions(true))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := j.Emit(ctx, tx, e); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

func journalRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM attributed_outbox`).Scan(&n); err != nil {
		t.Fatalf("подсчёт строк: %v", err)
	}
	return n
}

// TestEventCarriesInitiatorOccurredAtAndDeletedName — событие несёт инициатора
// строки журнала, её время, усечённое до секунды, и имя только у снятия вида с
// формой имени DNS-метки (З2; NTF3-57, NTF3-59, близнец УК3-13 (б)).
func TestEventCarriesInitiatorOccurredAtAndDeletedName(t *testing.T) {
	j := attributedJournal()
	s := newStand(t, standOpts{journal: &j})
	pool := attributedPool(t, s.dsn)

	usr := operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)}
	sva := operations.Principal{Type: "service_account", ID: ids.NewID(ids.PrefixServiceAccount)}
	comp := operations.Principal{Type: "user", ID: "system.storage-reconciler"}

	writes := []struct {
		p    operations.Principal
		e    subscription.Entry
		init string
		name string
	}{
		{usr, subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "CREATED",
			Payload: map[string]any{"id": "net-a", "name": "net-a"}}, "user:" + usr.ID, ""},
		{sva, subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "DELETED",
			Payload: map[string]any{"id": "net-a", "name": "net-a"}}, "service_account:" + sva.ID, "net-a"},
		{comp, subscription.Entry{Kind: "Repository", ID: "reg-1/app", ProjectID: "prj-1", Change: "DELETED",
			Payload: map[string]any{"id": "reg-1/app", "name": "app"}}, "system:storage-reconciler", ""},
		{usr, subscription.Entry{Kind: "AddressPool", ID: "apl-1", Change: "DELETED",
			Payload: map[string]any{"id": "apl-1", "name": "pool-1"}}, "user:" + usr.ID, "pool-1"},
	}
	for i, w := range writes {
		if err := writeVia(t, pool, j, w.p, w.e); err != nil {
			t.Fatalf("запись %d: %v", i, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING},
	})
	got := recvEvents(t, sb, len(writes))

	rows, err := pool.Query(context.Background(), `SELECT created_at FROM attributed_outbox ORDER BY sequence_no`)
	if err != nil {
		t.Fatalf("чтение времени строк: %v", err)
	}
	var stamps []time.Time
	for rows.Next() {
		var ts time.Time
		if err := rows.Scan(&ts); err != nil {
			t.Fatalf("время строки: %v", err)
		}
		stamps = append(stamps, ts)
	}
	rows.Close()
	if len(stamps) != len(writes) {
		t.Fatalf("строк журнала %d, ожидалось %d", len(stamps), len(writes))
	}

	for i, w := range writes {
		ev := got[i]
		if ev.GetInitiator() != w.init {
			t.Errorf("событие %d (%s %s): initiator %q, ожидался %q", i, w.e.Kind, w.e.Change, ev.GetInitiator(), w.init)
		}
		if ev.GetName() != w.name {
			t.Errorf("событие %d (%s %s): name %q, ожидалось %q", i, w.e.Kind, w.e.Change, ev.GetName(), w.name)
		}
		ts := ev.GetOccurredAt()
		if ts == nil {
			t.Errorf("событие %d: occurred_at не выставлен", i)
			continue
		}
		if ts.GetNanos() != 0 {
			t.Errorf("событие %d: occurred_at несёт дробную часть секунды (%d нс)", i, ts.GetNanos())
		}
		if want := stamps[i].Truncate(time.Second); !ts.AsTime().Equal(want) {
			t.Errorf("событие %d: occurred_at %s, время строки журнала, усечённое до секунды, %s", i, ts.AsTime(), want)
		}
	}
}

// TestEventOfAJournalWithoutAttributionColumnsCarriesNone — журнал, не
// назвавший колонок инициатора и времени (форма до миграций NTF-3), событий с
// этими полями не производит: поля пусты, а не заполнены выдумкой.
func TestEventOfAJournalWithoutAttributionColumnsCarriesNone(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.emit(t, "Network", "net00000000000000001", "DELETED", "prj-a")
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING},
	})
	ev := recvEvents(t, sb, 1)[0]
	if ev.GetInitiator() != "" || ev.GetOccurredAt() != nil || ev.GetName() != "" {
		t.Errorf("событие журнала без объявлений атрибуции несёт initiator=%q occurred_at=%v name=%q",
			ev.GetInitiator(), ev.GetOccurredAt(), ev.GetName())
	}
}

// TestEmitRefusesWhatTheDeclarationForbids — функция фундамента отвергает
// запись, противоречащую объявлению вида, ДО оператора; у каждого отказа —
// близнец, отличающийся одним фактом и записанный (З2; УК3-13 (а), (б)).
func TestEmitRefusesWhatTheDeclarationForbids(t *testing.T) {
	j := attributedJournal()
	pool := attributedPool(t, pgtest.NewDB(t))
	usr := operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)}

	cases := []struct {
		name    string
		refused subscription.Entry
		twin    subscription.Entry
		mention string
	}{
		{"проектный вид с пустым якорем",
			subscription.Entry{Kind: "Network", ID: "net-a", Change: "CREATED", Payload: map[string]any{}},
			subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "CREATED", Payload: map[string]any{}},
			"Network"},
		{"вид уровня кластера с якорем",
			subscription.Entry{Kind: "AddressPool", ID: "apl-1", ProjectID: "prj-1", Change: "CREATED", Payload: map[string]any{}},
			subscription.Entry{Kind: "AddressPool", ID: "apl-1", Change: "CREATED", Payload: map[string]any{}},
			"AddressPool"},
		{"снятие вида с именем без имени",
			subscription.Entry{Kind: "Network", ID: "net-b", ProjectID: "prj-1", Change: "DELETED", Payload: map[string]any{}},
			subscription.Entry{Kind: "Network", ID: "net-b", ProjectID: "prj-1", Change: "DELETED", Payload: map[string]any{"name": "net-b"}},
			"Network"},
		{"снятие вида с именем, имя не DNS-метка",
			subscription.Entry{Kind: "Network", ID: "net-c", ProjectID: "prj-1", Change: "DELETED", Payload: map[string]any{"name": "Net_C"}},
			subscription.Entry{Kind: "Network", ID: "net-c", ProjectID: "prj-1", Change: "DELETED", Payload: map[string]any{"name": "net-c"}},
			"Network"},
		{"снятие вида с именем, имя не строка",
			subscription.Entry{Kind: "Network", ID: "net-d", ProjectID: "prj-1", Change: "DELETED", Payload: map[string]any{"name": 7}},
			subscription.Entry{Kind: "Network", ID: "net-d", ProjectID: "prj-1", Change: "DELETED", Payload: map[string]any{"name": "net-d"}},
			"Network"},
		{"вид без объявления формы имени и якоря",
			subscription.Entry{Kind: "Legacy", ID: "leg-1", ProjectID: "prj-1", Change: "CREATED", Payload: map[string]any{}},
			subscription.Entry{Kind: "Network", ID: "leg-1", ProjectID: "prj-1", Change: "CREATED", Payload: map[string]any{}},
			"Legacy"},
		{"вид вне словаря",
			subscription.Entry{Kind: "Subnet", ID: "sub-1", ProjectID: "prj-1", Change: "CREATED", Payload: map[string]any{}},
			subscription.Entry{Kind: "Network", ID: "sub-1", ProjectID: "prj-1", Change: "CREATED", Payload: map[string]any{}},
			"Subnet"},
		{"род изменения вне словаря",
			subscription.Entry{Kind: "Network", ID: "net-e", ProjectID: "prj-1", Change: "FAILED", Payload: map[string]any{}},
			subscription.Entry{Kind: "Network", ID: "net-e", ProjectID: "prj-1", Change: "MOVED", Payload: map[string]any{}},
			"FAILED"},
	}
	for _, c := range cases {
		before := journalRows(t, pool)
		err := writeVia(t, pool, j, usr, c.refused)
		if err == nil {
			t.Errorf("%s: запись принята", c.name)
			continue
		}
		if !errors.Is(err, subscription.ErrEntryRefused) {
			t.Errorf("%s: отказ %v не несёт ErrEntryRefused", c.name, err)
		}
		if !strings.Contains(err.Error(), c.mention) {
			t.Errorf("%s: отказ %q не называет %q", c.name, err.Error(), c.mention)
		}
		if after := journalRows(t, pool); after != before {
			t.Errorf("%s: строк журнала %d → %d при отказе", c.name, before, after)
		}
		if err := writeVia(t, pool, j, usr, c.twin); err != nil {
			t.Errorf("%s: близнец отвергнут: %v", c.name, err)
		}
		if after := journalRows(t, pool); after != before+1 {
			t.Errorf("%s: близнец дал строк %d, ожидалась 1", c.name, after-before)
		}
	}
}

// TestEmitAnchorMustMatchTheMapping — у журнала с якорем из отображения
// функция фундамента проверяет, что отображение выведет тот же якорь, что назван
// записью: иначе строка ляжет, а доставить её будет не по чему.
func TestEmitAnchorMustMatchTheMapping(t *testing.T) {
	j := attributedJournal()
	j.Storage.Project = subscription.ProjectFromMapping
	j.Storage.ProjectColumn = ""
	j.Mapping.Anchor = func(r subscription.Row) (string, error) {
		var m map[string]any
		if err := json.Unmarshal(r.Payload, &m); err != nil {
			return "", err
		}
		p, _ := m["projectId"].(string)
		return p, nil
	}
	if err := j.Validate(); err != nil {
		t.Fatalf("объявление: %v", err)
	}
	pool := attributedPool(t, pgtest.NewDB(t))
	usr := operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)}

	err := writeVia(t, pool, j, usr, subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "CREATED",
		Payload: map[string]any{"projectId": "prj-2"}})
	if !errors.Is(err, subscription.ErrEntryRefused) {
		t.Fatalf("якорь записи и отображения разошлись, отказ %v", err)
	}
	if n := journalRows(t, pool); n != 0 {
		t.Fatalf("строк журнала %d при отказе", n)
	}
	if err := writeVia(t, pool, j, usr, subscription.Entry{Kind: "Network", ID: "net-a", ProjectID: "prj-1", Change: "CREATED",
		Payload: map[string]any{"projectId": "prj-1"}}); err != nil {
		t.Fatalf("близнец отвергнут: %v", err)
	}
}
