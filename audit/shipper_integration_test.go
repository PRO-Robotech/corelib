// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/audit"
	"github.com/PRO-Robotech/corelib/observability"
	"github.com/PRO-Robotech/corelib/outbox/metrics"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// journalDDL — ФОРМА журнала аудита, которую читает вывоз. Она списана с обеих
// живых таблиц (`kaname.audit_outbox`, `public.audit_outbox`), и колонки
// сверх учётных здесь стоят намеренно: вывоз обязан доставлять СТРОКУ целиком,
// а не то подмножество, которое умеет назвать общая оснастка доставки.
const journalDDL = `
CREATE TABLE audit_journal (
    id              text PRIMARY KEY,
    event_type      text NOT NULL,
    actor_type      text NOT NULL DEFAULT '',
    actor_id        text NOT NULL DEFAULT '',
    resource_id     text NOT NULL DEFAULT '',
    payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    status          text NOT NULL DEFAULT 'pending',
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,
    last_error      text,
    CONSTRAINT audit_journal_status_check
        CHECK (status IN ('pending', 'sent')),
    CONSTRAINT audit_journal_sent_at_check
        CHECK ((status = 'sent') = (sent_at IS NOT NULL))
)`

func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "auditshipper",
		Migrate: pgtest.SQL(journalDDL),
	}))
}

// recordingSink — приёмник, который запоминает доставленное и умеет отказать по
// имени записи. Отказ у контракта [audit.Sink] ровно один вид — «не принял»,
// поэтому и подставной приёмник другого не изображает.
type recordingSink struct {
	mu       sync.Mutex
	got      []audit.Record
	refusing map[string]bool
	texts    map[string]string
}

func newRecordingSink() *recordingSink {
	return &recordingSink{refusing: map[string]bool{}, texts: map[string]string{}}
}

func (s *recordingSink) Ship(_ context.Context, r audit.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refusing[r.ID] {
		if text, ok := s.texts[r.ID]; ok {
			return errors.New(text)
		}
		return errors.New("синтетический отказ приёмника")
	}
	s.got = append(s.got, r)
	return nil
}

func (s *recordingSink) Name() string { return "recording" }

// refuseWith — отказ с ЗАДАННЫМ текстом. Текст отказа приходит из чужого тела и
// нашим правилам не подчиняется; проба ниже пользуется этим намеренно.
func (s *recordingSink) refuseWith(id, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusing[id] = true
	if s.texts == nil {
		s.texts = map[string]string{}
	}
	s.texts[id] = text
}

func (s *recordingSink) refuse(id string, yes bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusing[id] = yes
}

func (s *recordingSink) delivered() []audit.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.Record, len(s.got))
	copy(out, s.got)
	return out
}

type journalRow struct {
	status    string
	attempts  int
	sentAt    *time.Time
	lastError *string
	nextAt    time.Time
}

func readRow(t *testing.T, pool *pgxpool.Pool, id string) journalRow {
	t.Helper()
	var r journalRow
	err := pool.QueryRow(context.Background(),
		`SELECT status, attempts, sent_at, last_error, next_attempt_at FROM audit_journal WHERE id = $1`, id).
		Scan(&r.status, &r.attempts, &r.sentAt, &r.lastError, &r.nextAt)
	require.NoError(t, err)
	return r
}

