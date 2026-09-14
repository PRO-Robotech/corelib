// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// Задержка обслуженного вызова обязана быть НАБЛЮДАЕМОЙ, и наблюдаться она
// обязана так, чтобы отказ не смешивался с успехом.
//
// Пробы гоняют интерсептор так, как его гоняет сервер — с настоящим
// `grpc.UnaryServerInfo`, — и читают ЗНАЧЕНИЯ из реестра, а не факт вызова.
// Проверка «интерсептор позвал измеритель» осталась бы зелёной при измерителе,
// который кладёт величину не в тот ряд.

func observedValues(t *testing.T, reg *prometheus.Registry, name string) []*dto.Metric {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()
		}
	}
	return nil
}

func labelOf(m *dto.Metric, key string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == key {
			return l.GetValue()
		}
	}
	return ""
}

func TestServerLatency_ObservesDurationAndOutcome(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	itc := l.UnaryServerInterceptor(grpcsrv.ListenerPublic)
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Get"}

	_, err = itc(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil })
	require.NoError(t, err)

	ms := observedValues(t, reg, "kacho_grpc_server_handling_seconds")
	require.Len(t, ms, 1, "успешный вызов обязан дать РОВНО ОДИН ряд задержки")
	require.Equal(t, "kacho.cloud.vpc.v1.NetworkService", labelOf(ms[0], "grpc_service"))
	require.Equal(t, "Get", labelOf(ms[0], "grpc_method"))
	require.Equal(t, "ok", labelOf(ms[0], "outcome"))
	require.Equal(t, uint64(1), ms[0].GetHistogram().GetSampleCount())
}

func TestServerLatency_FailureIsADifferentRow(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	itc := l.UnaryServerInterceptor(grpcsrv.ListenerPublic)
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Create"}

	_, _ = itc(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil })
	_, err = itc(context.Background(), nil, info,
		func(context.Context, any) (any, error) {
			return nil, status.Error(codes.FailedPrecondition, "network is not empty")
		})
	require.Error(t, err)

	// Задержка отказа и задержка успеха — РАЗНЫЕ величины: быстрый отказ
	// занижает хвост, медленный завышает. Смешать их значит получить число,
	// которое не описывает ни один из двух случаев.
	ms := observedValues(t, reg, "kacho_grpc_server_handling_seconds")
	require.Len(t, ms, 2, "успех и отказ обязаны лечь в РАЗНЫЕ ряды")

	outcomes := map[string]uint64{}
	for _, m := range ms {
		outcomes[labelOf(m, "outcome")] = m.GetHistogram().GetSampleCount()
	}
	require.Equal(t, uint64(1), outcomes["ok"])
	require.Equal(t, uint64(1), outcomes["error"])

	// Полный код остаётся у счётчика: там ряд дёшев, и различить
	// «не хватило прав» от «предусловие не выполнено» без него нельзя.
	cs := observedValues(t, reg, "kacho_grpc_server_handled_total")
	require.Len(t, cs, 2)
	codesSeen := map[string]float64{}
	for _, m := range cs {
		codesSeen[labelOf(m, "grpc_code")] = m.GetCounter().GetValue()
	}
	require.Equal(t, float64(1), codesSeen["OK"])
	require.Equal(t, float64(1), codesSeen["FailedPrecondition"])
}

func TestServerLatency_BucketsResolveWhereDecisionsAreMade(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	_, _ = l.UnaryServerInterceptor(grpcsrv.ListenerPublic)(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.geo.v1.ZoneService/List"},
		func(context.Context, any) (any, error) { return "ok", nil })

	ms := observedValues(t, reg, "kacho_grpc_server_handling_seconds")
	require.Len(t, ms, 1)
	buckets := ms[0].GetHistogram().GetBucket()

	// Умолчание библиотеки начинается с 5 мс: чтение из своей базы укладывается
	// в единицы миллисекунд, и все такие вызовы свалились бы в ПЕРВУЮ корзину —
	// p50 и p90 стали бы неразличимы там, где живёт большинство запросов.
	require.LessOrEqual(t, buckets[0].GetUpperBound(), 0.001,
		"нижняя граница обязана разрешать субмиллисекундные чтения")

	// Потолок умолчания — 10 с; мутация с обращением к соседу и материализацией
	// прав законно переваливает за него, и хвост терял бы разрешение.
	require.GreaterOrEqual(t, buckets[len(buckets)-1].GetUpperBound(), 30.0,
		"верхняя граница обязана вмещать законно долгую мутацию")

	var between float64
	for _, b := range buckets {
		if ub := b.GetUpperBound(); ub >= 0.001 && ub <= 0.1 {
			between++
		}
	}
	require.GreaterOrEqual(t, between, float64(6),
		"сетка обязана сгущаться там, где принимаются решения — между миллисекундой и сотней")
}

