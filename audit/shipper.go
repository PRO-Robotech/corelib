// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/corelib/outbox"
	"github.com/PRO-Robotech/corelib/outbox/metrics"
)

// bookkeepingColumns — колонки, описывающие ДОСТАВКУ, а не действие.
//
// Они вычитаются из записи, уезжающей приёмнику: читатель журнала спрашивает
// «кто и что сделал», и число попыток доставки на этот вопрос не отвечает.
// Перечень объявлен ОДИН раз и здесь же используется — иначе «что считается
// учётным» разошлось бы между вывозом и его пробами молча.
var bookkeepingColumns = []string{
	"status", "attempts", "next_attempt_at", "sent_at", "last_error",
	// Эти три уезжают отдельными полями [Record], поэтому в Fields они были бы
	// вторым написанием одного значения.
	"id", "event_type", "created_at",
}

// ShipperConfig — настройки вывоза журнала.
type ShipperConfig struct {
	// Table — полное имя таблицы журнала (`<схема>.<таблица>`).
	Table string
	// BatchSize — сколько строк заклеймить за один заход (умолчание 256).
	//
	// Проход вывозит ВСЮ доступную голову, а не один батч: иначе темп вывоза
	// стал бы функцией темпа записи, и очередь, наполняемая быстрее батча за
	// такт, не разгружалась бы никогда.
	BatchSize int
	// Interval — пауза между проходами (умолчание 5s).
	//
	// Пробуждения уведомлением у журнала нет НАМЕРЕННО: требования к задержке
	// вывоза аудита нет, а канал уведомления — это ещё одно соединение со своим
	// переподключением и своим отказом. Триггер уведомления был снят вместе с
	// дренажом, которого не существовало; возвращать его не за чем.
	Interval time.Duration
	// BackoffMin/BackoffMax — пауза перед повтором после отказа приёмника
	// (умолчания 1s / 5m). Пауза хранится В СТРОКЕ (`next_attempt_at`), а не в
	// памяти: перезапуск службы иначе обнулял бы её, и отказавший приёмник
	// получал бы шквал повторов вместо разрежённых.
	BackoffMin time.Duration
	BackoffMax time.Duration
}

func (c ShipperConfig) withDefaults() ShipperConfig {
	if c.BatchSize <= 0 {
		c.BatchSize = 256
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Second
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = time.Second
	}
	if c.BackoffMax < c.BackoffMin {
		c.BackoffMax = 5 * time.Minute
	}
	if c.BackoffMax < c.BackoffMin {
		c.BackoffMax = c.BackoffMin
	}
	return c
}

// PassResult — исход одного прохода, разложенный по полосам.
//
// Одно число («обработано») слило бы доставку с отказом приёмника, и «журнал
// вывозится» с «журнал перестал вывозиться» выглядели бы одинаково.
type PassResult struct {
	// Shipped — принято приёмником.
	Shipped int
	// Deferred — приёмник не принял, повтор назначен.
	Deferred int
	// Stuck — приёмник не принял, и НАЗНАЧИТЬ ПОВТОР не удалось.
	//
	// Третий исход заведён отдельным числом, потому что он означает не то же
	// самое: строка остаётся ждущей, но её учётное состояние не сдвинулось, и
	// следующий проход возьмёт её снова. Свести её к Deferred значило бы
	// показывать сдвиг, которого не было.
	Stuck int
}

func (r *PassResult) add(o PassResult) {
	r.Shipped += o.Shipped
	r.Deferred += o.Deferred
	r.Stuck += o.Stuck
}

// progressed — сдвинулось ли учётное состояние хоть одной строки партии.
//
// Партия без сдвига означает, что следующий клейм вернёт ТЕ ЖЕ строки: проход,
// продолжающий цикл в этом положении, крутится вечно. Поэтому цикл прохода
// спрашивает именно про сдвиг, а не про число заклеймённых.
func (r PassResult) progressed() bool { return r.Shipped > 0 || r.Deferred > 0 }

