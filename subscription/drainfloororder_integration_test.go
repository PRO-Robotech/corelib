// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// drainfloororder_integration_test.go — ПОЛ БЕРЁТСЯ НЕ РАНЬШЕ СТРАНИЦЫ
// (задача #1764).
//
// # Предмет — чем эта проба отличается от соседней
//
// `journaltruncation_integration_test.go` строит уборку ОДНОЙ транзакцией и
// потому детерминирован: подписчик спит на уведомлении, просыпается на фиксации
// и видит уже сложившееся состояние. Гонки между двумя запросами дренажа он не
// видит by construction — и остаётся зелёным при ЛЮБОМ их порядке.
//
// Здесь предмет — сам порядок. Пол и страница суть ДВА запроса. Уборка,
// зафиксировавшаяся МЕЖДУ ними, снимает строки, которых пол ещё не видел:
// страница приходит без них, отказа нет, и подписчик получает НЕПОЛНОЕ как
// полное. Решает расписание, а не проверка (ban #10, software check-then-act).
//
// Порядок «пол не раньше страницы» делает вывод ДОКАЗУЕМЫМ: нижняя строка
// непустого журнала монотонна (уборка снимает префикс, номера растут), поэтому
// наблюдение, взятое в снимке не раньше страницы, занизиться не может. Пропала
// строка из окна ⟹ пол выше курсора ⟹ отказ.
package subscription_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/pagetoken"
)

