// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// c5_integration_test.go — интеграционные пробы ленты C5 (приёмка NTF-5
// kacho#2924, Р5, Р13, Р14, Р15, §3 З19; замысел issue-2924 З8 п.5, З10,
// З12 п.1, З24, З28; маршрут C5, CX5-05, CX5-52, CX5-54, CX5-55, CX5-57,
// CX5-60, CX5-61).
//
// КОНТРАКТ, который зовут пробы (на базе полосы его нет, пакет проб не
// собирается, и каждая ошибка сборки — имя этого контракта):
//
//	const feed.ClassObligation feed.Class = "obligation"
//	feed.Values.ThreadKey *string            // нет ключа — nil (CX5-52 (а))
//	feed.TemplateDesc{Class: ClassObligation, TTL: 0} — срока нет (З10 п.2)
//
//	func feed.Supersede(ctx context.Context, tx pgx.Tx, ids []string) ([]string, error)
//	func feed.DeleteUnleased(ctx context.Context, tx pgx.Tx, ids []string) (deleted, leased []string, err error)
//	    // источник (префикс таблиц) — из контекста Source.Bind, как у Put
//
//	type feed.OutcomeObserver interface {
//	    ObserveOutcome(ctx context.Context, tx pgx.Tx, row feed.AckedRow) error
//	}
//	type feed.AckedRow struct {
//	    ID        string
//	    Class     feed.Class
//	    Template  string
//	    Outcome   feed.Outcome
//	    ThreadKey *string
//	}
//	var feed.NopObserver feed.OutcomeObserver
//	func feed.ComposeObservers(obs ...feed.OutcomeObserver) feed.OutcomeObserver
//	var feed.ErrNilObserver error
//	feed.ServerConfig.Observer feed.OutcomeObserver   // nil — ErrNilObserver
//
//	type feed.LocalConfig struct {
//	    Module, Service string
//	    DB              <пул: *pgxpool.Pool подходит>
//	    Keyring         *feed.Keyring
//	    Observer        feed.OutcomeObserver          // nil — ErrNilObserver
//	    Metrics         prometheus.Registerer
//	}
//	func feed.NewLocal(cfg feed.LocalConfig) (*feed.Local, error)
//	func (*feed.Local) Claim(ctx context.Context, classes []feed.Class, max int) ([]feed.Claimed, error)
//	type feed.Claimed struct {
//	    ID, LeaseToken, Template string
//	    Class                    feed.Class
//	    ThreadKey                *string
//	    Attrs                    map[string]string
//	}
//	func (*feed.Local) Ack(ctx context.Context, a feed.Ack) error
//	type feed.Ack struct {
//	    ID, LeaseToken string
//	    Outcome        feed.Outcome
//	    DeferFor       time.Duration
//	}
//	var feed.ErrLeaseLost error                         // Local.Ack: аренда утрачена
//
// Порядок каждой пробы несущий: сначала фикстура и её положительный
// контроль, потом проба возможности.
package feed_test

import (
	"context"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/notify/feed/schema"
	"github.com/PRO-Robotech/corelib/notify/form"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// recObserver — наблюдатель-фикстура: записывает каждый вызов; gate, если
// задан, держит первый вызов (и с ним транзакцию записи исхода) до закрытия
// release.
type recObserver struct {
	mu      sync.Mutex
	calls   []feed.AckedRow
	entered chan struct{}
	release chan struct{}
}

func (o *recObserver) ObserveOutcome(_ context.Context, _ pgx.Tx, row feed.AckedRow) error {
	o.mu.Lock()
	o.calls = append(o.calls, row)
	first := len(o.calls) == 1
	o.mu.Unlock()
	if first && o.release != nil {
		close(o.entered)
		<-o.release
	}
	return nil
}

func (o *recObserver) seen() []feed.AckedRow {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]feed.AckedRow(nil), o.calls...)
}

// c5Stand — источник probe, сервер ленты и точка входа в процессе над одной
// базой; у каждой точки свой наблюдатель-фикстура.
type c5Stand struct {
	*fixture
	server   *feed.Server
	local    *feed.Local
	srvObs   *recObserver
	localObs *recObserver
}

type c5Opts struct {
	putRing, localRing *feed.Keyring
	localObs           *recObserver
}