func insertRow(t *testing.T, pool *pgxpool.Pool, id, eventType string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO audit_journal (id, event_type, actor_type, actor_id, resource_id, payload)
		 VALUES ($1, $2, 'user', 'usr-'||$1, 'res-'||$1, '{"k":"v"}'::jsonb)`, id, eventType)
	require.NoError(t, err)
}

func newTestShipper(t *testing.T, pool *pgxpool.Pool, sink audit.Sink, rec metrics.Recorder) *audit.Shipper {
	t.Helper()
	sh, err := audit.NewShipper(pool, sink, rec, observability.NewSlogger(io.Discard), audit.ShipperConfig{
		Table:      "audit_journal",
		BatchSize:  2,
		BackoffMin: 50 * time.Millisecond,
		BackoffMax: 100 * time.Millisecond,
	})
	require.NoError(t, err)
	return sh
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), pgtest.NewDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	return pool
}

// TestShipperDeliversWholeRowAndMarksItSent — строка ДОЕЗЖАЕТ, и это видно с
// обеих сторон: приёмник её получил, а таблица помечена доставленной с
// НЕНУЛЕВЫМ числом попыток.
//
// Число попыток проверяется отдельно от состояния намеренно: доставка, не
// увеличивающая счётчик, оставила бы «доставлено ноль раз» и «доставлено без
// единой попытки» неотличимыми — ровно та пара, из-за которой очередь и
// разбирали.
func TestShipperDeliversWholeRowAndMarksItSent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sink := newRecordingSink()
	sh := newTestShipper(t, pool, sink, metrics.NewMemRecorder())

	// Батч намеренно меньше числа строк: проход обязан вывезти ВСЮ голову, а не
	// один батч, иначе темп вывоза становится функцией темпа записи.
	for i := 1; i <= 5; i++ {
		insertRow(t, pool, fmt.Sprintf("aud%02d", i), "instance.create")
	}

	res, err := sh.Pass(context.Background())
	require.NoError(t, err)
	require.Equal(t, 5, res.Shipped)
	require.Zero(t, res.Deferred)

	got := sink.delivered()
	require.Len(t, got, 5)

	byID := map[string]audit.Record{}
	for _, r := range got {
		byID[r.ID] = r
	}
	one := byID["aud01"]
	require.Equal(t, "instance.create", one.EventType)
	require.False(t, one.CreatedAt.IsZero())
	require.Equal(t, "user", one.Fields["actor_type"], "актор обязан доехать")
	require.Equal(t, "res-aud01", one.Fields["resource_id"])
	require.NotContains(t, one.Fields, "status", "учётные колонки доставки — не часть записи журнала")
	require.NotContains(t, one.Fields, "attempts")
	require.NotContains(t, one.Fields, "next_attempt_at")
	require.NotContains(t, one.Fields, "sent_at")
	require.NotContains(t, one.Fields, "last_error")

	row := readRow(t, pool, "aud01")
	require.Equal(t, "sent", row.status)
	require.Equal(t, 1, row.attempts, "успешная доставка — это одна СОСТОЯВШАЯСЯ попытка")
	require.NotNil(t, row.sentAt)
	require.Nil(t, row.lastError)
}

// TestDeliveredRowIsNeverClaimedAgain — второй проход по вывезенному журналу не
// находит работы. Без этого «доезжает» было бы совместимо с бесконечной
// перепоставкой той же строки.
func TestDeliveredRowIsNeverClaimedAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sink := newRecordingSink()
	sh := newTestShipper(t, pool, sink, metrics.NewMemRecorder())

	insertRow(t, pool, "aud01", "instance.create")
	first, err := sh.Pass(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, first.Shipped)

	second, err := sh.Pass(context.Background())
	require.NoError(t, err)
	require.Zero(t, second.Shipped)
	require.Len(t, sink.delivered(), 1)
	require.Equal(t, 1, readRow(t, pool, "aud01").attempts)
}

// TestRefusedRowKeepsItsPlaceAndIsRetried — отказ приёмника не теряет запись и
// не объявляет её негодной: строка остаётся недоставленной, ждёт и доезжает,
// когда приёмник поправился.
func TestRefusedRowKeepsItsPlaceAndIsRetried(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sink := newRecordingSink()
	sink.refuse("aud01", true)
	sh := newTestShipper(t, pool, sink, metrics.NewMemRecorder())

	insertRow(t, pool, "aud01", "instance.create")

	res, err := sh.Pass(context.Background())
	require.NoError(t, err)
	require.Zero(t, res.Shipped)
	require.Equal(t, 1, res.Deferred)

	row := readRow(t, pool, "aud01")
	require.Equal(t, "pending", row.status, "отказ приёмника оставляет запись в очереди")
	require.Equal(t, 1, row.attempts)
	require.NotNil(t, row.lastError)
	require.True(t, row.nextAt.After(time.Now()), "повтор обязан ЖДАТЬ: иначе отказавший приёмник получает шквал")

	sink.refuse("aud01", false)
	require.Eventually(t, func() bool {
		r, perr := sh.Pass(context.Background())
		return perr == nil && r.Shipped == 1
	}, 3*time.Second, 25*time.Millisecond)

	after := readRow(t, pool, "aud01")
	require.Equal(t, "sent", after.status)
	require.Equal(t, 2, after.attempts)
	require.Nil(t, after.lastError)
}

// TestPermanentlyRefusedRowNeverWedgesTheQueue — ОТРИЦАНИЕ к пробе доставки:
// запись, которую приёмник не принимает НИКОГДА, не заклинивает журнал ни в
// этом проходе, ни в последующих, и сама остаётся недоставленной, а не
// объявляется негодной.
//
// Отказная запись стоит ПЕРВОЙ по времени — иначе проба зеленела бы на любой
// реализации, которая до неё просто не дошла.
func TestPermanentlyRefusedRowNeverWedgesTheQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sink := newRecordingSink()
	sink.refuse("aud01", true)
	sh := newTestShipper(t, pool, sink, metrics.NewMemRecorder())

	insertRow(t, pool, "aud01", "instance.create")
	insertRow(t, pool, "aud02", "instance.delete")
	insertRow(t, pool, "aud03", "instance.update")

	res, err := sh.Pass(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, res.Shipped, "преемники отказной записи обязаны доехать в ТОМ ЖЕ проходе")
	require.Equal(t, 1, res.Deferred)
	require.Equal(t, "sent", readRow(t, pool, "aud02").status)
	require.Equal(t, "sent", readRow(t, pool, "aud03").status)

	// Сколько бы проходов ни прошло, отказная строка остаётся ждущей, а новые
	// записи вывозятся мимо неё.
	for i := 4; i <= 6; i++ {
		insertRow(t, pool, fmt.Sprintf("aud%02d", i), "instance.create")
	}
	// Условие ждёт ОБЕ половины сразу: преемники вывезены И отказная строка
	// получила новую попытку. Половина «преемники вывезены» выполняется на
	// первом же проходе, поэтому одна она доказывала бы только то, что вывоз до
	// отказной строки не дошёл.
	require.Eventually(t, func() bool {
		if _, perr := sh.Pass(context.Background()); perr != nil {
			return false
		}
		return readRow(t, pool, "aud06").status == "sent" &&
			readRow(t, pool, "aud01").attempts >= 2
	}, 3*time.Second, 25*time.Millisecond)

	stuck := readRow(t, pool, "aud01")
	require.Equal(t, "pending", stuck.status,
		"терминального состояния у журнала НЕТ: запись аудита не выбрасывают из-за нашей же неисправности")
	require.Nil(t, stuck.sentAt)
	require.GreaterOrEqual(t, stuck.attempts, 2, "попытки продолжаются и это видно счётчиком")
	for i := 4; i <= 6; i++ {
		require.Equal(t, "sent", readRow(t, pool, fmt.Sprintf("aud%02d", i)).status)
	}
}

// TestRunStopsWithContext — цикл вывоза завершается по отмене, а не переживает
// остановку службы, и завершается ШТАТНО: отмена по сигналу остановки не
// является отказом службы и не должна читаться так вызывающим.
func TestRunStopsWithContext(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sh := newTestShipper(t, pool, newRecordingSink(), metrics.NewMemRecorder())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sh.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("цикл вывоза не завершился по отмене")
	}
}

// TestRefusalTextTheDatabaseRejectsDoesNotRedeliverTheBatch — отказ, чей ТЕКСТ
// база не принимает, ограничен своей строкой.
//
// # Что здесь опровергается
//
// Прежняя редакция объявляла «заклинить очередь нельзя by construction»,
// подразумевая полосу отказа приёмника. Полоса отказа ЗАПИСИ повтора этим
// свойством не обладала: запись причины шла тем же оператором, что и пометка
// доставленных, поэтому отвергнутый базой текст отменял транзакцию целиком —
// соседи, УЖЕ ОТДАННЫЕ приёмнику, оставались ждущими и уезжали к нему снова на
// каждом проходе. Это не гипотеза: приёмник — экспортированный интерфейс, а
// предикат пересмотра решения предусматривает внешний накопитель, чей ответ мы
// не сочиняем.
//
// # Почему нулевой байт
//
// Это самый дешёвый текст, который Postgres в `text` не принимает вовсе, — то
// есть настоящий вход, а не выдуманный. Обеззараживание причины делает его
// безвредным, а точка сохранения ограничивает радиус ЛЮБОГО иного отказа записи.
//
// Утверждается ПАРА: соседи доезжают (иначе проба зеленела бы на реализации,
// которая просто ничего не доставляет) И отказная строка остаётся ждущей.
func TestRefusalTextTheDatabaseRejectsDoesNotRedeliverTheBatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sink := newRecordingSink()
	sh := newTestShipper(t, pool, sink, metrics.NewMemRecorder())

	// Отказная — ПЕРВАЯ по времени: партия обрабатывается по порядку создания,
	// и на любой другой позиции проба не проверяла бы соседей после неё.
	insertRow(t, pool, "aud01", "instance.create")
	sink.refuseWith("aud01", "внешний накопитель ответил: \x00 неразбираемое тело")
	for _, id := range []string{"aud02", "aud03", "aud04", "aud05"} {
		insertRow(t, pool, id, "instance.create")
	}

	res, err := sh.Pass(context.Background())
	require.NoError(t, err, "отказ ОДНОЙ строки не роняет проход")
	require.Equal(t, 4, res.Shipped, "соседи по партии обязаны доехать")
	require.Equal(t, 1, res.Deferred, "отказная строка обязана получить повтор")
	require.Zero(t, res.Stuck, "обеззараженная причина записывается — застрявших быть не должно")

	for _, id := range []string{"aud02", "aud03", "aud04", "aud05"} {
		require.Equal(t, "sent", readRow(t, pool, id).status,
			"строка %s отдана приёмнику и обязана быть помечена доставленной", id)
	}

	stuck := readRow(t, pool, "aud01")
	require.Equal(t, "pending", stuck.status)
	require.Equal(t, 1, stuck.attempts)
	require.NotNil(t, stuck.lastError)
	require.NotContains(t, *stuck.lastError, "\x00", "нулевой байт до колонки не доезжает")
	require.Contains(t, *stuck.lastError, "внешний накопитель ответил",
		"причина обязана остаться читаемой: обеззараживание — не стирание")

	// Второй проход: соседи уже доставлены и повторно приёмнику не уезжают.
	before := len(sink.delivered())
	res2, err := sh.Pass(context.Background())
	require.NoError(t, err)
	require.Zero(t, res2.Shipped)
	require.Equal(t, before, len(sink.delivered()),
		"повторной доставки доставленных быть не должно — именно её давал откат партии")
}

// TestPassEndsWhenNoRowMoves — проход прекращается, если партия не сдвинула ни
// одной строки.
//
// # Зачем это отдельная проба
//
// Отказ записи повтора больше не роняет партию — значит строка остаётся
// заклеймляемой со своим прежним временем повтора, и цикл прохода, который
// продолжается «пока что-то заклеймлено», крутился бы вечно внутри одного тика:
// служба перестала бы останавливаться. Условием выхода поэтому служит СДВИГ, а
// не число заклеймлённых, и это утверждается здесь.
//
// Условие «повтор не записывается» создаётся честно — колонка причины снимается
// на время пробы, — а не подменой кода: проба обязана видеть тот же путь, что и
// продукт.
func TestPassEndsWhenNoRowMoves(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sink := newRecordingSink()
	sink.refuse("aud01", true)
	sh := newTestShipper(t, pool, sink, metrics.NewMemRecorder())

	insertRow(t, pool, "aud01", "instance.create")

	_, err := pool.Exec(context.Background(),
		`ALTER TABLE audit_journal DROP COLUMN last_error`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`ALTER TABLE audit_journal ADD COLUMN last_error text`)
	})

	done := make(chan error, 1)
	var res audit.PassResult
	go func() {
		var perr error
		res, perr = sh.Pass(context.Background())
		done <- perr
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "непишущийся повтор — не отказ прохода")
		require.Equal(t, 1, res.Stuck, "строка обязана быть названа застрявшей, а не отложенной")
		require.Zero(t, res.Deferred, "сдвига не было — показывать его нельзя")
	case <-time.After(10 * time.Second):
		t.Fatal("проход не завершился: партия без сдвига заклеймляется снова и снова")
	}
}

// TestUnparseableRowDoesNotWedgeTheJournal — строка, которую не удалось
// РАЗОБРАТЬ, не задерживает соседей.
//
// # Почему это отдельный класс от отказа приёмника
//
// Отказ приёмника — про доставку, и он был покрыт с самого начала. Разбор идёт
// РАНЬШЕ доставки, на пути клейма, и его отказ прежде ронял весь проход: такую
// строку клейм возвращает всегда, поэтому журнал не доставлял бы НИКОГО и ни
// одной попытки не делалось бы — то есть очередь вставала бы целиком из-за одной
// строки. Гейт, прикрывающий очередь со стороны формы полезной нагрузки, этот
// путь не видит: он смотрит на объявление колонки, а разбирается здесь строка
// целиком (`to_jsonb(t.*)`).
//
// Утверждается ПАРА: соседи доезжают И неразобранная остаётся ждущей с причиной
// в строке — иначе проба зеленела бы на реализации, которая такую строку молча
// объявляет доставленной.
func TestUnparseableRowDoesNotWedgeTheJournal(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool := newPool(t)
	sink := newRecordingSink()
	sh := newTestShipper(t, pool, sink, metrics.NewMemRecorder())

	// Неразбираемая — ПЕРВАЯ по времени: на любой другой позиции проба не
	// проверяла бы соседей после неё.
	//
	// Условие создаётся ЧЕСТНО — типом столбца, а не подменой кода: клейм
	// по-прежнему инкрементирует попытку тем же оператором, `to_jsonb`
	// по-прежнему собирает строку целиком, и разбор идёт тот же, что в проде.
	// Дробное число попыток целым не читается — это и есть отказ разбора.
	insertRow(t, pool, "aud01", "instance.create")
	_, err := pool.Exec(context.Background(),
		`ALTER TABLE audit_journal ALTER COLUMN attempts TYPE numeric`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`UPDATE audit_journal SET attempts = trunc(attempts)`)
		_, _ = pool.Exec(context.Background(),
			`ALTER TABLE audit_journal ALTER COLUMN attempts TYPE int USING attempts::int`)
	})
	_, err = pool.Exec(context.Background(),
		`UPDATE audit_journal SET attempts = 0.5 WHERE id = 'aud01'`)
	require.NoError(t, err)

	for _, id := range []string{"aud02", "aud03"} {
		insertRow(t, pool, id, "instance.create")
	}

	res, err := sh.Pass(context.Background())
	require.NoError(t, err, "неразбираемая строка не роняет проход")
	require.Equal(t, 2, res.Shipped, "соседи по партии обязаны доехать")
	require.Equal(t, 1, res.Deferred, "неразобранная обязана получить повтор, а не пропасть")

	// Тип столбца возвращается ДО чтения: помощник пробы читает попытку целым,
	// и без этого падал бы он, а не предмет.
	_, err = pool.Exec(context.Background(),
		`ALTER TABLE audit_journal ALTER COLUMN attempts TYPE int USING trunc(attempts)::int`)
	require.NoError(t, err)

	for _, id := range []string{"aud02", "aud03"} {
		require.Equal(t, "sent", readRow(t, pool, id).status, "строка %s обязана доехать", id)
	}
	bad := readRow(t, pool, "aud01")
	require.Equal(t, "pending", bad.status, "неразобранная не объявляется доставленной")
	require.NotNil(t, bad.lastError)
	require.Contains(t, *bad.lastError, "разобрать строку",
		"причина обязана называть предмет: оператор чинит по ней")
	for _, r := range sink.delivered() {
		require.NotEqual(t, "aud01", r.ID,
			"неразобранную приёмнику не предлагают — предлагать нечего")
	}
}
