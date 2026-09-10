// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package narrowmetrics_test

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/listnarrow/narrowmetrics"
	"github.com/PRO-Robotech/corelib/listnarrow/narrowtest"
)

// exposeOf снимает экспозицию реестра тем же обработчиком, что монтируется на
// диагностическую поверхность: читать реестр в обход него значило бы утверждать
// не о том, что уезжает на провод.
func exposeOf(t *testing.T, c prometheus.Collector) string {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	require.Equal(t, 200, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	return string(body)
}

// TestFourBandsPresentAsZeroForEveryConsumer — OBS-1-15.
//
// Ни одной операции сужения не выполнено, а все четыре полосы уже стоят нулями —
// у каждого из пяти потребителей, и имя сервиса в имени серии совпадает с
// каталогом потребителя.
func TestFourBandsPresentAsZeroForEveryConsumer(t *testing.T) {
	for _, service := range []string{"vpc", "compute", "nlb", "storage", "registry"} {
		t.Run(service, func(t *testing.T) {
			narrower := narrowtest.AllowingAll()
			body := exposeOf(t, narrowmetrics.New(service, func() listnarrow.Counts { return narrower.Counts() }))
			name := "kacho_" + service + "_list_narrow_pages_total"
			for _, outcome := range []string{"narrowed", "breakglass", "softpass_misconfigured", "softpass_transient"} {
				assert.Contains(t, body, name+`{outcome="`+outcome+`"} 0`)
			}
		})
	}
}

// TestCollectorWithoutACarrierStillDeclaresBands — законный близнец к
// предыдущей: сужателя на посадке нет, а полосы объявлены.
//
// Исчезновение серий сообщило бы собирателю не «сужений не было», а ничего.
func TestCollectorWithoutACarrierStillDeclaresBands(t *testing.T) {
	body := exposeOf(t, narrowmetrics.New("vpc", nil))
	assert.Contains(t, body, `kacho_vpc_list_narrow_pages_total{outcome="narrowed"} 0`)
	assert.Contains(t, body, `kacho_vpc_list_narrow_pages_total{outcome="breakglass"} 0`)
}

// TestNormalNarrowingGrowsThePositiveBand — OBS-1-16.
func TestNormalNarrowingGrowsThePositiveBand(t *testing.T) {
	peer := &narrowtest.Peer{Allow: map[string]bool{"net-1": true}}
	narrower := narrowtest.New(peer)

	out, err := listnarrow.IDs(narrowtest.Caller(), narrower, "vpc_network", "get",
		[]string{"net-1", "net-2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"net-1"}, out)

	c := narrower.Counts()
	assert.Equal(t, uint64(1), c.Narrowed)
	assert.Equal(t, uint64(0), c.Breakglass)
	assert.Equal(t, uint64(0), c.SoftPassMisconfigured)
	assert.Equal(t, uint64(0), c.SoftPassTransient)
}

// TestBreakglassIsCountedAndIsNotCountedAsNormal — OBS-1-18.
//
// Пара к предыдущей в обе стороны: без неё «страница ушла несуженной» было бы
// неотличимо от «страница сужена».
func TestBreakglassIsCountedAndIsNotCountedAsNormal(t *testing.T) {
	narrower := narrowtest.Breakglass()

	out, err := listnarrow.IDs(narrowtest.Caller(), narrower, "vpc_network", "get",
		[]string{"net-1", "net-2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"net-1", "net-2"}, out, "аварийный режим отдаёт страницу целиком")

	c := narrower.Counts()
	assert.Equal(t, uint64(1), c.Breakglass)
	assert.Equal(t, uint64(0), c.Narrowed, "несуженный проход не является штатным сужением")
}

// TestSoftPassMisconfiguredIsItsOwnBand — OBS-1-19.
//
// Ответ соседа ДОКАЗЫВАЕТ неверную настройку (метода нет), то есть сам такой
// отказ не пройдёт никогда — и полоса у него своя.
func TestSoftPassMisconfiguredIsItsOwnBand(t *testing.T) {
	peer := &narrowtest.Peer{Err: status.Error(codes.Unimplemented, "no such method")}
	narrower := listnarrow.New(peer, listnarrow.Config{
		Relations:             map[string][]string{"": {"v_get"}},
		SoftPassOnPeerFailure: true,
	})

	out, err := listnarrow.IDs(narrowtest.Caller(), narrower, "vpc_network", "get", []string{"net-1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"net-1"}, out, "мягкий проход отдаёт страницу несуженной")

	c := narrower.Counts()
	assert.Equal(t, uint64(1), c.SoftPassMisconfigured)
	assert.Equal(t, uint64(0), c.SoftPassTransient)
	assert.Equal(t, uint64(0), c.Narrowed)
}

// TestSoftPassTransientIsItsOwnBand — OBS-1-20.
func TestSoftPassTransientIsItsOwnBand(t *testing.T) {
	peer := &narrowtest.Peer{Err: status.Error(codes.Unavailable, "connection refused")}
	narrower := listnarrow.New(peer, listnarrow.Config{
		Relations:             map[string][]string{"": {"v_get"}},
		SoftPassOnPeerFailure: true,
	})

	_, err := listnarrow.IDs(narrowtest.Caller(), narrower, "vpc_network", "get", []string{"net-1"})
	require.NoError(t, err)

	c := narrower.Counts()
	assert.Equal(t, uint64(1), c.SoftPassTransient)
	assert.Equal(t, uint64(0), c.SoftPassMisconfigured)
	assert.Equal(t, uint64(0), c.Narrowed)
}

// TestUnnamedCallerGrowsNothing — OBS-1-21 (половина «в процессе»).
//
// Отказ ДО сужения — не сужение. Без этой пробы положительная полоса считала бы
// не то: «сужателя не звали» слилось бы с «сужатель отработал».
func TestUnnamedCallerGrowsNothing(t *testing.T) {
	narrower := narrowtest.AllowingAll()

	_, err := listnarrow.IDs(context.Background(), narrower, "vpc_network", "get", []string{"net-1"})
	require.Error(t, err, "безымянный вызывающий отвергается")

	c := narrower.Counts()
	assert.Equal(t, listnarrow.Counts{}, c, "ни одна из четырёх полос не изменилась")
}

// TestRefusalWithoutAModelGrowsNothing — соседний отказ той же полосы: модели на
// посадке нет вовсе.
func TestRefusalWithoutAModelGrowsNothing(t *testing.T) {
	narrower := narrowtest.Unwired()

	_, err := listnarrow.IDs(narrowtest.Caller(), narrower, "vpc_network", "get", []string{"net-1"})
	require.Error(t, err)

	assert.Equal(t, listnarrow.Counts{}, narrower.Counts())
}

// TestHardFailureGrowsNothing — отказ соседа БЕЗ мягкого прохода: страница не
// отдана, значит и сужения не было.
func TestHardFailureGrowsNothing(t *testing.T) {
	peer := &narrowtest.Peer{Err: errors.New("boom")}
	narrower := narrowtest.New(peer)

	_, err := listnarrow.IDs(narrowtest.Caller(), narrower, "vpc_network", "get", []string{"net-1"})
	require.Error(t, err)

	assert.Equal(t, listnarrow.Counts{}, narrower.Counts())
}

// TestBandsReachTheSurface — величины доходят до экспозиции, а не только до
// структуры.
func TestBandsReachTheSurface(t *testing.T) {
	peer := &narrowtest.Peer{AllowAll: true}
	narrower := narrowtest.New(peer)
	_, err := listnarrow.IDs(narrowtest.Caller(), narrower, "vpc_network", "get", []string{"net-1"})
	require.NoError(t, err)

	body := exposeOf(t, narrowmetrics.New("vpc", func() listnarrow.Counts { return narrower.Counts() }))
	assert.Contains(t, body, `kacho_vpc_list_narrow_pages_total{outcome="narrowed"} 1`)
	assert.Contains(t, body, `kacho_vpc_list_narrow_pages_total{outcome="breakglass"} 0`)
}
