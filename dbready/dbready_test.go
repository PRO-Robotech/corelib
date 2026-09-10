// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dbready_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/dbready"
)

// fakePinger отдаёт ошибки из очереди, затем nil. Считает попытки.
type fakePinger struct {
	errs  []error
	calls int
}

func (f *fakePinger) PingContext(ctx context.Context) error {
	f.calls++
	if f.calls <= len(f.errs) {
		return f.errs[f.calls-1]
	}
	return nil
}

// fastOpts — тестовый профиль: интервалы в микросекундах, чтобы retry-петля не
// добавляла wall-clock в unit-прогон (детерминированно, без time.Sleep-эвристик).
func fastOpts() dbready.Options {
	return dbready.Options{
		InitialInterval: time.Microsecond,
		MaxInterval:     time.Microsecond,
		MaxElapsed:      2 * time.Second,
	}
}

func connRefused() error {
	return &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connect: connection refused"),
	}
}

func pgErr(code string) error {
	return &pgconn.PgError{Code: code, Message: "pg says " + code}
}

// TestWait_ReadyImmediately — счастливый путь: PG уже принимает соединения,
// ни одной лишней попытки, ни одной секунды ожидания.
func TestWait_ReadyImmediately(t *testing.T) {
	p := &fakePinger{}
	require.NoError(t, dbready.Wait(context.Background(), p, fastOpts()))
	require.Equal(t, 1, p.calls, "ready DB must be pinged exactly once")
}

// TestWait_RetriesUntilReady — ЯДРО P0-4: «PG ещё не поднялся» — не фатальная
// ошибка. Пока сейчас мигратор умирает на первой же попытке (log.Fatalf) и
// уходит в CrashLoopBackOff, здесь он обязан дождаться.
func TestWait_RetriesUntilReady(t *testing.T) {
	p := &fakePinger{errs: []error{
		connRefused(),
		pgErr(dbready.SQLStateCannotConnectNow),
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")},
	}}
	require.NoError(t, dbready.Wait(context.Background(), p, fastOpts()))
	require.Equal(t, 4, p.calls, "must retry each not-ready error and then succeed")
}

// TestWait_FailsFastOnRealError — вторая половина контракта: НАСТОЯЩАЯ ошибка
// обязана падать сразу, а не маскироваться двухминутным ожиданием. Неверный
// пароль/несуществующая БД — это конфигурационный дефект, ретраить его значит
// прятать причину и удлинять инцидент.
func TestWait_FailsFastOnRealError(t *testing.T) {
	for name, code := range map[string]string{
		"invalid_password":                  "28P01",
		"invalid_authorization":             "28000",
		"invalid_catalog_name (no such db)": "3D000",
		"insufficient_privilege":            "42501",
	} {
		t.Run(name, func(t *testing.T) {
			p := &fakePinger{errs: []error{pgErr(code), pgErr(code), pgErr(code)}}
			err := dbready.Wait(context.Background(), p, fastOpts())
			require.Error(t, err)
			require.Equal(t, 1, p.calls, "a real error must not be retried")
			require.Containsf(t, err.Error(), code,
				"error must surface the underlying SQLSTATE, not a generic wait timeout")
		})
	}
}

// TestWait_FailsFastOnNonPgError — не-pg ошибка без сетевой природы (напр. кривой
// DSN, TLS-отказ) тоже не «не готов».
func TestWait_FailsFastOnNonPgError(t *testing.T) {
	p := &fakePinger{errs: []error{errors.New("tls: failed to verify certificate")}}
	err := dbready.Wait(context.Background(), p, fastOpts())
	require.Error(t, err)
	require.Equal(t, 1, p.calls)
	require.Contains(t, err.Error(), "tls")
}

// TestWait_BoundedByMaxElapsed — ожидание ОГРАНИЧЕНО: вечно висящий init-контейнер
// хуже CrashLoopBackOff (никакого сигнала, никакого рестарта). По исчерпании
// бюджета Wait возвращает ошибку, называя и бюджет, и последнюю причину.
func TestWait_BoundedByMaxElapsed(t *testing.T) {
	p := &fakePinger{errs: make([]error, 1_000_000)}
	for i := range p.errs {
		p.errs[i] = connRefused()
	}

	opts := dbready.Options{
		InitialInterval: time.Millisecond,
		MaxInterval:     time.Millisecond,
		MaxElapsed:      50 * time.Millisecond,
	}
	start := time.Now()
	err := dbready.Wait(context.Background(), p, opts)
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "must give up at the configured budget")
	require.Contains(t, err.Error(), "connection refused", "must surface the last underlying cause")
	require.Truef(t, strings.Contains(err.Error(), "not ready"),
		"error must say the DB never became ready, got: %v", err)
}

// TestWait_HonoursContextCancel — внешняя отмена (SIGTERM init-контейнера) не
// должна дожидаться конца бюджета.
func TestWait_HonoursContextCancel(t *testing.T) {
	p := &fakePinger{errs: make([]error, 1000)}
	for i := range p.errs {
		p.errs[i] = connRefused()
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	opts := dbready.Options{
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     5 * time.Millisecond,
		MaxElapsed:      time.Hour,
	}
	err := dbready.Wait(ctx, p, opts)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

// TestIsNotReady_Classification — таблица классификации отдельно от петли: именно
// она отделяет «БД ещё не принимает соединения» от «настоящей ошибки». Ошибка в
// ней либо вернёт CrashLoopBackOff (false там, где надо true), либо спрячет
// конфигурационный дефект под таймаутом (true там, где надо false).
func TestIsNotReady_Classification(t *testing.T) {
	notReady := map[string]error{
		"dial connection refused":  connRefused(),
		"dns not resolvable yet":   &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", IsNotFound: true}},
		"io timeout":               &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")},
		"unexpected EOF":           io.ErrUnexpectedEOF,
		"57P03 cannot_connect_now": pgErr(dbready.SQLStateCannotConnectNow),
		"57P01 admin_shutdown":     pgErr("57P01"),
		"57P02 crash_shutdown":     pgErr("57P02"),
		"53300 too_many_conns":     pgErr("53300"),
		"08006 conn_failure":       pgErr("08006"),
		"08000 conn_exception":     pgErr("08000"),
		"wrapped conn refused":     fmt.Errorf("failed to connect: %w", connRefused()),
	}
	for name, err := range notReady {
		t.Run("notready/"+name, func(t *testing.T) {
			require.Truef(t, dbready.IsNotReady(err), "%v must be classified as not-ready", err)
		})
	}

	real := map[string]error{
		"28P01 bad password":     pgErr("28P01"),
		"3D000 no such database": pgErr("3D000"),
		"42501 no privilege":     pgErr("42501"),
		"42P01 undefined table":  pgErr("42P01"),
		"23505 unique violation": pgErr("23505"),
		"plain error":            errors.New("boom"),
		"nil":                    nil,
	}
	for name, err := range real {
		t.Run("real/"+name, func(t *testing.T) {
			require.Falsef(t, dbready.IsNotReady(err), "%v must NOT be treated as not-ready", err)
		})
	}
}
