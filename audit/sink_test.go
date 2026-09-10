// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/audit"
	"github.com/PRO-Robotech/corelib/observability"
)

// errWriter — носитель отказа записи. Ради него приёмник и зовёт обработчик
// напрямую: `slog.Logger.Info` ошибку записи ГЛОТАЕТ, поэтому приёмник,
// построенный на логгере, докладывал бы об успехе всегда — и полоса повтора
// была бы недостижима by construction.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// TestLogSinkWritesOneRecordWithEveryField — доставленная запись несёт ВСЕ поля
// строки журнала, а не только те, что знает общая оснастка доставки.
//
// Положительный контроль к паре ниже: без него «поле не доехало» было бы
// неотличимо от «приёмник ничего не пишет».
func TestLogSinkWritesOneRecordWithEveryField(t *testing.T) {
	var buf bytes.Buffer
	sink := audit.NewLogSink(observability.NewSlogger(&buf))

	created := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	err := sink.Ship(context.Background(), audit.Record{
		ID:        "aud00000000000000001",
		EventType: "instance.create",
		CreatedAt: created,
		Fields: map[string]any{
			"actor_id":      "usr01",
			"actor_type":    "user",
			"resource_id":   "ins01",
			"resource_type": "instance",
			"project_id":    "prj01",
			"payload":       map[string]any{"zone_id": "zn01"},
		},
	})
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1, "одна запись журнала — одна строка приёмника")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &got))

	rec, ok := got["audit"].(map[string]any)
	require.True(t, ok, "запись обязана лежать под своим ключом, а не вперемешку с полями логгера: %s", lines[0])
	require.Equal(t, "aud00000000000000001", rec["id"])
	require.Equal(t, "instance.create", rec["event_type"])
	require.Equal(t, "usr01", rec["actor_id"], "актор — то, ради чего журнал существует")
	require.Equal(t, "ins01", rec["resource_id"])
	require.Equal(t, "prj01", rec["project_id"])
	payload, ok := rec["payload"].(map[string]any)
	require.True(t, ok, "вложенный объект обязан доехать объектом, а не строкой")
	require.Equal(t, "zn01", payload["zone_id"])
}

// TestLogSinkReportsWriteFailure — отказ записи ДОХОДИТ до вызывающего.
//
// Приёмник, у которого отказ невыразим, делает повтор украшением: строка
// помечалась бы доставленной ровно тогда, когда доставки не было.
func TestLogSinkReportsWriteFailure(t *testing.T) {
	boom := errors.New("устройство отказало")
	sink := audit.NewLogSink(observability.NewSlogger(errWriter{err: boom}))

	err := sink.Ship(context.Background(), audit.Record{
		ID:        "aud00000000000000002",
		EventType: "instance.delete",
		CreatedAt: time.Now(),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, boom)
}

// TestLogSinkTreatsMutedReceiverAsFailure — приёмник, до которого запись не
// доедет из-за НАСТРОЙКИ уровня, отказывает громко, а не пропускает молча.
//
// Мягкий проход здесь означал бы контроль, не отказавший ни разу за всю жизнь:
// журнал помечался бы доставленным, а в потоке приёмника его не было бы вовсе.
func TestLogSinkTreatsMutedReceiverAsFailure(t *testing.T) {
	var buf bytes.Buffer
	muted := observability.NewSloggerLevel(&buf, slog.LevelError)
	sink := audit.NewLogSink(muted)

	err := sink.Ship(context.Background(), audit.Record{
		ID:        "aud00000000000000004",
		EventType: "instance.create",
		CreatedAt: time.Now(),
	})
	require.Error(t, err)
	require.Empty(t, buf.String())
}

// TestLogSinkPreflightDecidesTheSameQuestionAsShip — страж старта и путь
// доставки отвечают на один вопрос ОДИНАКОВО.
//
// # Зачем это утверждать отдельно
//
// Страж заведён затем, чтобы служба с заглушённым журналом не поднималась. Если
// он отвечает мягче пути доставки, служба поднимется, а строки не поедут — то
// есть страж будет присутствовать и не работать. Если строже — служба не
// поднимется там, где журнал бы доехал. Расходятся эти двое молча, поэтому
// утверждается их СОВПАДЕНИЕ на всей оси уровней, а не поведение каждого
// поодиночке.
func TestLogSinkPreflightDecidesTheSameQuestionAsShip(t *testing.T) {
	ctx := context.Background()
	rec := audit.Record{ID: "aud1", EventType: "iam.account.created", CreatedAt: time.Now()}

	for _, level := range []slog.Level{
		slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError,
	} {
		var buf bytes.Buffer
		sink := audit.NewLogSink(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})))

		pre := sink.Preflight(ctx)
		ship := sink.Ship(ctx, rec)

		if (pre == nil) != (ship == nil) {
			t.Fatalf("уровень %s: страж и доставка разошлись — страж %v, доставка %v",
				level, pre, ship)
		}
		// Обе половины оси обязаны встретиться: без этого проба зеленела бы на
		// приёмнике, отвергающем всё, и на приёмнике, не отвергающем ничего.
		if level <= slog.LevelInfo && pre != nil {
			t.Fatalf("уровень %s поток принимает — страж обязан молчать: %v", level, pre)
		}
		if level > slog.LevelInfo {
			if pre == nil {
				t.Fatalf("уровень %s заглушает журнал — страж обязан отказать", level)
			}
			if buf.Len() != 0 {
				t.Fatalf("уровень %s: в поток не должно уехать ничего, уехало %d байт",
					level, buf.Len())
			}
		}
	}
}
