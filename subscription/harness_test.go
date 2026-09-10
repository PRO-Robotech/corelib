// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/listnarrow/narrowtest"
	"github.com/PRO-Robotech/corelib/pgtest"
	"github.com/PRO-Robotech/corelib/subscription"
)

// probeJournal — объявление владельца под схему `journalSchema`.
//
// Якорь берётся ИЗ ОТОБРАЖЕНИЯ (у журнала нет проектной колонки) — то есть
// проба стоит на форме трёх владельцев из четырёх, а не на самой удобной.
func probeJournal() subscription.Journal {
	return subscription.Journal{
		Channel: journalChannel,
		Storage: subscription.Storage{
			Table:          "probe_outbox",
			PositionColumn: "sequence_no",
			KindColumn:     "resource_kind",
			IDColumn:       "resource_id",
			ChangeColumn:   "event_type",
			PayloadColumn:  "payload",
			Project:        subscription.ProjectFromMapping,
			Retention:      subscription.RetainsEverything,
		},
		Mapping: subscription.Mapping{
			Kinds: map[string]subscription.Kind{
				"Network": {ObjectType: "vpc_network", Action: "vpc.networks.get"},
				"Subnet":  {ObjectType: "vpc_subnet", Action: "vpc.subnets.get"},
			},
			Changes: map[string]subscriptionv1.SubscriptionEvent_Change{
				"CREATED": subscriptionv1.SubscriptionEvent_CREATED,
				"UPDATED": subscriptionv1.SubscriptionEvent_UPDATED,
				"DELETED": subscriptionv1.SubscriptionEvent_DELETED,
			},
			Anchor: func(r subscription.Row) (string, error) {
				var m map[string]any
				if err := json.Unmarshal(r.Payload, &m); err != nil {
					return "", err
				}
				p, _ := m["projectId"].(string)
				return p, nil
			},
			State: func(r subscription.Row) (*anypb.Any, subscription.StateAbsence, error) {
				var m map[string]any
				if err := json.Unmarshal(r.Payload, &m); err != nil {
					return nil, subscription.StateAbsenceUnnamed, err
				}
				st, err := structpb.NewStruct(m)
				if err != nil {
					return nil, subscription.StateAbsenceUnnamed, err
				}
				packed, err := anypb.New(st)
				if err != nil {
					return nil, subscription.StateAbsenceUnnamed, err
				}
				return packed, subscription.StateAbsenceUnnamed, nil
			},
		},
	}
}

func probeProjectGate() subscription.ProjectGate {
	return subscription.ProjectGate{
		ObjectType:     "project",
		Action:         "resourcemanager.projects.get",
		Relations:      []string{"v_get"},
		NotFoundFormat: "Project %s not found",
	}
}

// stand — поднятый сервер подписки за настоящим gRPC поверх bufconn.
//
// Поверх настоящего транспорта, а не поддельного потока: предмет проб — потолок
// одновременных потоков, обрыв соединения и порядок отказов, и все три
// наблюдаемы только там, где поток настоящий.
type stand struct {
	dsn    string
	client subscriptionv1.InternalSubscriptionServiceClient
	srv    *grpc.Server
}

type standOpts struct {
	narrower   *listnarrow.Narrower
	maxStreams int
	idlePoll   time.Duration
	budget     time.Duration
	journal    *subscription.Journal
	caller     context.Context
	// logger — журнал процесса, когда проба УТВЕРЖДАЕТ о том, что он напечатал.
	// Ноль отправляет вывод в никуда: прочим пробам он не нужен, а печатать его
	// в поток проб значило бы утопить их отказы в чужих строках.
	logger *slog.Logger
}

func newStand(t testing.TB, o standOpts) *stand {
	t.Helper()

	dsn := pgtest.NewDB(t)

	j := probeJournal()
	if o.journal != nil {
		j = *o.journal
	}
	n := o.narrower
	if n == nil {
		n = narrowtest.AllowingAll()
	}
	if o.maxStreams == 0 {
		o.maxStreams = 8
	}
	if o.idlePoll == 0 {
		o.idlePoll = 150 * time.Millisecond
	}
	if o.budget == 0 {
		o.budget = 30 * time.Second
	}
	caller := o.caller
	if caller == nil {
		caller = narrowtest.Caller()
	}

	log := o.logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	gate := probeProjectGate()
	if j.Storage.Project == subscription.ProjectAbsent {
		gate = subscription.ProjectGate{}
	}

	server, err := subscription.NewServer(subscription.Config{
		Journal:      j,
		DSN:          dsn,
		Narrower:     n,
		ProjectGate:  gate,
		MaxStreams:   o.maxStreams,
		StreamBudget: o.budget,
		IdlePoll:     o.idlePoll,
		Logger:       log,
	})
	if err != nil {
		t.Fatalf("сервер не поднялся: %v", err)
	}

	lis := bufconn.Listen(1 << 20)
	// Личность вызывающего кладёт звено — там же, где её кладёт боевая цепочка.
	// Класть её мимо транспорта значило бы проверять сервер на входе, которого
	// в бою не бывает.
	srv := grpc.NewServer(grpc.StreamInterceptor(
		func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, principalStream{ServerStream: ss, ctx: mergeCancel(caller, ss.Context())})
		}))
	subscriptionv1.RegisterInternalSubscriptionServiceServer(srv, server)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("клиент не собрался: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &stand{dsn: dsn, client: subscriptionv1.NewInternalSubscriptionServiceClient(conn), srv: srv}
}