func newC5Stand(t *testing.T, o c5Opts) *c5Stand {
	t.Helper()
	r := ring(t, key(1, 0xA1))
	if o.putRing == nil {
		o.putRing = r
	}
	if o.localRing == nil {
		o.localRing = r
	}
	if o.localObs == nil {
		o.localObs = &recObserver{}
	}
	pool, err := pgxpool.New(context.Background(), pgtest.NewDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	f := fixtureSealed(t, pool, true, o.putRing)
	srvObs := &recObserver{}
	srv, err := feed.NewServer(feed.ServerConfig{
		Module: "probe", Service: "probe", Enabled: enabled(t, true), DB: pool,
		Keyring: r, Metrics: f.reg, Observer: srvObs,
	})
	require.NoError(t, err)
	local, err := feed.NewLocal(feed.LocalConfig{
		Module: "probe", Service: "probe", DB: pool, Keyring: o.localRing,
		Observer: o.localObs, Metrics: f.reg,
	})
	require.NoError(t, err)
	return &c5Stand{fixture: f, server: srv, local: local, srvObs: srvObs, localObs: o.localObs}
}

// putRow ставит одно письмо Put и возвращает id строки — тот, что вернул
// оператор постановки.
func (s *c5Stand) putRow(t *testing.T, d feed.TemplateDesc, to string, v feed.Values) string {
	t.Helper()
	tx := s.begin(t)
	q, err := feed.PutID(s.ctx, tx, d, to, v)
	require.NoError(t, err, "ФИКСТУРА: постановка %s", d.Name)
	require.NoError(t, tx.Commit(s.ctx))
	id, ok := q.ID()
	require.True(t, ok, "ФИКСТУРА: строка %s не поставлена", d.Name)
	return id
}

func (s *c5Stand) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(), q, args...)
	require.NoError(t, err)
}

