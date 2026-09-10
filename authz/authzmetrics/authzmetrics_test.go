// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authzmetrics_test

// authzmetrics_test.go — доля попаданий кеша вердиктов ВИДНА НА ПРОВОДЕ.
//
// Пробы читают экспозицию ТЕМ ЖЕ обработчиком, который монтируется на
// диагностическую поверхность: читать реестр в обход него значило бы утверждать
// не о том, что уезжает собирателю.

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/authzmetrics"
	"github.com/PRO-Robotech/corelib/operations"
	"google.golang.org/grpc"
)

func expose(t *testing.T, c prometheus.Collector) string {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("экспозиция ответила %d", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("чтение экспозиции: %v", err)
	}
	return string(body)
}

func mustContain(t *testing.T, body, line string) {
	t.Helper()
	if !strings.Contains(body, line) {
		t.Fatalf("на проводе нет строки %q\n--- экспозиция ---\n%s", line, body)
	}
}

// demoMap — карта прав пробы: один пообъектный RPC.
func demoMap() authz.RPCMap {
	return authz.RPCMap{
		"/kacho.cloud.vpc.v1.NetworkService/Get": {
			Relation: "viewer",
			Extract: authz.StaticExtractor("vpc_network", func(req any) (string, error) {
				return req.(string), nil
			}),
		},
	}
}

func callGet(t *testing.T, intr *authz.Interceptor, id string) {
	t.Helper()
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr-1", DisplayName: "usr-1"})
	_, err := intr.Unary()(ctx, id,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Get"},
		func(context.Context, any) (any, error) { return "handled", nil })
	if err != nil {
		t.Fatalf("вызов Get(%s): %v", id, err)
	}
}

// TestSeriesPresentAsZeroBeforeAnyLookup — серии стоят нулями с первой секунды.
//
// Без этого «попаданий не было» и «коллектора нет» читались бы одинаково —
// пустым местом в экспозиции.
func TestSeriesPresentAsZeroBeforeAnyLookup(t *testing.T) {
	var src authzmetrics.Source
	body := expose(t, authzmetrics.New("vpc", map[string]authzmetrics.Reader{
		authzmetrics.LaneRPC: src.Cache,
	}, src.Read))
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="hit"} 0`)
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="miss"} 0`)
	mustContain(t, body, `kacho_vpc_authz_cache_entries{lane="rpc"} 0`)
	mustContain(t, body, `kacho_vpc_authz_cache_evictions_total{lane="rpc",reason="capacity"} 0`)
	mustContain(t, body, `kacho_vpc_authz_cache_evictions_total{lane="rpc",reason="expired"} 0`)
	mustContain(t, body, `kacho_vpc_authz_cache_evictions_total{lane="rpc",reason="invalidated"} 0`)
}

// TestHitRateGrowsOnTheWire — величина РАСТЁТ, а не просто объявлена.
//
// Проба, утверждающая «метрика зарегистрирована», зеленеет на кеше, который не
// работает. Поэтому здесь три состояния подряд: пусто → промах → попадание, и
// каждое утверждается дословной строкой экспозиции.
func TestHitRateGrowsOnTheWire(t *testing.T) {
	cache := authz.NewCache(time.Minute)
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-vpc",
		Map:         demoMap(),
		Cache:       cache,
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return true, nil
		}),
	})
	var src authzmetrics.Source
	src.Install(intr.Metrics)
	coll := authzmetrics.New("vpc", map[string]authzmetrics.Reader{authzmetrics.LaneRPC: src.Cache}, src.Read)

	// Положительный контроль: ни одного вопроса — ни одного попадания.
	mustContain(t, expose(t, coll), `kacho_vpc_authz_cache_total{lane="rpc",result="hit"} 0`)

	callGet(t, intr, "enp00000000000000001")
	body := expose(t, coll)
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="miss"} 1`)
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="hit"} 0`)
	mustContain(t, body, `kacho_vpc_authz_cache_entries{lane="rpc"} 1`)

	callGet(t, intr, "enp00000000000000001")
	body = expose(t, coll)
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="hit"} 1`)
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="miss"} 1`)
}