// TestConcurrentSweepUnderOpenStreamsNeverDeliversASilentGap — уборка ПОД
// работающими потоками не производит молчаливого пропуска.
//
// # Инвариант, а не расписание
//
// Утверждается то, что верно при ЛЮБОМ порядке фиксаций: подписчик не вправе
// уйти ЗА строку, которой не получил. Курсор открытия стоит ровно под первой
// строкой раунда, поэтому полученное обязано быть НАЧАЛЬНЫМ ОТРЕЗКОМ выложенного
// — строка за строкой, без пропусков. Законных исходов два: отдать очередное
// событие либо отказать «позиция утрачена»; третьего — «отдать следующее за
// снятым» — не существует.
//
// Отказ здесь НЕ является поражением потока и нарушением не считается: строки
// действительно сняты, и явный отказ с возобновимой позицией есть ровно то, ради
// чего пол заведён. Нарушение — тишина на их месте.
//
// # Как гонка делается ЧАСТОЙ, а не редкой
//
// Решающий момент один на поток за раунд: тот, в который порог уборки
// перешагивает курсор. Попадёт ли снятие в узкое окно между двумя запросами
// дренажа — вопрос доли `окно / длительность итерации`, поэтому итерацию делают
// ДЕШЁВОЙ: писатель кладёт строки по одной, граница устоявшегося идёт следом, и
// партия дренажа выходит в единицы строк вместо сотни. Тогда окно занимает
// заметную долю цикла, а не проценты.
//
// Раундов несколько по той же причине, по какой их несколько у журнала смены
// субъекта: одна попытка ловит дефект не всегда, а раунды берут ту же гонку
// многократно в ОДНОМ прогоне, ничего не ослабляя — утверждение в каждом раунде
// то же самое.
//
// # Положительный контроль встроен в каждый раунд
//
// Уборка стартует лишь после того, как КАЖДЫЙ поток отдал хотя бы одно событие.
// Без этого проба зеленела бы на сервере, который рвёт всякий поток немедленно:
// «пропусков нет» стало бы неотличимо от «нечего пропускать».
func TestConcurrentSweepUnderOpenStreamsNeverDeliversASilentGap(t *testing.T) {
	j := sweepingJournal()
	s := newStand(t, standOpts{
		journal:    &j,
		maxStreams: 64,
		idlePoll:   50 * time.Millisecond,
		budget:     240 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	const (
		rounds  = 8
		streams = 8
		// preseed — строки, на которых берётся положительный контроль раунда:
		// поток обязан отдать событие ДО того, как уборка вообще началась.
		preseed = 24
		// stagger — на сколько строк курсоры потоков РАЗВЕДЕНЫ между собой.
		//
		// Сажать все потоки на одну позицию значило бы получить не восемь
		// попыток, а одну: они просыпаются на одном уведомлении, идут в ногу и
		// перешагиваются уборкой ОДНОВРЕМЕННО. Разведённые курсоры дают разные
		// моменты перешагивания, то есть независимые попытки.
		stagger = 2
		// feedRows / feedGap — писатель кладёт строки ПО ОДНОЙ: так граница
		// устоявшегося идёт вплотную за курсором, и партия дренажа мала.
		feedRows = 100
		feedGap  = 1200 * time.Microsecond
		sweepGap = 600 * time.Microsecond
	)

	var (
		violations []string
		delivered  int
		refusals   int
	)

	for round := 0; round < rounds; round++ {
		log := &journalLog{}
		s.appendRows(t, log, fmt.Sprintf("r%dp", round), preseed)
		first := log.first()

		// Курсор открытия отсчитывается ОТ ПЕРВОЙ строки раунда: тогда первое
		// законное событие каждого потока известно поимённо, и «начальный
		// отрезок» перестаёт зависеть от того, что было в журнале раньше.
		roundCtx, roundCancel := context.WithTimeout(ctx, 120*time.Second)
		cols := make([]*collector, streams)
		for i := range cols {
			token := pagetoken.EncodeSubscriptionPosition(
				pagetoken.SubscriptionPosition{Settled: first - 1 + int64(i*stagger)})
			cols[i] = collect(s.open(t, roundCtx, &subscriptionv1.SubscriptionRequest{
				Start: &subscriptionv1.SubscriptionRequest_Position{Position: token},
			}))
		}
		for i, c := range cols {
			select {
			case <-c.first:
			case <-time.After(60 * time.Second):
				roundCancel()
				t.Fatalf("раунд %d: поток %d не отдал НИ ОДНОГО события до начала уборки — "+
					"проверять «нет пропусков» не на чем", round, i)
			}
		}

		// Писатель и уборщик идут СВОИМИ соединениями и своими транзакциями:
		// подписчик не видит их промежуточных состояний, но и не защищён от
		// того, что уборка зафиксируется посреди его партии.
		var wg sync.WaitGroup
		fed := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer close(fed)
			s.feed(roundCtx, t, log, fmt.Sprintf("r%df", round), feedRows, feedGap)
		}()
		go func() {
			defer wg.Done()
			s.sweepTail(roundCtx, t, fed, sweepGap)
		}()
		wg.Wait()

		// Дать потокам договорить: тому, кто уже прочитал страницу, остаётся
		// отдать её события либо отказать.
		time.Sleep(750 * time.Millisecond)
		roundCancel()

		all := log.ordered()
		for i, c := range cols {
			// Каждый поток сел на СВОЮ позицию, поэтому и ожидаемый отрезок у
			// него свой: тот, что начинается сразу над его курсором.
			ids := all[i*stagger:]
			got, failure := c.wait()
			delivered += len(got)
			for k, id := range got {
				if k < len(ids) && id == ids[k] {
					continue
				}
				want := "<за пределами выложенного>"
				if k < len(ids) {
					want = ids[k]
				}
				violations = append(violations, fmt.Sprintf(
					"раунд %d, поток %d: событие №%d — %q, ожидалось %q; подписчик переехал "+
						"через снятое и получил НЕПОЛНОЕ, ничем не отличимое от «изменений не было»",
					round, i, k+1, id, want))
				break
			}
			if st, _ := status.FromError(failure); failure != nil && st.Code() == codes.OutOfRange {
				refusals++
				if !hasResumablePosition(st) {
					violations = append(violations, fmt.Sprintf(
						"раунд %d, поток %d: отказ «позиция утрачена» не называет возобновимую "+
							"позицию машинно: %v", round, i, st.Proto()))
				}
			}
		}
	}

	if delivered == 0 {
		t.Fatal("за весь прогон не доставлено ни одного события — утверждение о пропусках вакуумно")
	}
	for _, v := range violations {
		t.Error(v)
	}
	t.Logf("перепись: раундов %d, потоков в раунде %d; доставлено событий %d, "+
		"отказов «позиция утрачена» %d, нарушений %d",
		rounds, streams, delivered, refusals, len(violations))
}

// journalLog — что и в каком порядке было выложено в журнал за раунд.
//
// Порядок берётся СОРТИРОВКОЙ по позиции, а не порядком строк ответа: `RETURNING`
// его не обещает, и опереться на него значило бы утверждать о реализации базы.
type journalLog struct {
	mu   sync.Mutex
	rows []journalRow
}

type journalRow struct {
	seq int64
	id  string
}

func (l *journalLog) add(seq int64, id string) {
	l.mu.Lock()
	l.rows = append(l.rows, journalRow{seq: seq, id: id})
	l.mu.Unlock()
}

func (l *journalLog) ordered() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := make([]journalRow, len(l.rows))
	copy(rows, l.rows)
	sort.Slice(rows, func(i, j int) bool { return rows[i].seq < rows[j].seq })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.id)
	}
	return out
}