func (s *c5Stand) state(t *testing.T, id string) string {
	t.Helper()
	var st string
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT state FROM probe_notification_outbox WHERE id = $1`, id).Scan(&st))
	return st
}

// serverClaim — Claim сервера ленты по классу notice.
func (s *c5Stand) serverClaim(t *testing.T, max uint32) []*notifyv1.ClaimedNotification {
	t.Helper()
	resp, err := s.server.Claim(context.Background(), &notifyv1.ClaimRequest{
		Max: max, Classes: []notifyv1.NotificationClass{notifyv1.NotificationClass_NOTICE},
	})
	require.NoError(t, err)
	return resp.GetNotifications()
}

func (s *c5Stand) serverAck(id, token string, kind notifyv1.OutcomeKind, reason notifyv1.OutcomeReason, deferFor time.Duration) error {
	req := &notifyv1.AckRequest{Id: id, LeaseToken: token, Outcome: &notifyv1.Outcome{Kind: kind, Reason: reason}}
	if deferFor > 0 {
		req.DeferFor = durationpb.New(deferFor)
	}
	_, err := s.server.Ack(context.Background(), req)
	return err
}

func (s *c5Stand) localClaim(t *testing.T, max int, classes ...feed.Class) []feed.Claimed {
	t.Helper()
	got, err := s.local.Claim(context.Background(), classes, max)
	require.NoError(t, err)
	return got
}

func claimedIDs(cs []feed.Claimed) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	sort.Strings(out)
	return out
}

func sorted(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func orEmpty(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func threadKey(k string) *string { return &k }

func withThread(v feed.Values, k string) feed.Values {
	v.ThreadKey = threadKey(k)
	return v
}

// obligationDesc — шаблон класса obligation: срока нет, лимитов нет (Р12).
func obligationDesc() feed.TemplateDesc {
	return feed.TemplateDesc{
		Name: "notice-outage-started", Class: feed.ClassObligation, SchemaRev: 1, Recipient: feed.RecipientAddress,
		Attrs: []feed.AttrDesc{{Name: "starts_at", Kind: form.KindTimestamp, Presence: feed.PresenceRequired}},
	}
}

func obligationValues() feed.Values {
	return attrs("starts_at", time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
}

// supersededReason — причина клетки SUPERSEDED из ЕДИНСТВЕННОЙ таблицы
// «состояние × причина», а не выписанная пробой.
func supersededReason(t *testing.T) string {
	t.Helper()
	rs := schema.OutcomePairs()["superseded"]
	require.Len(t, rs, 1, "у состояния superseded ждали одну причину в таблице схемы, есть %v", rs)
	return rs[0]
}

// requireLeaseLost — FAILED_PRECONDITION LEASE_LOST сервера ленты.
func requireLeaseLost(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "не статус gRPC: %v", err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			require.Equal(t, feed.ReasonLeaseLost, info.GetReason(), "%v", err)
			return
		}
	}
	t.Fatalf("у отказа нет ErrorInfo: %v", err)
}

// З28 п.3: наблюдатель — обязательное поле конструктора обеих точек входа;
// nil — ErrNilObserver до старта. Близнец — явное значение NopObserver.
func TestC5_ObserverIsARequiredFieldOfBothEntryPoints(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), pgtest.NewDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	r := ring(t, key(1, 0xA1))
	srvCfg := func(o feed.OutcomeObserver) feed.ServerConfig {
		return feed.ServerConfig{Module: "probe", Service: "probe", Enabled: enabled(t, true), DB: pool,
			Keyring: r, Metrics: prometheus.NewRegistry(), Observer: o}
	}
	localCfg := func(o feed.OutcomeObserver) feed.LocalConfig {
		return feed.LocalConfig{Module: "probe", Service: "probe", DB: pool, Keyring: r,
			Metrics: prometheus.NewRegistry(), Observer: o}
	}
	_, err = feed.NewServer(srvCfg(feed.NopObserver))
	require.NoError(t, err, "близнец: NewServer с NopObserver")
	_, err = feed.NewLocal(localCfg(feed.NopObserver))
	require.NoError(t, err, "близнец: NewLocal с NopObserver")
	_, err = feed.NewServer(srvCfg(nil))
	require.ErrorIs(t, err, feed.ErrNilObserver)
	_, err = feed.NewLocal(localCfg(nil))
	require.ErrorIs(t, err, feed.ErrNilObserver)
}

// З21 п.1, З28 п.3: ComposeObservers зовёт каждого наблюдателя по разу.
func TestC5_ComposeObserversCallsEach(t *testing.T) {
	a, b := &recObserver{}, &recObserver{}
	s := newC5Stand(t, c5Opts{localObs: &recObserver{}})
	local, err := feed.NewLocal(feed.LocalConfig{Module: "probe", Service: "probe", DB: s.pool,
		Keyring: ring(t, key(1, 0xA1)), Metrics: prometheus.NewRegistry(), Observer: feed.ComposeObservers(a, b)})
	require.NoError(t, err)
	id := s.putRow(t, helloDesc(), "a@example.invalid", hello())
	got, err := local.Claim(context.Background(), []feed.Class{feed.ClassNotice}, 10)
	require.NoError(t, err)
	require.Equal(t, []string{id}, claimedIDs(got))
	require.NoError(t, local.Ack(context.Background(), feed.Ack{ID: id, LeaseToken: got[0].LeaseToken,
		Outcome: feed.Outcome{Kind: feed.KindSent}}))
	require.Len(t, a.seen(), 1)
	require.Len(t, b.seen(), 1)
}

// З10 п.3 (пятый читатель срока — условие Claim), §3 З19: строка obligation
// без срока выдаётся Claim; строка notice с истёкшим сроком не выдаётся.
// Положительный контроль — свежая строка notice выдаётся тем же Claim.
func TestC5_ObligationRowWithoutExpiryIsClaimed(t *testing.T) {
	s := newC5Stand(t, c5Opts{})
	fresh := s.putRow(t, helloDesc(), "fresh@example.invalid", hello())
	stale := s.putRow(t, helloDesc(), "stale@example.invalid", hello())
	s.exec(t, `UPDATE probe_notification_outbox SET expires_at = now() - interval '1 second' WHERE id = $1`, stale)
	ob := s.putRow(t, obligationDesc(), "ob@example.invalid", obligationValues())

	var hasExpiry bool
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT expires_at IS NOT NULL FROM probe_notification_outbox WHERE id = $1`, ob).Scan(&hasExpiry))
	require.False(t, hasExpiry, "строка obligation поставлена со сроком")

	got := s.localClaim(t, 10, feed.ClassObligation, feed.ClassNotice)
	require.Equal(t, sorted(fresh, ob), claimedIDs(got), "выдано не то: obligation без срока и свежая notice — да, истёкшая notice — нет")
}