func TestServerLatency_UnparsableMethodKeepsLabelsBounded(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	_, _ = l.UnaryServerInterceptor(grpcsrv.ListenerPublic)(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "мусор"},
		func(context.Context, any) (any, error) { return nil, errors.New("x") })

	ms := observedValues(t, reg, "kacho_grpc_server_handling_seconds")
	require.Len(t, ms, 1)
	require.Equal(t, "unknown", labelOf(ms[0], "grpc_service"),
		"неразобранное имя обязано дать ОГРАНИЧЕННУЮ метку, а не пустую: "+
			"пустая сливает разные методы в один ряд и делает величину неверной молча")
}

func TestServerLatency_NilMeterIsATransparentPassThrough(t *testing.T) {
	var l *grpcsrv.ServerLatency
	resp, err := l.UnaryServerInterceptor(grpcsrv.ListenerPublic)(context.Background(), "req",
		&grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"},
		func(_ context.Context, r any) (any, error) { return r, nil })
	require.NoError(t, err)
	require.Equal(t, "req", resp, "нулевой измеритель обязан пропускать вызов насквозь")
}

func TestServerLatency_DoubleRegistrationIsAnErrorNotAPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	// Повторная регистрация — ошибка ВЫЗЫВАЮЩЕГО, а не причина ронять процесс:
	// слушатель, поднятый дважды по недосмотру композиционного корня, обязан
	// сказать об этом, а не умереть в момент, когда причина уже не видна.
	_, err = grpcsrv.NewServerLatency(reg)
	require.Error(t, err)
}

// ── полоса слушателя ────────────────────────────────────────────────────────

// TestServerLatency_SameMethodOnBothListenersIsNotBlended — один и тот же метод,
// служимый ОБОИМИ слушателями, даёт РАЗНЫЕ ряды.
//
// Это не педантизм: `OperationService` в этом дереве регистрируется и на
// публичном слушателе, и на внутреннем. Публичный вызов приходит от арендатора
// через край и тащит за собой выяснение личности и вопрос о правах; внутренний
// приходит от соседнего модуля по mTLS. Профили задержки у них разные, а
// слитый ряд — среднее двух разных величин, то есть число, которое неверно про
// обе полосы сразу и молча.
func TestServerLatency_SameMethodOnBothListenersIsNotBlended(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.operation.v1.OperationService/Get"}
	pass := func(context.Context, any) (any, error) { return "ok", nil }

	_, err = l.UnaryServerInterceptor(grpcsrv.ListenerPublic)(context.Background(), nil, info, pass)
	require.NoError(t, err)
	_, err = l.UnaryServerInterceptor(grpcsrv.ListenerInternal)(context.Background(), nil, info, pass)
	require.NoError(t, err)

	ms := observedValues(t, reg, "kacho_grpc_server_handling_seconds")
	require.Len(t, ms, 2, "один метод на двух слушателях обязан дать ДВА ряда, а не один слитый")
	got := map[string]uint64{}
	for _, m := range ms {
		got[labelOf(m, "listener")] = m.GetHistogram().GetSampleCount()
	}
	require.Equal(t, map[string]uint64{"public": 1, "internal": 1}, got)
}

// TestServerLatency_UnknownListenerKeepsTheLabelBounded — полоса вне словаря
// схлопывается в одно значение.
//
// Метка обязана оставаться ОГРАНИЧЕННОЙ по числу значений: свободная строка в
// метке — это способ уронить хранилище рядами, которых никто не заказывал.
func TestServerLatency_UnknownListenerKeepsTheLabelBounded(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Get"}
	for _, bogus := range []grpcsrv.Listener{"", "хосты-1", "public-2"} {
		_, err = l.UnaryServerInterceptor(bogus)(context.Background(), nil, info,
			func(context.Context, any) (any, error) { return "ok", nil })
		require.NoError(t, err)
	}

	ms := observedValues(t, reg, "kacho_grpc_server_handling_seconds")
	require.Len(t, ms, 1, "три неизвестных полосы обязаны схлопнуться в ОДИН ряд")
	require.Equal(t, "unknown", labelOf(ms[0], "listener"))
	require.Equal(t, uint64(3), ms[0].GetHistogram().GetSampleCount())
}

// ── подписка: своя величина, своя серия ─────────────────────────────────────

// fakeStream — минимальный серверный стрим: интерсептору от него нужен только
// контекст.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f fakeStream) Context() context.Context { return f.ctx }

