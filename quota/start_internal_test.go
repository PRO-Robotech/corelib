// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quota

// start_internal_test.go — подъём тянущего величины: бюджет первого прохода,
// его единственность и заметность состояния «ни одного прохода за всю жизнь»
// (#666).
//
// Пробы стоят ВНУТРИ пакета намеренно: предмет — сборка подъёма, а она не
// экспортируется. Вынести её наружу ради пробы значило бы расширить поверхность
// пакета под нужды проверки и объявить публичным то, чем никто не пользуется.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// budgetSource — источник, запоминающий, НЁС ЛИ контекст вызова срок.
//
// Утверждается наблюдаемое соседом: сосед видит срок либо не видит его. Проба на
// «поле конфигурации прочитано» зеленела бы и на проходе, который срока не
// передал.
type budgetSource struct {
	calls        int
	sawDeadline  []bool
	blockUntilCC bool
	err          error
	seen         chan struct{}
}

func (b *budgetSource) ListChangedSince(ctx context.Context, cursor string, _ int32) ([]Change, string, error) {
	b.calls++
	_, ok := ctx.Deadline()
	b.sawDeadline = append(b.sawDeadline, ok)
	if b.seen != nil {
		select {
		case b.seen <- struct{}{}:
		default:
		}
	}
	if b.blockUntilCC {
		<-ctx.Done()
		return nil, "", ctx.Err()
	}
	if b.err != nil {
		return nil, "", b.err
	}
	return nil, cursor, nil
}

// grantingProjection — проекция, которая проход отдаёт и ничего не помнит, кроме
// числа обращений. Строгость дублёра здесь не нужна: предмет проб — подъём, а не
// операторы.
type grantingProjection struct{ beats int }

func (g *grantingProjection) ClaimPass(context.Context, time.Duration) (string, bool, error) {
	return "", true, nil
}
func (g *grantingProjection) ApplyChange(context.Context, Change) (int64, error) { return 0, nil }
func (g *grantingProjection) SaveCursor(context.Context, string, int64) error    { return nil }
func (g *grantingProjection) Heartbeat(context.Context) error                    { g.beats++; return nil }

func captureLogger() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	return &buf, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestStartSyncer_FirstPassCarriesItsOwnBudget — первый проход несёт СВОЙ срок.
//
// Без него проход идёт с контекстом процесса, у которого срока нет вовсе: сосед,
// принявший соединение и замолчавший, держал бы подъём сервиса столько, сколько
// сам захочет. У цикла бюджет был всегда — у первого прохода его не было.
func TestStartSyncer_FirstPassCarriesItsOwnBudget(t *testing.T) {
	src := &budgetSource{blockUntilCC: true}
	proj := &grantingProjection{}
	buf, logger := captureLogger()

	s, err := NewSyncer(src, proj, Config{Interval: time.Hour, PullTimeout: 30 * time.Millisecond}, logger)
	require.NoError(t, err)

	done := make(chan struct{})
	var stop func()
	go func() {
		defer close(done)
		stop = startSyncer(context.Background(), s, "kacho_vpc", logger)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("первый проход не вернулся: бюджета у него нет, и подъём держит замолчавший сосед")
	}
	if stop != nil {
		stop()
	}

	require.Equal(t, 1, src.calls)
	require.Equal(t, []bool{true}, src.sawDeadline,
		"сосед обязан увидеть срок: без него отказ первого прохода наступает только с падением процесса")
	require.Contains(t, buf.String(), "first limit sync failed")
}

// TestStartSyncer_FirstPassIsNotRepeated — проход, уже сделанный подъёмом, фоновый
// цикл НЕ повторяет.
//
// На стенде это давало ровно два отказа на каждый подъём каждого домена с
// разницей в десятки миллисекунд — и оба про одно и то же событие. Дубль не
// просто шумит: он удваивает нагрузку на соседа ровно в загрузочную бурю, то
// есть тогда, когда тот и без того на пределе.
func TestStartSyncer_FirstPassIsNotRepeated(t *testing.T) {
	src := &budgetSource{}
	proj := &grantingProjection{}
	buf, logger := captureLogger()

	// Тик заведомо дальше жизни пробы: всё, что будет позвано, позвано ПОДЪЁМОМ
	// либо немедленным проходом цикла — то есть ровно тем, что здесь считается.
	s, err := NewSyncer(src, proj, Config{Interval: time.Hour, PullTimeout: time.Second}, logger)
	require.NoError(t, err)

	stop := startSyncer(context.Background(), s, "kacho_vpc", logger)
	stop()

	require.Equal(t, 1, src.calls,
		"проход обязан быть один: подъём его уже сделал, и повтор в тот же миг ничего не узнаёт")
	require.Contains(t, buf.String(), "caught up")
}

// TestSyncer_NeverSynchronisedIsLoud — «ни одного прохода за всю жизнь» видно в
// строке отказа.
//
// Накопительные счётчики существовали и не имели ни одного читателя в прод-коде.
// Пока читателя нет, «предел не меняли» неотличимо от «тянущий мёртв» — и
// именно на этом различении спотыкается всякий разбор.
func TestSyncer_NeverSynchronisedIsLoud(t *testing.T) {
	src := &budgetSource{err: errors.New("peer refused"), seen: make(chan struct{}, 1)}
	proj := &grantingProjection{}
	buf, logger := captureLogger()

	s, err := NewSyncer(src, proj, Config{Interval: time.Hour, PullTimeout: time.Second}, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()

	<-src.seen // проход состоялся и отказал; строка об этом уже написана
	cancel()
	<-done

	require.Contains(t, buf.String(), "never_synchronised=true",
		"отказ тянущего, не сделавшего НИ ОДНОГО удачного прохода за всю жизнь, обязан говорить об этом: "+
			"иначе мёртвый тянущий неотличим от живого на неизменной конфигурации")

	h := s.Health()
	require.True(t, h.NeverSynchronised)
	require.Zero(t, h.Pulls)
	require.True(t, h.LastSuccess.IsZero())
}

// TestSyncer_HealthyPassClearsTheNeverFlag — контроль: удачный проход снимает
// признак, и отказ ПОСЛЕ него говорит уже о возрасте снимка, а не о смерти.
//
// Без этого контроля признак годился бы и в форме «всегда истинен»: строка
// присутствовала бы всегда и перестала бы что-либо означать — ровно та беда,
// которую она лечит.
func TestSyncer_HealthyPassClearsTheNeverFlag(t *testing.T) {
	src := &budgetSource{}
	proj := &grantingProjection{}
	buf, logger := captureLogger()

	s, err := NewSyncer(src, proj, Config{Interval: time.Hour, PullTimeout: time.Second}, logger)
	require.NoError(t, err)

	_, ran, rerr := s.RunOnce(context.Background())
	require.NoError(t, rerr)
	require.True(t, ran)

	h := s.Health()
	require.False(t, h.NeverSynchronised)
	require.Equal(t, int64(1), h.Pulls)
	require.False(t, h.LastSuccess.IsZero())

	require.NotContains(t, buf.String(), "never_synchronised=true")
	require.False(t, strings.Contains(buf.String(), "quota limit sync failed"))
}