// З12 п.1 (CX5-52 (б)): две строки без ключа нити один Claim выдаёт обе; две
// строки одного ключа — одну (первую по порядку постановки), вторая — после
// исхода первой.
func TestC5_ThreadHeadInClaim(t *testing.T) {
	s := newC5Stand(t, c5Opts{})
	n1 := s.putRow(t, helloDesc(), "n1@example.invalid", hello())
	n2 := s.putRow(t, helloDesc(), "n2@example.invalid", hello())
	require.Equal(t, sorted(n1, n2), claimedIDs(s.localClaim(t, 10, feed.ClassNotice)),
		"строки без ключа нити: один Claim выдаёт обе")

	t1 := s.putRow(t, helloDesc(), "u@example.invalid", withThread(hello(), "ntc-1/usr-1"))
	t2 := s.putRow(t, helloDesc(), "u@example.invalid", withThread(hello(), "ntc-1/usr-1"))
	got := s.localClaim(t, 10, feed.ClassNotice)
	require.Equal(t, []string{t1}, claimedIDs(got), "строки одной нити: выдаётся только голова")
	require.NotNil(t, got[0].ThreadKey)
	require.Equal(t, "ntc-1/usr-1", *got[0].ThreadKey)
	require.Empty(t, s.localClaim(t, 10, feed.ClassNotice), "вторая строка нити, пока первая в полёте, не выдаётся")

	require.NoError(t, s.local.Ack(context.Background(), feed.Ack{ID: t1, LeaseToken: got[0].LeaseToken,
		Outcome: feed.Outcome{Kind: feed.KindSent}}))
	require.Equal(t, []string{t2}, claimedIDs(s.localClaim(t, 10, feed.ClassNotice)),
		"после исхода первой выдаётся вторая")
}

// З12 п.1, CX5-55: условие головы нити стоит в одном операторе — строку нити,
// взятую Local.Claim, сервер ленты не выдаёт второй.
func TestC5_ThreadHeadHoldsAcrossEntryPoints(t *testing.T) {
	s := newC5Stand(t, c5Opts{})
	t1 := s.putRow(t, helloDesc(), "u@example.invalid", withThread(hello(), "ntc-2/usr-1"))
	t2 := s.putRow(t, helloDesc(), "u@example.invalid", withThread(hello(), "ntc-2/usr-1"))
	other := s.putRow(t, helloDesc(), "v@example.invalid", hello())
	require.Equal(t, []string{t1}, claimedIDs(s.localClaim(t, 1, feed.ClassNotice)))
	got := s.serverClaim(t, 10)
	ids := make([]string, 0, len(got))
	for _, n := range got {
		ids = append(ids, n.GetId())
	}
	require.Equal(t, []string{other}, ids, "сервер ленты выдал вторую строку нити %s, пока первая в полёте", t2)
}

// CX5-54, CX5-55: Local.Ack SENT зовёт наблюдатель ровно раз, строка несёт
// класс и шаблон; повтор Ack с тем же токеном и исходом наблюдателя не зовёт.
// Близнец — Server.Ack той же формы: тоже ровно раз.
func TestC5_ObserverIsCalledOncePerRecordedOutcome(t *testing.T) {
	s := newC5Stand(t, c5Opts{})
	a := s.putRow(t, helloDesc(), "a@example.invalid", hello())
	got := s.localClaim(t, 10, feed.ClassNotice)
	require.Equal(t, []string{a}, claimedIDs(got))
	ack := feed.Ack{ID: a, LeaseToken: got[0].LeaseToken, Outcome: feed.Outcome{Kind: feed.KindSent}}
	require.NoError(t, s.local.Ack(context.Background(), ack))
	require.NoError(t, s.local.Ack(context.Background(), ack), "повтор тем же токеном и исходом — успех без изменения")
	calls := s.localObs.seen()
	require.Len(t, calls, 1, "Local.Ack: наблюдатель ждали ровно раз")
	require.Equal(t, a, calls[0].ID)
	require.Equal(t, feed.ClassNotice, calls[0].Class)
	require.Equal(t, "probe-hello", calls[0].Template)
	require.Equal(t, feed.Outcome{Kind: feed.KindSent}, calls[0].Outcome)

	b := s.putRow(t, helloDesc(), "b@example.invalid", hello())
	sg := s.serverClaim(t, 10)
	require.Len(t, sg, 1)
	require.Equal(t, b, sg[0].GetId())
	require.NoError(t, s.serverAck(b, sg[0].GetLeaseToken(), notifyv1.OutcomeKind_SENT, 0, 0))
	require.NoError(t, s.serverAck(b, sg[0].GetLeaseToken(), notifyv1.OutcomeKind_SENT, 0, 0))
	require.Len(t, s.srvObs.seen(), 1, "Server.Ack: наблюдатель ждали ровно раз")
	require.Len(t, s.localObs.seen(), 1, "наблюдатель Local не зовётся исходом сервера")
}