// TestNoHitsWhenTheWindowIsClosed — положительный контроль второй стороны.
//
// Кеш, чьё окно истекает мгновенно, обязан давать промахи и НИ ОДНОГО
// попадания. Без этой пробы счётчик попаданий, возвращающий число вызовов,
// прошёл бы предыдущую.
func TestNoHitsWhenTheWindowIsClosed(t *testing.T) {
	const window = time.Minute
	cache := authz.NewCache(window)
	now := time.Now()
	// Часы двигаются вперёд на КАЖДОМ чтении времени, и шаг СТРОГО больше окна:
	// запись, поставленная одним вызовом, к следующему уже истекла. Шаг, равный
	// окну, условия не создаёт — сравнение строгое, и запись доживает до
	// следующего вопроса (проба сначала так и была написана и утверждала о
	// работающем окне, думая, что оно закрыто).
	cache.SetNowFunc(func() time.Time { now = now.Add(2 * window); return now })
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-vpc",
		Map:         demoMap(),
		Cache:       cache,
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return true, nil
		}),
	})
	var src authzmetrics.Source
	src.Install(intr.Metrics)
	coll := authzmetrics.New("vpc", map[string]authzmetrics.Reader{authzmetrics.LaneRPC: src.Cache}, src.Read)

	for i := 0; i < 3; i++ {
		callGet(t, intr, "enp00000000000000001")
	}
	body := expose(t, coll)
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="hit"} 0`)
	mustContain(t, body, `kacho_vpc_authz_cache_total{lane="rpc",result="miss"} 3`)
	mustContain(t, body, `kacho_vpc_authz_cache_evictions_total{lane="rpc",reason="expired"} 2`)
}

// TestUnreadLaneAnswersZeroInsteadOfDisappearing — полоса, чей носитель ещё не
// установлен, отвечает нулями.
//
// Носитель звена собирается ВНУТРИ носителя контура, то есть позже, чем корень
// регистрирует коллектор. Исчезновение серий на это окно сообщило бы собирателю
// не «попаданий не было», а ничего.
func TestUnreadLaneAnswersZeroInsteadOfDisappearing(t *testing.T) {
	var src authzmetrics.Source
	if s := src.Cache(); s != (authz.CacheStats{}) {
		t.Fatalf("неустановленный носитель обязан отвечать нулями, получено %+v", s)
	}
	mustContain(t, expose(t, authzmetrics.New("geo", map[string]authzmetrics.Reader{
		authzmetrics.LaneRPC: src.Cache,
	}, src.Read)), `kacho_geo_authz_cache_total{lane="rpc",result="hit"} 0`)
}

// TestTwoLanesInOneProcessAreToldApart — у процесса с двумя кешами вердиктов
// полосы различимы.
//
// registry держит ДВА кеша `pkg/authz`: один у звена решения, второй на прямом
// пути пообъектного опроса страницы. Сложить их в одну серию значило бы сделать
// невидимым тот из них, который не попадает.
func TestTwoLanesInOneProcessAreToldApart(t *testing.T) {
	rpc := authz.NewCache(time.Minute)
	list := authz.NewCache(time.Minute)
	rpc.SetAllowed("usr-1", "viewer", "registry_registry", "reg-1")
	_, _ = rpc.Get("usr-1", "viewer", "registry_registry", "reg-1")
	_, _ = list.Get("usr-1", "v_list", "registry_repository", "repo-1")

	body := expose(t, authzmetrics.New("registry", map[string]authzmetrics.Reader{
		authzmetrics.LaneRPC:  rpc.Stats,
		authzmetrics.LaneList: list.Stats,
	}, nil))
	mustContain(t, body, `kacho_registry_authz_cache_total{lane="rpc",result="hit"} 1`)
	mustContain(t, body, `kacho_registry_authz_cache_total{lane="list",result="hit"} 0`)
	mustContain(t, body, `kacho_registry_authz_cache_total{lane="list",result="miss"} 1`)
}

// TestDecisionBandsGrowOnTheWire — решения звена тоже выходят наружу, и растут.
//
// До этой провязки снимок величин звена (`authz.Interceptor.Metrics`) объявлял
// себя «счётчиками для Prometheus», не имея в прод-коде НИ ОДНОГО читателя.
// Проба утверждает наблюдаемое: сначала все шесть полос стоят нулями, затем
// разрешение сдвигает свою, а отказ — свою, и ни одна не сдвигает чужую.
func TestDecisionBandsGrowOnTheWire(t *testing.T) {
	allow := true
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-vpc",
		Map:         demoMap(),
		Cache:       authz.NewCache(time.Minute),
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return allow, nil
		}),
	})
	var src authzmetrics.Source
	src.Install(intr.Metrics)
	coll := authzmetrics.New("vpc", map[string]authzmetrics.Reader{authzmetrics.LaneRPC: src.Cache}, src.Read)

	// Положительный контроль: ни одного решения — все полосы нули, и они ЕСТЬ.
	body := expose(t, coll)
	for _, decision := range []string{"allowed", "denied", "unavailable", "breakglass", "unmapped", "rate_limited"} {
		mustContain(t, body, `kacho_vpc_authz_check_decisions_total{decision="`+decision+`"} 0`)
	}

	callGet(t, intr, "enp00000000000000001")
	body = expose(t, coll)
	mustContain(t, body, `kacho_vpc_authz_check_decisions_total{decision="allowed"} 1`)
	mustContain(t, body, `kacho_vpc_authz_check_decisions_total{decision="denied"} 0`)

	allow = false
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr-1", DisplayName: "usr-1"})
	if _, err := intr.Unary()(ctx, "enp00000000000000002",
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Get"},
		func(context.Context, any) (any, error) { return "handled", nil }); err == nil {
		t.Fatal("отказ модели прошёл как разрешение — проба меряет не тот исход")
	}
	body = expose(t, coll)
	mustContain(t, body, `kacho_vpc_authz_check_decisions_total{decision="denied"} 1`)
	mustContain(t, body, `kacho_vpc_authz_check_decisions_total{decision="allowed"} 1`)
}

// TestUninstalledDecisionsAnswerZero — решения без установленного носителя тоже
// отвечают нулями, а не пропадают.
func TestUninstalledDecisionsAnswerZero(t *testing.T) {
	mustContain(t, expose(t, authzmetrics.New("nlb",
		map[string]authzmetrics.Reader{authzmetrics.LaneRPC: nil}, nil)),
		`kacho_nlb_authz_check_decisions_total{decision="unmapped"} 0`)
}

// TestUnknownLaneIsRefusedAtWiring — словарь полос закрыт ПО ПОСТРОЕНИЮ.
//
// Молчаливый пропуск неизвестной полосы был бы «принято и проигнорировано»:
// корень объявил окно, коллектор его выбросил, и величины не выходят наружу
// ровно так же, как если бы провязки не было. Отказ случается в композиционном
// корне, то есть на старте, а не в обслуживании.
func TestUnknownLaneIsRefusedAtWiring(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("неизвестная полоса принята — метка полосы приходит из данных, " +
				"и число серий растёт с числом обслуженных арендаторов")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "tenant-42") {
			t.Fatalf("отказ не называет полосу, по нему нечего чинить: %v", r)
		}
	}()
	_ = authzmetrics.New("vpc", map[string]authzmetrics.Reader{"tenant-42": nil}, nil)
}

// TestKnownLanesAreAccepted — законный близнец того же вызова.
//
// Без него предыдущая проба была бы неотличима от конструктора, отвергающего
// всё подряд.
func TestKnownLanesAreAccepted(t *testing.T) {
	body := expose(t, authzmetrics.New("registry", map[string]authzmetrics.Reader{
		authzmetrics.LaneRPC:  nil,
		authzmetrics.LaneList: nil,
	}, nil))
	mustContain(t, body, `kacho_registry_authz_cache_total{lane="rpc",result="hit"} 0`)
	mustContain(t, body, `kacho_registry_authz_cache_total{lane="list",result="hit"} 0`)
}

// TestUndeclaredLaneIsNotDrawn — полоса, которой в процессе нет, не рисуется.
//
// Нули по необъявленной полосе утверждали бы существование окна, которого нет,
// и «второго кеша не завели» стало бы неотличимо от «второй кеш не попадает».
func TestUndeclaredLaneIsNotDrawn(t *testing.T) {
	body := expose(t, authzmetrics.New("geo", map[string]authzmetrics.Reader{
		authzmetrics.LaneRPC: nil,
	}, nil))
	if strings.Contains(body, `lane="list"`) {
		t.Fatalf("нарисована полоса, которую никто не объявлял:\n%s", body)
	}
	mustContain(t, body, `kacho_geo_authz_cache_total{lane="rpc",result="miss"} 0`)
}
