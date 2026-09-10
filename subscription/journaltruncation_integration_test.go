// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// journaltruncation_integration_test.go — что происходит с подписчиком, чья
// позиция снята уборкой ПОД РАБОТАЮЩИМ ПОТОКОМ.
//
// Открытие потока с утраченной позиции уже закреплено
// (`TestPositionNoLongerResumableIsAnExplicitRefusal`, WATCH-1-11). Здесь —
// случай, которого та проба не видит by construction: поток УЖЕ открыт, нижняя
// граница объявлена подписчику один раз, и уборка двигает её дальше. Выборка
// снятых строк просто не находит, курсор переезжает через них по последней
// прочитанной позиции — и подписчик получает НЕПОЛНОЕ, ничем не отличимое от
// «изменений не было».
package subscription_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/pgtest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/subscription"
)

// sweepingJournal — объявление владельца, который журнал ЧИСТИТ.
func sweepingJournal() subscription.Journal {
	j := probeJournal()
	j.Storage.Retention = subscription.RetainsFromEarliestRow
	j.Storage.AgeColumn = "created_at"
	return j
}

// TestTruncationUnderAnOpenStreamRefusesInsteadOfDeliveringLess — уборка,
// прошедшая под открытым потоком, обязана дать ЯВНЫЙ отказ.
//
// # Как состояние строится ДЕТЕРМИНИРОВАННО
//
// Всё, что происходит «под потоком», делается ОДНОЙ транзакцией: пробуждение
// приходит подписчику на её фиксации, поэтому промежуточного состояния он не
// видит и гонки между вставкой и снятием нет. К моменту пробуждения курсор
// подписчика стоит на 3, самая ранняя удержанная строка — 6, значит пол равен 5,
// а строки 4 и 5 для него утрачены.
func TestTruncationUnderAnOpenStreamRefusesInsteadOfDeliveringLess(t *testing.T) {
	j := sweepingJournal()
	s := newStand(t, standOpts{journal: &j})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for i := 0; i < 3; i++ {
		s.emit(t, "Network", "net0000000000000000"+string(rune('a'+i)), "CREATED", "prj-a")
	}

	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	// Подписчик догнал: курсор стоит на 3.
	recvEvents(t, sb, 3)

	// Владелец под работающим потоком: две строки написаны и тут же сняты вместе
	// с уже прочитанными, одна написана заново. Одной транзакцией — подписчик
	// просыпается только на фиксации.
	s.execAtomically(t,
		`INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
		   VALUES ('Network','net00000000000000004','CREATED','{"projectId":"prj-a"}'::jsonb)`,
		`INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
		   VALUES ('Network','net00000000000000005','CREATED','{"projectId":"prj-a"}'::jsonb)`,
		`DELETE FROM probe_outbox WHERE sequence_no <= 5`,
		`INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
		   VALUES ('Network','net00000000000000006','CREATED','{"projectId":"prj-a"}'::jsonb)`,
	)

	select {
	case ev, ok := <-sb.events:
		if ok {
			t.Fatalf("поток отдал событие %q поверх утраченного участка — подписчик получил НЕПОЛНОЕ "+
				"и не отличит это от «изменений не было»", ev.GetResourceId())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("поток не ответил ни событием, ни отказом")
	}

	err := <-sb.fail
	st, _ := status.FromError(err)
	if st.Code() != codes.OutOfRange {
		t.Fatalf("уборка под открытым потоком ответила %s (%v), ожидался OutOfRange", st.Code(), err)
	}
	if !hasResumablePosition(st) {
		t.Fatalf("отказ не называет возобновимую позицию машинно: %v", st.Proto())
	}
}

// TestStreamAboveTheFloorSurvivesTruncation — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к пробе
// выше.
//
// Без него отказ зеленел бы на потоке, который рвётся при ЛЮБОЙ уборке, в том
// числе снявшей только уже прочитанное. Здесь уборка снимает ровно то, что
// подписчик уже получил, — поток обязан продолжиться и отдать следующее событие.
func TestStreamAboveTheFloorSurvivesTruncation(t *testing.T) {
	j := sweepingJournal()
	s := newStand(t, standOpts{journal: &j})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for i := 0; i < 3; i++ {
		s.emit(t, "Network", "net0000000000000000"+string(rune('a'+i)), "CREATED", "prj-a")
	}
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	recvEvents(t, sb, 3)

	// Снимается ТОЛЬКО прочитанное: пол переезжает на 3, курсор подписчика тоже
	// 3, значит `курсор >= пол` — терять нечего.
	s.execAtomically(t,
		`DELETE FROM probe_outbox WHERE sequence_no <= 3`,
		`INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
		   VALUES ('Network','net00000000000000009','CREATED','{"projectId":"prj-a"}'::jsonb)`,
	)

	got := recvEvents(t, sb, 1)
	if got[0].GetResourceId() != "net00000000000000009" {
		t.Fatalf("после уборки уже прочитанного поток отдал %q", got[0].GetResourceId())
	}
	select {
	case err := <-sb.fail:
		t.Fatalf("поток оборвался, хотя утрачивать было нечего: %v", err)
	default:
	}
}

// TestSweepRemovesAgedRowsAsAPrefix — уборщик снимает СТАРЫЕ строки и снимает их
// ПРЕФИКСОМ.
//
// Утверждаются обе половины сразу: снятое — только то, что старше порога, и
// оставшееся — непрерывный суффикс. Вторая половина несущая: дыра над нижней
// границей делает возобновление подписчика молча неполным, и никакой отказ её не
// поймает — пол-то не сдвинулся.
func TestSweepRemovesAgedRowsAsAPrefix(t *testing.T) {
	j := sweepingJournal()
	s := newStand(t, standOpts{journal: &j})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for i := 0; i < 6; i++ {
		s.emit(t, "Network", "net0000000000000000"+string(rune('a'+i)), "CREATED", "prj-a")
	}
	// Первые четыре состарены на два часа; порог пробы — час.
	s.exec(t, `UPDATE probe_outbox SET created_at = now() - interval '2 hours' WHERE sequence_no <= 4`)

	pool := s.pool(t, ctx)
	sw, err := subscription.NewJournalSweeper(pool, j, nil)
	if err != nil {
		t.Fatalf("уборщик не собрался: %v", err)
	}

	removed, full, err := sw.Sweep(ctx, time.Hour, 100)
	if err != nil {
		t.Fatalf("проход уборки: %v", err)
	}
	if removed != 4 {
		t.Fatalf("снято %d строк, ожидалось 4", removed)
	}
	if full {
		t.Fatal("проход объявил партию полной при партии в 100 строк — догон меряли бы не тем")
	}
	if got := positions(t, ctx, pool); !equalInt64(got, []int64{5, 6}) {
		t.Fatalf("после уборки остались позиции %v, ожидался непрерывный суффикс [5 6]", got)
	}

	// Повторный проход не снимает НИЧЕГО: молодые строки порогу не подлежат.
	removed, _, err = sw.Sweep(ctx, time.Hour, 100)
	if err != nil {
		t.Fatalf("второй проход: %v", err)
	}
	if removed != 0 {
		t.Fatalf("второй проход снял %d строк — уборка трогает строки внутри окна удержания", removed)
	}
}

// TestSweepNeverBreaksThePrefixWhenAgesAreOutOfOrder — отметка времени и номер
// строки НЕ упорядочены одинаково, и уборка обязана это переживать.
//
// Отметка ставится в НАЧАЛЕ транзакции, номер выдаётся на ВСТАВКЕ, поэтому
// строка с бо́льшим номером бывает СТАРШЕ строки с меньшим. Уборка «по возрасту»
// сняла бы верхнюю, оставив нижнюю: пол не сдвинулся бы, а над ним появилась бы
// дыра — и подписчик, законно допущенный по полу, потерял бы событие МОЛЧА.
func TestSweepNeverBreaksThePrefixWhenAgesAreOutOfOrder(t *testing.T) {
	j := sweepingJournal()
	s := newStand(t, standOpts{journal: &j})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for i := 0; i < 4; i++ {
		s.emit(t, "Network", "net0000000000000000"+string(rune('a'+i)), "CREATED", "prj-a")
	}
	// Строка 3 старая, строка 2 — молодая: порядок возрастов ОБРАТЕН порядку
	// номеров ровно на этой паре.
	s.exec(t, `UPDATE probe_outbox SET created_at = now() - interval '2 hours' WHERE sequence_no IN (1, 3)`)

	pool := s.pool(t, ctx)
	sw, err := subscription.NewJournalSweeper(pool, j, nil)
	if err != nil {
		t.Fatalf("уборщик не собрался: %v", err)
	}
	if _, _, err := sw.Sweep(ctx, time.Hour, 100); err != nil {
		t.Fatalf("проход уборки: %v", err)
	}

	// Снята ТОЛЬКО первая: она единственная лежит строго ниже самой ранней
	// строки, которую снимать ещё нельзя.
	if got := positions(t, ctx, pool); !equalInt64(got, []int64{2, 3, 4}) {
		t.Fatalf("после уборки остались позиции %v, ожидался непрерывный суффикс [2 3 4]: "+
			"снятие старой строки поверх молодой оставляет дыру над полом", got)
	}
}

// TestConcurrentSweepsLeaveNoHole — СПОРНЫЙ ПУТЬ: две реплики убирают один
// журнал одновременно.
//
// Требование `data-integrity.md` §«Чек-лист нового ссылочного поля» п. 5:
// конкурентный путь проверяется параллельными горутинами, а не рассуждением.
// Здесь утверждается не «ровно одна выиграла» (обе вправе снять свою часть), а
// то, ради чего уборка вообще устроена префиксом: сколько бы реплик ни шло
// разом, оставшееся — НЕПРЕРЫВНЫЙ СУФФИКС.
func TestConcurrentSweepsLeaveNoHole(t *testing.T) {
	j := sweepingJournal()
	s := newStand(t, standOpts{journal: &j})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const rows = 40
	for i := 0; i < rows; i++ {
		s.emit(t, "Network", "net00000000000000000", "CREATED", "prj-a")
	}
	s.exec(t, `UPDATE probe_outbox SET created_at = now() - interval '2 hours' WHERE sequence_no <= 30`)

	pool := s.pool(t, ctx)

	const replicas = 4
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int64
		errs  []error
	)
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sw, err := subscription.NewJournalSweeper(pool, j, nil)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			// Партия меньше накопленного намеренно: реплики обязаны встретиться
			// на одном и том же участке, а не разойтись по разным.
			for range 5 {
				n, _, serr := sw.Sweep(ctx, time.Hour, 8)
				mu.Lock()
				total += n
				if serr != nil {
					errs = append(errs, serr)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(errs) > 0 {
		t.Fatalf("параллельная уборка отказала: %v", errs)
	}
	if total != 30 {
		t.Fatalf("реплики сняли суммарно %d строк, ожидалось ровно 30 — "+
			"строка не может быть снята дважды", total)
	}
	got := positions(t, ctx, pool)
	want := make([]int64, 0, rows-30)
	for p := int64(31); p <= rows; p++ {
		want = append(want, p)
	}
	if !equalInt64(got, want) {
		t.Fatalf("после параллельной уборки остались позиции %v, ожидался непрерывный суффикс %v", got, want)
	}
}

// TestSweeperRefusesAJournalThatPromisesToRetainEverything — уборщик не
// собирается для владельца, объявившего [subscription.RetainsEverything].
//
// Иначе оператор снимал бы строки у журнала, чей контракт говорит подписчику,
// что отказ «позиция утрачена» не наступает никогда: обещание и снятие — одно
// решение, и половина его не выражается.
func TestSweeperRefusesAJournalThatPromisesToRetainEverything(t *testing.T) {
	j := probeJournal() // RetainsEverything
	if _, err := subscription.NewJournalSweeper(nil, j, nil); err == nil {
		t.Fatal("уборщик собрался для журнала, обещавшего удерживать всё")
	}
}

// positions — какие позиции остались в журнале, по возрастанию.
func positions(t testing.TB, ctx context.Context, pool *pgxpool.Pool) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT sequence_no FROM probe_outbox ORDER BY sequence_no`)
	if err != nil {
		t.Fatalf("чтение позиций: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var p int64
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("чтение позиции: %v", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("чтение позиций: %v", err)
	}
	return out
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pool — пул к журналу пробы. Уборщик спрашивает границу и пишет ОДНИМ
// источником, поэтому и здесь он один.
func (s *stand) pool(t testing.TB, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(ctx, s.dsn)
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, p)
	return p
}

// execAtomically выполняет операторы ОДНОЙ транзакцией.
//
// Пробуждение подписчика приходит на фиксации, поэтому промежуточных состояний
// он не видит: проба строит нужное состояние без гонки с потоком.
func (s *stand) execAtomically(t testing.TB, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		t.Fatalf("соединение: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("транзакция: %v", err)
	}
	for _, q := range stmts {
		if _, err := tx.Exec(ctx, q); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("оператор %q: %v", q, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("фиксация: %v", err)
	}
}