// З28 п.2, CX5-61: строку с неоткрывшимся секретом finishClaim закрывает
// вызовом recordOutcome — наблюдатель зовётся ровно раз с INVALID
// (key_unavailable), строка несёт класс и шаблон. Положительный контроль —
// строка в ответ Claim не попала и закрыта.
func TestC5_SealedCloseGoesThroughTheObserver(t *testing.T) {
	s := newC5Stand(t, c5Opts{putRing: ring(t, key(1, 0xA1)), localRing: ring(t, key(2, 0xB2))})
	id := s.putRow(t, secretDesc(), "a@example.invalid", secretValues("old-key"))
	require.Empty(t, s.localClaim(t, 10, feed.ClassNotice))
	require.Equal(t, "invalid", s.state(t, id), "ФИКСТУРА: строка с неоткрывшимся секретом не закрыта")
	calls := s.localObs.seen()
	require.Len(t, calls, 1, "закрытие после расшифровки: наблюдатель ждали ровно раз")
	require.Equal(t, id, calls[0].ID)
	require.Equal(t, feed.ClassNotice, calls[0].Class)
	require.Equal(t, "probe-all", calls[0].Template)
	require.Equal(t, feed.Outcome{Kind: feed.KindInvalid, Reason: feed.ReasonKeyUnavailable}, calls[0].Outcome)
}

// З8 п.5, CX5-05, CX5-60: Supersede закрывает строки pending без действующей
// аренды — после Ack DEFER (аренда снята), с истёкшей арендой, ни разу не
// взятую — и возвращает закрытые. Арендованную и терминальную не трогает,
// отсутствующий id — не ошибка. Семь столбцов оператора: state, outcome_reason,
// outcome_token, recorded_kind, recorded_reason, secret_attrs, outcome_at.
// Повтор Ack DEFER закрытой строки — LEASE_LOST. Наблюдатель не зовётся.
func TestC5_SupersedeClosesOnlyUnleasedPending(t *testing.T) {
	s := newC5Stand(t, c5Opts{})
	deferred := s.putRow(t, helloDesc(), "d@example.invalid", hello())
	leased := s.putRow(t, helloDesc(), "l@example.invalid", hello())
	lapsed := s.putRow(t, helloDesc(), "x@example.invalid", hello())
	sent := s.putRow(t, helloDesc(), "s@example.invalid", hello())
	got := s.serverClaim(t, 10)
	require.Len(t, got, 4, "ФИКСТУРА: четыре строки не взяты")
	token := map[string]string{}
	for _, n := range got {
		token[n.GetId()] = n.GetLeaseToken()
	}
	require.NoError(t, s.serverAck(deferred, token[deferred], notifyv1.OutcomeKind_DEFER,
		notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE, time.Minute))
	require.NoError(t, s.serverAck(sent, token[sent], notifyv1.OutcomeKind_SENT, 0, 0))
	s.exec(t, `UPDATE probe_notification_outbox SET lease_until = now() - interval '1 second' WHERE id = $1`, lapsed)
	fresh := s.putRow(t, helloDesc(), "f@example.invalid", hello())
	before := len(s.srvObs.seen())

	tx := s.begin(t)
	closed, err := feed.Supersede(s.ctx, tx, []string{deferred, leased, lapsed, sent, fresh, ids.NewHyphenID(ids.PrefixNotificationHyphen)})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(s.ctx))
	require.Equal(t, sorted(deferred, lapsed, fresh), orEmpty(closed))

	reason := supersededReason(t)
	for _, id := range []string{deferred, lapsed, fresh} {
		var st, rsn string
		var hasToken, kind, rreason, secret, outcomeAt bool
		require.NoError(t, s.pool.QueryRow(context.Background(), `
SELECT state, coalesce(outcome_reason, ''), outcome_token IS NOT NULL, recorded_kind IS NOT NULL,
       recorded_reason IS NOT NULL, secret_attrs IS NOT NULL, outcome_at IS NOT NULL
  FROM probe_notification_outbox WHERE id = $1`, id).Scan(&st, &rsn, &hasToken, &kind, &rreason, &secret, &outcomeAt))
		require.Equal(t, "superseded", st, id)
		require.Equal(t, reason, rsn, id)
		require.False(t, hasToken || kind || rreason || secret, "%s: столбцы исхода и секрет обнулены", id)
		require.True(t, outcomeAt, id)
	}
	require.Equal(t, "pending", s.state(t, leased), "арендованная строка доходит до своего исхода")
	require.Equal(t, "sent", s.state(t, sent))
	require.Len(t, s.srvObs.seen(), before, "Supersede наблюдатель не зовёт")

	requireLeaseLost(t, s.serverAck(deferred, token[deferred], notifyv1.OutcomeKind_DEFER,
		notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE, time.Minute))
}