func (r PassResult) empty() bool { return r.Shipped == 0 && r.Deferred == 0 && r.Stuck == 0 }

// Shipper вывозит строки журнала аудита в приёмник.
//
// # Порядка между строками НЕТ, и это свойство, а не упущение
//
// Записи журнала — независимые факты о состоявшихся действиях; ни одна не
// отменяет другую. Поэтому у выборки нет условия «голова партиции», и строка,
// которую ПРИЁМНИК не принимает, преемников не задерживает: до назначенного
// времени повтора её просто нет в выборке, а привязки к ней ни у кого нет.
//
// Утверждение относится к полосе отказа ПРИЁМНИКА и только к ней. Отказ ЗАПИСИ
// повтора — другая полоса, и там свойство держится не построением, а кодом:
// запись идёт под точкой сохранения, её отказ ограничен своей строкой и партию
// не роняет, а проход прекращается, если партия не сдвинула ни одной строки
// (см. [Shipper.Pass], [Shipper.deferRow]). Различать полосы обязательно:
// прежняя редакция объявляла «не заклинивает ничего by construction» и была
// опровергнута приёмником, чей текст отказа база не принимает.
//
// Этим журнал и отличается от очереди намерений, где `+` и `−` по одному ключу
// не коммутативны и порядок обязан держаться клеймом по голове партиции.
type Shipper struct {
	pool *pgxpool.Pool
	sink Sink
	rec  metrics.Recorder
	log  *slog.Logger
	cfg  ShipperConfig

	claimSQL string
	sentSQL  string
	deferSQL string
}

// NewShipper строит вывоз. Все зависимости обязательны: вывоз без приёмника
// доставляет в никуда, а без счётчиков молчит так же, как отсутствующий.
func NewShipper(
	pool *pgxpool.Pool,
	sink Sink,
	rec metrics.Recorder,
	log *slog.Logger,
	cfg ShipperConfig,
) (*Shipper, error) {
	switch {
	case pool == nil:
		return nil, fmt.Errorf("audit.NewShipper: pool обязателен")
	case sink == nil:
		return nil, fmt.Errorf("audit.NewShipper: приёмник обязателен")
	case rec == nil:
		return nil, fmt.Errorf("audit.NewShipper: счётчики обязательны")
	case log == nil:
		return nil, fmt.Errorf("audit.NewShipper: логгер обязателен")
	case cfg.Table == "":
		return nil, fmt.Errorf("audit.NewShipper: ShipperConfig.Table обязателен")
	}
	cfg = cfg.withDefaults()
	sql := buildShipperSQL(cfg.Table)

	return &Shipper{
		pool: pool, sink: sink, rec: rec,
		log: log.With(slog.String("table", cfg.Table), slog.String("sink", sink.Name())),
		cfg: cfg,
		// cursor-list-table: audit_outbox
		//
		// Объявление выше читает гейт курсорных индексов
		// (`internal/repohygiene/listcursorindex.go`). Оно нужно потому, что имя
		// таблицы здесь ВЫЧИСЛЯЕТСЯ (`ShipperConfig.Table` → `<схема>.audit_outbox`),
		// а в тексте запроса стоит глагол форматирования: разбор, ищущий
		// `FROM <литерал>`, такое чтение не видит. Обе живые таблицы журнала
		// называются одинаково, схемы у них разные — гейт сверяется по имени без
		// схемы, поэтому одного объявления хватает на обе.
		//
		// Клейм держит блокировку строки до конца транзакции, поэтому вторая
		// реплика возьмёт другие строки, а срыв процесса откатит и клейм: строк
		// «навсегда в полёте» не бывает, и отдельного состояния для полёта тоже.
		claimSQL: sql.claim,
		sentSQL:  sql.sent,
		deferSQL: sql.defer_,
	}, nil
}

