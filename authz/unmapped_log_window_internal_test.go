// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestUnmappedLineCarriesTheSuppressedCountIntoTheNextWindow — подавленное не
// исчезает молча: первая строка следующего окна несёт число строк, не
// записанных в предыдущем. Сто вызовов в окне — одна строка и затем счётчик 99.
func TestUnmappedLineCarriesTheSuppressedCountIntoTheNextWindow(t *testing.T) {
	base := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	now := base
	var logs bytes.Buffer
	intr := NewInterceptor(InterceptorOptions{
		Cache:  NewCache(0),
		Map:    RPCMap{},
		Client: CheckClientFunc(func(context.Context, string, string, string) (bool, error) { return false, nil }),
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	intr.now = func() time.Time { return now }

	const method = "/kacho.cloud.vpc.v1.UnknownService/Foo"
	for i := 0; i < 100; i++ {
		intr.authorize(context.Background(), method, nil)
	}
	if strings.Contains(logs.String(), "suppressed=") {
		t.Fatalf("первая строка окна не может знать о подавленных после неё:\n%s", logs.String())
	}

	now = base.Add(unmappedLogWindow)
	intr.authorize(context.Background(), method, nil)

	lines := unmappedLines(logs.String())
	if len(lines) != 2 {
		t.Fatalf("строк за два окна: %d, ожидалось 2:\n%s", len(lines), logs.String())
	}
	if !strings.Contains(lines[1], "suppressed=99") {
		t.Fatalf("строка второго окна не несёт счётчика подавленных 99: %q", lines[1])
	}
	if !strings.Contains(lines[1], "rpc="+method) {
		t.Fatalf("строка второго окна не называет метод: %q", lines[1])
	}

	// Окно без подавленных не печатает счётчика — нулём «не было» не притворяется
	// «не считали».
	now = now.Add(unmappedLogWindow)
	intr.authorize(context.Background(), method, nil)
	lines = unmappedLines(logs.String())
	if len(lines) != 3 || strings.Contains(lines[2], "suppressed=") {
		t.Fatalf("третье окно: ожидалась строка без счётчика, получено:\n%s", logs.String())
	}
}

// TestUnmappedLineKeysAreBounded — ключи ограничения не растут без предела:
// сверх потолка вызовы делят одно окно переполнения, а не заводят новых ключей,
// и строка при этом по-прежнему называет свой метод.
func TestUnmappedLineKeysAreBounded(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	intr := NewInterceptor(InterceptorOptions{
		Cache:  NewCache(0),
		Map:    RPCMap{},
		Client: CheckClientFunc(func(context.Context, string, string, string) (bool, error) { return false, nil }),
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	intr.now = func() time.Time { return now }

	for i := 0; i < unmappedLogMaxKeys+50; i++ {
		intr.authorize(context.Background(), "/kacho.cloud.vpc.v1.S/M"+strconv.Itoa(i), nil)
	}
	if got := intr.unmappedLog.size(); got > unmappedLogMaxKeys+1 {
		t.Fatalf("ключей окна %d, потолок %d (+1 окно переполнения)", got, unmappedLogMaxKeys)
	}
	if got := len(unmappedLines(logs.String())); got != unmappedLogMaxKeys+1 {
		t.Fatalf("строк %d, ожидалось %d: по строке на ключ до потолка и одна на окно переполнения", got, unmappedLogMaxKeys+1)
	}
}

func unmappedLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "msg=authz_unmapped_rpc") {
			out = append(out, line)
		}
	}
	return out
}