// З24 п.4: DeleteUnleased удаляет строки без действующей аренды в любом
// состоянии (pending, терминальную), арендованную оставляет и возвращает в
// leased; отсутствующий id — ни там ни там, ошибки нет.
func TestC5_DeleteUnleasedSplitsByLease(t *testing.T) {
	s := newC5Stand(t, c5Opts{})
	held := s.putRow(t, helloDesc(), "h@example.invalid", hello())
	done := s.putRow(t, helloDesc(), "d@example.invalid", hello())
	got := s.serverClaim(t, 10)
	require.Len(t, got, 2, "ФИКСТУРА: две строки не взяты")
	for _, n := range got {
		if n.GetId() == done {
			require.NoError(t, s.serverAck(done, n.GetLeaseToken(), notifyv1.OutcomeKind_SENT, 0, 0))
		}
	}
	pending := s.putRow(t, helloDesc(), "p@example.invalid", hello())
	missing := ids.NewHyphenID(ids.PrefixNotificationHyphen) // строки с таким id нет

	tx := s.begin(t)
	deleted, leasedIDs, err := feed.DeleteUnleased(s.ctx, tx, []string{held, done, pending, missing})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(s.ctx))
	require.Equal(t, sorted(done, pending), orEmpty(deleted))
	require.Equal(t, []string{held}, orEmpty(leasedIDs))
	require.Equal(t, 1, s.count(t, `SELECT count(*) FROM probe_notification_outbox`), "осталась только арендованная")
	require.Equal(t, "pending", s.state(t, held))
}

// З24 п.4, CX5-57: решение «под арендой» и удаление — на одном чтении. Запись
// исхода DEFER держит блокировку строки (наблюдатель-фикстура в её
// транзакции), DeleteUnleased ждёт; после фиксации записи строка — ровно в
// одном из двух списков (аренду DEFER снял — значит в deleted).
func TestC5_DeleteUnleasedAgainstAConcurrentOutcomeWrite(t *testing.T) {
	obs := &recObserver{entered: make(chan struct{}), release: make(chan struct{})}
	s := newC5Stand(t, c5Opts{localObs: obs})
	id := s.putRow(t, helloDesc(), "a@example.invalid", hello())
	got := s.localClaim(t, 10, feed.ClassNotice)
	require.Equal(t, []string{id}, claimedIDs(got))

	ackErr := make(chan error, 1)
	go func() {
		ackErr <- s.local.Ack(context.Background(), feed.Ack{ID: id, LeaseToken: got[0].LeaseToken,
			Outcome: feed.Outcome{Kind: feed.KindDefer, Reason: feed.ReasonPlatformUnavailable}, DeferFor: time.Minute})
	}()
	<-obs.entered // запись исхода исполнена, транзакция открыта, строка заблокирована

	type res struct {
		deleted, leased []string
		err             error
	}
	done := make(chan res, 1)
	go func() {
		tx, err := s.pool.Begin(s.ctx)
		if err != nil {
			done <- res{err: err}
			return
		}
		d, l, err := feed.DeleteUnleased(s.ctx, tx, []string{id})
		if err == nil {
			err = tx.Commit(s.ctx)
		} else {
			_ = tx.Rollback(s.ctx)
		}
		done <- res{d, l, err}
	}()
	require.Eventually(t, func() bool {
		return s.count(t, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) == 1
	}, 10*time.Second, 20*time.Millisecond, "DeleteUnleased не встал в ожидание блокировки строки")

	close(obs.release)
	require.NoError(t, <-ackErr)
	r := <-done
	require.NoError(t, r.err)
	inDeleted, inLeased := slices.Contains(r.deleted, id), slices.Contains(r.leased, id)
	require.True(t, inDeleted != inLeased, "строка ровно в одном списке: deleted=%v leased=%v", r.deleted, r.leased)
	require.True(t, inDeleted, "DEFER снял аренду — строка удалена")
}