func (l *journalLog) first() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	first := l.rows[0].seq
	for _, r := range l.rows {
		if r.seq < first {
			first = r.seq
		}
	}
	return first
}

// appendRows выкладывает n строк ОДНИМ оператором — предысторию раунда.
func (s *stand) appendRows(t testing.TB, log *journalLog, tag string, n int) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		t.Fatalf("соединение писателя: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	rows, err := conn.Query(ctx, `
		INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
		SELECT 'Network', $1 || lpad(g::text, 7, '0'), 'CREATED', '{"projectId":"prj-a"}'::jsonb
		FROM generate_series(1, $2) g
		RETURNING sequence_no, resource_id`, tag, n)
	if err != nil {
		t.Fatalf("выкладка предыстории раунда: %v", err)
	}
	count := 0
	for rows.Next() {
		var r journalRow
		if err := rows.Scan(&r.seq, &r.id); err != nil {
			rows.Close()
			t.Fatalf("чтение выложенной строки: %v", err)
		}
		log.add(r.seq, r.id)
		count++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("выкладка предыстории раунда: %v", err)
	}
	if count != n {
		t.Fatalf("выложено %d строк из %d", count, n)
	}
}

// feed кладёт строки ПО ОДНОЙ — каждую своей транзакцией.
//
// Строка за строкой, а не пачкой: граница устоявшегося идёт вплотную за
// курсором, поэтому дренаж читает единицы строк за итерацию. Дешёвая итерация и
// делает окно между двумя запросами заметной её долей — иначе решающий момент
// приходился бы на окно шириной в проценты цикла.
func (s *stand) feed(ctx context.Context, t testing.TB, log *journalLog, tag string, n int, gap time.Duration) {
	t.Helper()
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		t.Errorf("соединение писателя: %v", err)
		return
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for i := 1; i <= n; i++ {
		var (
			seq int64
			id  string
		)
		err := conn.QueryRow(ctx, `
			INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
			VALUES ('Network', $1, 'CREATED', '{"projectId":"prj-a"}'::jsonb)
			RETURNING sequence_no, resource_id`,
			fmt.Sprintf("%s%07d", tag, i)).Scan(&seq, &id)
		if err != nil {
			if ctx.Err() == nil {
				t.Errorf("вставка строки %d: %v", i, err)
			}
			return
		}
		log.add(seq, id)
		time.Sleep(gap)
	}
}

// sweepTail снимает ВСЁ, кроме последней строки, и делает это снова и снова, пока
// писатель кладёт новые.
//
// Порог идёт вплотную за головой журнала намеренно: он обязан перешагивать
// курсор подписчика, отставший хотя бы на строку, — именно это перешагивание и
// есть решающий момент. Последняя строка не снимается, чтобы журнал не опустел:
// пустота — отдельная ветвь формулы пола, и смешивать её с этой пробой значило бы
// проверять два предмета одним утверждением.
func (s *stand) sweepTail(ctx context.Context, t testing.TB, until <-chan struct{}, gap time.Duration) {
	t.Helper()
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		t.Errorf("соединение уборщика: %v", err)
		return
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for {
		select {
		case <-until:
			return
		case <-ctx.Done():
			return
		default:
		}
		if _, err := conn.Exec(ctx, `
			DELETE FROM probe_outbox
			WHERE sequence_no <= (SELECT max(sequence_no) - 1 FROM probe_outbox)`); err != nil {
			if ctx.Err() == nil {
				t.Errorf("уборка: %v", err)
			}
			return
		}
		time.Sleep(gap)
	}
}

// collector — единственный читатель потока, копящий ПОЛУЧЕННОЕ.
//
// Читатель у потока один по той же причине, по какой он один у [sub]: `Recv`
// необратим, и утверждение «больше ничего нет», сделанное вторым читателем,
// съедает событие, приехавшее позже.
type collector struct {
	first chan struct{}
	done  chan struct{}

	mu   sync.Mutex
	ids  []string
	fail error
}

func collect(sb *sub) *collector {
	c := &collector{first: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		var once sync.Once
		for ev := range sb.events {
			c.mu.Lock()
			c.ids = append(c.ids, ev.GetResourceId())
			c.mu.Unlock()
			once.Do(func() { close(c.first) })
		}
		err := <-sb.fail
		c.mu.Lock()
		c.fail = err
		c.mu.Unlock()
	}()
	return c
}

// wait дожидается конца потока и отдаёт полученное вместе с его исходом.
func (c *collector) wait() ([]string, error) {
	select {
	case <-c.done:
	case <-time.After(30 * time.Second):
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.ids))
	copy(out, c.ids)
	return out, c.fail
}