// TestServerLatency_StreamLifetimeIsItsOwnSeries — срок жизни подписки НЕ
// попадает в гистограмму задержки вызова.
//
// «Верхняя граница обработки» и «срок жизни подписки» — разные предметы; это
// уже записано в дескрипторе двумя разными осями. Если сложить их в одну серию,
// часовая подписка станет «вызовом длиной в час», и всякий разговор о хвосте
// задержки перестанет быть разговором о задержке.
func TestServerLatency_StreamLifetimeIsItsOwnSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	info := &grpc.StreamServerInfo{FullMethod: "/kacho.cloud.compute.v1.InternalWatchService/Watch"}
	err = l.StreamServerInterceptor(grpcsrv.ListenerInternal)(nil, fakeStream{ctx: context.Background()}, info,
		func(any, grpc.ServerStream) error { return nil })
	require.NoError(t, err)

	require.Empty(t, observedValues(t, reg, "kacho_grpc_server_handling_seconds"),
		"подписка не имеет права попадать в гистограмму задержки ВЫЗОВА")

	ms := observedValues(t, reg, "kacho_grpc_server_stream_seconds")
	require.Len(t, ms, 1)
	require.Equal(t, "kacho.cloud.compute.v1.InternalWatchService", labelOf(ms[0], "grpc_service"))
	require.Equal(t, "Watch", labelOf(ms[0], "grpc_method"))
	require.Equal(t, "internal", labelOf(ms[0], "listener"))
	require.Equal(t, "ok", labelOf(ms[0], "outcome"))
}

// TestServerLatency_StreamIsCountedByTheSameHandledCounter — подписка попадает в
// тот же счётчик обслуженных, с кодом своего исхода.
//
// Счётчик отвечает на вопрос «сколько вызовов и чем кончились»; подписка —
// такой же обслуженный вызов, и её отсутствие в счётчике означало бы, что
// оборванные подписки не видны нигде.
func TestServerLatency_StreamIsCountedByTheSameHandledCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)

	info := &grpc.StreamServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.InternalResourceLifecycleService/Subscribe"}
	err = l.StreamServerInterceptor(grpcsrv.ListenerInternal)(nil, fakeStream{ctx: context.Background()}, info,
		func(any, grpc.ServerStream) error {
			return status.Error(codes.DeadlineExceeded, "срок подписки истёк")
		})
	require.Error(t, err)

	ms := observedValues(t, reg, "kacho_grpc_server_handled_total")
	require.Len(t, ms, 1)
	require.Equal(t, "DeadlineExceeded", labelOf(ms[0], "grpc_code"))
	require.Equal(t, float64(1), ms[0].GetCounter().GetValue())

	sm := observedValues(t, reg, "kacho_grpc_server_stream_seconds")
	require.Len(t, sm, 1)
	require.Equal(t, "error", labelOf(sm[0], "outcome"),
		"оборванная подписка обязана лежать в другом ряду, чем дожившая до конца")
}

// TestServerLatency_NilMeterStreamIsATransparentPassThrough — нулевой измеритель
// не мешает стриму, как не мешает вызову.
func TestServerLatency_NilMeterStreamIsATransparentPassThrough(t *testing.T) {
	var l *grpcsrv.ServerLatency
	called := false
	err := l.StreamServerInterceptor(grpcsrv.ListenerPublic)(nil, fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/x.Y/Z"},
		func(any, grpc.ServerStream) error { called = true; return nil })
	require.NoError(t, err)
	require.True(t, called)
}

// TestServerLatency_StreamBucketsSpanASubscriptionLifetime — сетка подписки
// покрывает ЕЁ порядок величин, а не порядок одиночного вызова.
//
// Проба читает ГРАНИЦЫ сетки, а не подделывает часовое наблюдение: границы
// статичны, поэтому одного наблюдения довольно, а ждать час или заводить
// тест-только-ручку в прод-коде не приходится. Сторожится выбор: сетка задержки
// вызова кончается тридцатью секундами, и взять её сюда значило бы сложить все
// живые подписки в один ряд переполнения.
func TestServerLatency_StreamBucketsSpanASubscriptionLifetime(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := grpcsrv.NewServerLatency(reg)
	require.NoError(t, err)
	err = l.StreamServerInterceptor(grpcsrv.ListenerInternal)(nil, fakeStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/kacho.cloud.compute.v1.InternalWatchService/Watch"},
		func(any, grpc.ServerStream) error { return nil })
	require.NoError(t, err)

	ms := observedValues(t, reg, "kacho_grpc_server_stream_seconds")
	require.Len(t, ms, 1)
	var top float64
	for _, b := range ms[0].GetHistogram().GetBucket() {
		if b.GetUpperBound() > top {
			top = b.GetUpperBound()
		}
	}
	require.GreaterOrEqual(t, top, 3600.0,
		"верхняя граница сетки подписки обязана покрывать час: nlb объявляет срок подписки в час, "+
			"и сетка, кончающаяся раньше, не разрешает ничего на своём же потолке")

	callTop := 0.0
	cms := observedValues(t, reg, "kacho_grpc_server_handling_seconds")
	require.Empty(t, cms, "предпосылка пробы: подписка не попадает в серию задержки вызова")
	_ = callTop
}
