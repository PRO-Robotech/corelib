// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package dbready — ограниченное ожидание готовности Postgres принимать соединения.
//
// Зачем. Init-контейнер `migrate` стартует одновременно с подом Postgres и не
// ждёт его. Мигратор при этом умирает на ПЕРВОЙ же неудаче (`log.Fatalf`), под
// уходит в CrashLoopBackOff и «самоизлечивается» только когда PG наконец поднялся.
// Формально это работает, фактически — шум, который маскирует НАСТОЯЩИЕ сбои
// старта: в логах и событиях невозможно отличить «PG ещё не успел» от «неверный
// пароль» / «миграция сломана», потому что обе выглядят одинаково — рестарт.
//
// Что делает пакет. Ждёт ТОЛЬКО класс «БД ещё не принимает соединения»
// (см. IsNotReady) и ТОЛЬКО в пределах бюджета. Любая другая ошибка возвращается
// немедленно — конфигурационный дефект обязан падать сразу, а не прятаться под
// двухминутным ожиданием.
//
// Почему именно Ping, а не «обернуть sql.Open в retry». `sql.Open` ЛЕНИВ: он не
// дозванивается до сервера и почти никогда не ошибается. Гонка проявляется позже —
// на первом реальном запросе (у нас это `goose.Up`). Retry вокруг `sql.Open` был
// бы полностью бесполезен; поэтому барьер ставится явным `PingContext` ДО goose.
package dbready

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/corelib/backoff"
)

// SQLState-коды, означающие «сервер жив, но пока не обслуживает» —
// не путать с ошибками аутентификации/схемы.
const (
	// SQLStateCannotConnectNow — 57P03 "the database system is starting up".
	// Канонический признак того, что PG поднялся, но ещё проигрывает WAL.
	SQLStateCannotConnectNow = "57P03"
	// SQLStateAdminShutdown — 57P01, сервер завершается (rolling restart).
	SQLStateAdminShutdown = "57P01"
	// SQLStateCrashShutdown — 57P02, сервер перезапускается после сбоя бэкенда.
	SQLStateCrashShutdown = "57P02"
	// SQLStateTooManyConnections — 53300; на bring-up пул ещё не разошёлся.
	SQLStateTooManyConnections = "53300"
)

// Значения по умолчанию. Бюджет 2 минуты покрывает холодный старт Postgres
// (initdb + WAL replay) с запасом и при этом ОГРАНИЧЕН: вечно висящий
// init-контейнер хуже CrashLoopBackOff — он не даёт ни сигнала, ни рестарта.
const (
	DefaultInitialInterval = 250 * time.Millisecond
	DefaultMaxInterval     = 5 * time.Second
	DefaultMaxElapsed      = 2 * time.Minute
)

// Pinger — минимальный контракт, который реализует *sql.DB. Интерфейс (а не
// *sql.DB) держит пакет юнит-тестируемым без живого Postgres.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Options — параметры ожидания. Нулевое значение валидно: каждое поле
// подставляется дефолтом.
type Options struct {
	InitialInterval time.Duration
	MaxInterval     time.Duration
	MaxElapsed      time.Duration
}

func (o Options) withDefaults() Options {
	if o.InitialInterval <= 0 {
		o.InitialInterval = DefaultInitialInterval
	}
	if o.MaxInterval <= 0 {
		o.MaxInterval = DefaultMaxInterval
	}
	if o.MaxElapsed <= 0 {
		o.MaxElapsed = DefaultMaxElapsed
	}
	return o
}

// Wait пингует БД, пока она не начнёт отвечать, но не дольше opts.MaxElapsed.
//
// Возврат:
//   - nil — БД принимает соединения;
//   - исходная ошибка, если она НЕ из класса «не готов» (fail-fast, без ретраев);
//   - ошибка с бюджетом и последней причиной, если БД так и не поднялась;
//   - ctx.Err(), если контекст отменён (SIGTERM init-контейнера).
//
// РЕПЛИКИ: запрос — петля принадлежит вызову — ожиданию готовности базы при старте — и
// завершается по его условию. Ничего не пишет: только опрашивает.
func Wait(ctx context.Context, p Pinger, opts Options) error {
	opts = opts.withDefaults()

	bo := backoff.ExponentialBackoffBuilder().
		WithInitialInterval(opts.InitialInterval).
		WithMaxInterval(opts.MaxInterval).
		WithMaxElapsedThreshold(opts.MaxElapsed).
		Build()

	var lastErr error
	for {
		err := p.PingContext(ctx)
		if err == nil {
			return nil
		}
		// Отмена контекста — не «БД не готова»: выходим немедленно, иначе
		// init-контейнер игнорировал бы SIGTERM до конца бюджета.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !IsNotReady(err) {
			// Настоящая ошибка (пароль/БД/привилегии/TLS) — падаем сразу,
			// сохраняя исходный текст: он и есть диагноз.
			return err
		}
		lastErr = err

		next := bo.NextBackOff()
		if next == backoff.Stop {
			return fmt.Errorf("database not ready after %s: %w", opts.MaxElapsed, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(next):
		}
	}
}

// IsNotReady сообщает, означает ли ошибка «Postgres ещё не принимает соединения».
//
// Классификация — сердце пакета: false там, где нужно true, вернёт
// CrashLoopBackOff; true там, где нужно false, спрячет конфигурационный дефект
// (неверный пароль, несуществующая БД) под таймаутом ожидания. Поэтому список
// намеренно УЗКИЙ: транспорт (dial/DNS/reset/EOF) + SQLSTATE-класс 08
// (connection_exception) + явные «сервер стартует/выключается/перегружен».
// Всё остальное — включая 28P01 (bad password), 3D000 (no such database),
// 42501 (no privilege) и любые ошибки самих миграций — настоящая ошибка.
func IsNotReady(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// Транспорт: сервиса/эндпоинта ещё нет, порт закрыт, соединение оборвалось
	// на середине хендшейка.
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// Сервер ответил на протокольном уровне и назвал SQLSTATE.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case SQLStateCannotConnectNow,
			SQLStateAdminShutdown,
			SQLStateCrashShutdown,
			SQLStateTooManyConnections:
			return true
		}
		// Класс 08 — connection_exception целиком.
		if strings.HasPrefix(pgErr.Code, "08") {
			return true
		}
		// Любой другой SQLSTATE — настоящая ошибка, ретраить нельзя.
		return false
	}

	return false
}