// principalStream — поток с подменённым контекстом: несёт личность вызывающего и
// ОТМЕНУ настоящего потока. Взять один контекст без другого нельзя: без личности
// сервер отказывает по личности, без отмены проба не смогла бы закрыть поток.
type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s principalStream) Context() context.Context { return s.ctx }

// mergeCancel — значения из `values`, отмена из `cancel`.
func mergeCancel(values, cancel context.Context) context.Context {
	ctx, stop := context.WithCancel(values)
	go func() {
		<-cancel.Done()
		stop()
	}()
	return ctx
}

// emit вставляет строку журнала своей транзакцией и возвращает её номер.
func (s *stand) emit(t testing.TB, kind, id, change, projectID string) int64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		t.Fatalf("соединение: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var seq int64
	payload := `{"projectId":"` + projectID + `","id":"` + id + `"}`
	err = conn.QueryRow(ctx,
		`INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
		 VALUES ($1,$2,$3,$4::jsonb) RETURNING sequence_no`,
		kind, id, change, payload).Scan(&seq)
	if err != nil {
		t.Fatalf("вставка: %v", err)
	}
	return seq
}

// sub — открытый поток вместе с ЕДИНСТВЕННЫМ его читателем.
//
// # Почему читатель один, а не Recv из каждой проверки
//
// `Recv` необратим: снятое им сообщение вернуть в поток нельзя. Утверждение «в
// потоке больше ничего нет» естественно писать через `Recv` с таймаутом — и
// именно оно СЪЕДАЕТ следующее событие, приехавшее позже. Проба тогда краснеет
// на исправном сервере, а объясняется это часами (так и вышло: два подслучая
// WATCH-1-31 упали по этой причине, а не по предмету).
//
// Поэтому поток читает одна горутина, а пробы утверждают о ПРОЧИТАННОМ.
type sub struct {
	opened *subscriptionv1.SubscriptionOpened
	events chan *subscriptionv1.SubscriptionEvent
	fail   chan error
}

// open открывает поток, снимает служебное сообщение и заводит его читателя.
func (s *stand) open(t testing.TB, ctx context.Context, req *subscriptionv1.SubscriptionRequest) *sub {
	t.Helper()
	strm, err := s.client.Subscribe(ctx, req)
	if err != nil {
		t.Fatalf("подписка не открылась: %v", err)
	}
	msg, err := strm.Recv()
	if err != nil {
		t.Fatalf("служебное сообщение не пришло: %v", err)
	}
	opened := msg.GetOpened()
	if opened == nil {
		t.Fatalf("ПЕРВЫМ пришло не служебное сообщение, а %T", msg.GetMessage())
	}

	out := &sub{
		opened: opened,
		events: make(chan *subscriptionv1.SubscriptionEvent, 256),
		fail:   make(chan error, 1),
	}
	go func() {
		for {
			m, rerr := strm.Recv()
			if rerr != nil {
				out.fail <- rerr
				close(out.events)
				return
			}
			if ev := m.GetEvent(); ev != nil {
				out.events <- ev
				continue
			}
			out.fail <- errSecondOpened
			close(out.events)
			return
		}
	}()
	return out
}

// errSecondOpened — служебное сообщение приходит ПЕРВЫМ и ровно один раз.
var errSecondOpened = errors.New("служебное сообщение пришло не первым или повторно")

// recvEvents снимает ровно n событий либо валит пробу по сроку.
func recvEvents(t testing.TB, sb *sub, n int) []*subscriptionv1.SubscriptionEvent {
	t.Helper()
	out := make([]*subscriptionv1.SubscriptionEvent, 0, n)
	for len(out) < n {
		select {
		case ev, ok := <-sb.events:
			if !ok {
				t.Fatalf("получено %d событий из %d, поток закрылся: %v", len(out), n, <-sb.fail)
			}
			out = append(out, ev)
		case <-time.After(20 * time.Second):
			t.Fatalf("получено %d событий из %d, дальше тишина", len(out), n)
		}
	}
	return out
}

// requireQuiet — в потоке БОЛЬШЕ НИЧЕГО НЕТ.
//
// Утверждать это можно только по сроку, и срок обязан быть назван: молчание
// доказывается ожиданием, а не отсутствием попытки прочитать. Съесть событие
// оно не может — читает не оно (см. [sub]).
func requireQuiet(t testing.TB, sb *sub) {
	t.Helper()
	select {
	case ev, ok := <-sb.events:
		if ok {
			t.Fatalf("поток отдал лишнее событие: предмет %q", ev.GetResourceId())
		}
	case <-time.After(1500 * time.Millisecond):
	}
}

// exec выполняет служебный оператор против журнала пробы.
func (s *stand) exec(t testing.TB, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		t.Fatalf("соединение: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("оператор %q: %v", sql, err)
	}
}