// Run вывозит журнал, пока жив контекст.
//
// РЕПЛИКИ: клейм — строки берутся `FOR UPDATE SKIP LOCKED`, и блокировка живёт
// до конца транзакции прохода, поэтому вторая реплика заклеймит ДРУГИЕ строки, а
// не те же. Срыв процесса откатывает клейм вместе с инкрементом попытки, и
// строка возвращается в выборку — «навсегда в полёте» не бывает. Дубля доставки
// это не исключает полностью (запись уезжает приёмнику ДО фиксации), но цена
// дубля у журнала — повторная запись того же факта, а не потеря.
//
// Отмена — ШТАТНОЕ завершение, поэтому возвращается nil: вызывающий поднимает
// вывоз рядом с прочими фоновыми задачами, и отмена по сигналу остановки не
// должна читаться там как отказ службы.
func (s *Shipper) Run(ctx context.Context) error {
	tick := time.NewTicker(s.cfg.Interval)
	defer tick.Stop()

	for {
		res, err := s.Pass(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			// Отказ БАЗЫ — не отказ приёмника: строки не тронуты, повтор
			// осмыслен на следующем такте.
			s.log.Warn("проход вывоза журнала аудита не состоялся", slog.String("err", err.Error()))
		case !res.empty():
			s.log.Info("журнал аудита вывезен",
				slog.Int("shipped", res.Shipped), slog.Int("deferred", res.Deferred))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// Pass — один проход: вывозит всю доступную голову журнала батчами.
//
// Возвращает разложенный исход И ошибку опроса базы отдельно: «вывезли ноль»,
// «вывозить было нечего» и «спросить не смогли» — три разных мира, и слить их в
// один ноль значило бы объявить отказ базы благополучием.
func (s *Shipper) Pass(ctx context.Context) (PassResult, error) {
	var total PassResult
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, claimed, err := s.shipBatch(ctx)
		total.add(res)
		if err != nil {
			return total, err
		}
		if claimed == 0 {
			return total, nil
		}
		if !res.progressed() {
			// Партия заклеймлена, но ни одна строка не сдвинулась: следующий
			// клейм вернёт её же. Проход обязан закончиться, иначе он крутится
			// вечно внутри одного тика и не даёт остановить службу.
			s.log.Error("вывоз журнала аудита не сдвинул ни одной строки партии — проход прекращён",
				slog.Int("claimed", claimed), slog.Int("stuck", res.Stuck))
			return total, nil
		}
	}
}

// shipBatch клеймит до BatchSize строк, доставляет их и фиксирует исход каждой
// в ТОЙ ЖЕ транзакции, что держит блокировку.
func (s *Shipper) shipBatch(ctx context.Context) (PassResult, int, error) {
	var res PassResult

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, 0, fmt.Errorf("audit: открыть транзакцию вывоза: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	records, err := s.claim(ctx, tx)
	if err != nil {
		return res, 0, err
	}
	if len(records) == 0 {
		return res, 0, nil
	}

	sent := make([]string, 0, len(records))
	for _, r := range records {
		shipErr := r.DecodeErr
		if shipErr == nil {
			shipErr = s.sink.Ship(ctx, r.Record)
		}
		if shipErr == nil {
			sent = append(sent, r.ID)
			res.Shipped++
			continue
		}
		if err := s.deferRow(ctx, tx, r.ID, r.Attempts, shipErr); err != nil {
			// Отказ ЗАПИСИ повтора не роняет партию: соседи по ней уже отданы
			// приёмнику, и откат всей транзакции отдал бы их повторно, оставив
			// ждущими. Строка остаётся ждущей со своим прежним временем повтора
			// — то есть хуже, чем назначено, но не хуже, чем было.
			res.Stuck++
			s.log.Error("не удалось назначить повтор записи журнала аудита",
				slog.String("id", r.ID), slog.Int("attempts", r.Attempts),
				slog.String("err", err.Error()))
			continue
		}
		s.log.Warn("приёмник журнала аудита не принял запись, назначен повтор",
			slog.String("id", r.ID), slog.Int("attempts", r.Attempts),
			slog.String("err", sanitizeCause(shipErr)))
		res.Deferred++
	}

	if len(sent) > 0 {
		if _, err := tx.Exec(ctx, s.sentSQL, sent); err != nil {
			return res, len(records), fmt.Errorf("audit: пометить доставленные: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, len(records), fmt.Errorf("audit: зафиксировать вывоз: %w", err)
	}
	return res, len(records), nil
}

// claimedRecord — запись плюс её учётное состояние на момент клейма.
type claimedRecord struct {
	Record
	// Attempts — число попыток ПОСЛЕ инкремента в клейме, то есть включая
	// текущую. Из него выводится длина паузы перед следующей.
	Attempts int
	// DecodeErr — строку не удалось разобрать. Приёмнику она не предлагается
	// (предлагать нечего), но и проход не роняет: повтор назначается по общему
	// пути, причина видна оператору в самой строке.
	DecodeErr error
}

func (s *Shipper) claim(ctx context.Context, tx pgx.Tx) ([]claimedRecord, error) {
	rows, err := tx.Query(ctx, s.claimSQL, s.cfg.BatchSize)
	if err != nil {
		return nil, fmt.Errorf("audit: заклеймить строки журнала: %w", err)
	}
	defer rows.Close()

	var out []claimedRecord
	for rows.Next() {
		var (
			id, eventType string
			createdAt     time.Time
			raw           []byte
		)
		if err := rows.Scan(&id, &eventType, &createdAt, &raw); err != nil {
			return nil, fmt.Errorf("audit: прочитать заклеймённую строку: %w", err)
		}
		fields, attempts, err := decodeRow(raw)
		if err != nil {
			// Неразбираемая строка НЕ роняет партию: иначе одна такая строка
			// заклинивала бы весь журнал — её всегда возвращает клейм, и проход
			// всегда падал бы на ней, не доставив никого. Она отправляется тем
			// же путём, что и отвергнутая приёмником: причина ложится в строку,
			// повтор назначается, оператор видит предмет в `last_error`.
			out = append(out, claimedRecord{
				Record:    Record{ID: id, EventType: eventType, CreatedAt: createdAt},
				Attempts:  attempts,
				DecodeErr: fmt.Errorf("audit: разобрать строку %s: %w", id, err),
			})
			continue
		}
		out = append(out, claimedRecord{
			Record:   Record{ID: id, EventType: eventType, CreatedAt: createdAt, Fields: fields},
			Attempts: attempts,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: перебрать заклеймённые строки: %w", err)
	}
	return out, nil
}

// decodeRow превращает строку журнала в поля записи и число попыток.
//
// Числа читаются как json.Number: приведение к float64 испортило бы длинное
// целое молча, а журнал обязан доехать тем, чем он был записан.
func decodeRow(raw []byte) (map[string]any, int, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var row map[string]any
	if err := dec.Decode(&row); err != nil {
		return nil, 0, err
	}
	attempts := 0
	if n, ok := row["attempts"].(json.Number); ok {
		v, err := n.Int64()
		if err != nil {
			return nil, 0, fmt.Errorf("attempts: %w", err)
		}
		if v > math.MaxInt32 {
			v = math.MaxInt32
		}
		attempts = int(v)
	}
	for _, k := range bookkeepingColumns {
		delete(row, k)
	}
	return row, attempts, nil
}

// deferRow назначает повтор одной строке.
//
// # Почему через точку сохранения
//
// Текст отказа приходит из ЧУЖОГО тела: приёмник — экспортированный интерфейс, и
// предикат пересмотра решения предусматривает внешний накопитель, чей ответ мы
// не сочиняем. Оператор, отвергнутый базой (а любой отказ оператора отменяет всю
// транзакцию Postgres), унёс бы вместе с собой пометки уже доставленных соседей
// — то есть отказ ОДНОЙ строки стал бы повторной доставкой ВСЕЙ партии, и так на
// каждом проходе. Вложенная транзакция ограничивает радиус отказа этой строкой.
func (s *Shipper) deferRow(ctx context.Context, tx pgx.Tx, id string, attempts int, cause error) error {
	wait := s.backoff(attempts)

	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("audit: точка сохранения для %s: %w", id, err)
	}
	if _, err := sp.Exec(ctx, s.deferSQL, id, wait.Seconds(), sanitizeCause(cause)); err != nil {
		_ = sp.Rollback(ctx)
		return fmt.Errorf("audit: назначить повтор %s: %w", id, err)
	}
	if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("audit: закрепить повтор %s: %w", id, err)
	}
	return nil
}

// sanitizeCause — текст чужого отказа, пригодный к записи и к чтению.
//
// Чужой текст не обязан быть ни правильным UTF-8, ни свободным от нулевого
// байта, ни коротким: он приходит из тела ответа приёмника. Postgres нулевой
// байт в `text` не принимает вовсе, и такой отказ — а он приходит РОВНО тогда,
// когда приёмник сломан, — превращался бы в отказ записи повтора. Поэтому текст
// приводится к пригодному виду ЗДЕСЬ, а не проверяется где-то выше по стеку.
//
// Обрезка длины намеренна: `last_error` — диагностика для оператора, а не
// хранилище чужих ответов; мегабайтная страница ошибки в строке очереди мешает
// читать очередь и ничего не добавляет к причине.
func sanitizeCause(cause error) string {
	if cause == nil {
		return ""
	}
	const limit = 1000
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(cause.Error(), "\uFFFD") {
		if b.Len() >= limit {
			b.WriteString("…")
			break
		}
		if r == 0 || (r < 0x20 && r != '\n' && r != '\t') {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// backoff — пауза перед следующей попыткой: удвоение от BackoffMin с потолком
// BackoffMax.
//
// Потолок обязателен: без него бессрочный повтор превратился бы в бессрочное
// ожидание, и строка, которую приёмник примет через час, ждала бы сутки.
func (s *Shipper) backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	wait := s.cfg.BackoffMin
	for i := 1; i < attempts && wait < s.cfg.BackoffMax; i++ {
		wait *= 2
	}
	if wait > s.cfg.BackoffMax {
		wait = s.cfg.BackoffMax
	}
	return wait
}

// buildShipperSQL собирает три оператора вывоза для одной таблицы.
//
// shipperSQL — три оператора вывоза, собранные для одной таблицы.
type shipperSQL struct {
	claim  string
	sent   string
	defer_ string //nolint:revive // подчёркивание — «defer» занят ключевым словом
}

// buildShipperSQL собирает операторы вывоза для одной таблицы.
//
// Состояния стоят в операторах ЛИТЕРАЛАМИ, а не глаголами форматирования.
// Причина не в стиле: предикат `status <> 'sent'` обязан читаться в тексте
// запроса дословно — по нему гейт курсорных индексов узнаёт, что
// частичный индекс журнала обслуживает именно этот обход, а гейт
// производителей — что значение словаря кто-то пишет. Подставленное
// форматированием, оно видно только в рантайме. Расхождение литерала с
// [StatusPending]/[StatusSent] ловит проба TestShipperSQLSpeaksTheDeclaredStates.
func buildShipperSQL(table string) shipperSQL {
	tbl := outbox.SanitizeTable(table)
	return shipperSQL{
		claim: fmt.Sprintf(`
			UPDATE %[1]s AS t
			   SET attempts = t.attempts + 1
			 WHERE t.id IN (
			     SELECT id FROM %[1]s
			      WHERE status <> 'sent'
			        AND next_attempt_at <= now()
			      ORDER BY created_at, id
			      FOR UPDATE SKIP LOCKED
			      LIMIT $1
			 )
			RETURNING t.id, t.event_type, t.created_at, to_jsonb(t.*)`, tbl),
		sent: fmt.Sprintf(
			`UPDATE %s SET status = 'sent', sent_at = now(), last_error = NULL WHERE id = ANY($1)`,
			tbl),
		defer_: fmt.Sprintf(
			`UPDATE %s SET status = 'pending', next_attempt_at = now() + make_interval(secs => $2),
			     last_error = $3 WHERE id = $1`, tbl),
	}
}
