// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

// latency_test.go — слушатель, поднятый носителем, НАБЛЮДАЕТ задержку каждого
// обслуженного вызова, и наблюдает её ВНЕ всех прочих звеньев.
//
// # Почему пробы на исход, а не на присутствие звена
//
// «Звено стоит в цепочке» остаётся верным и тогда, когда оно кладёт величину не
// в тот ряд, и тогда, когда его обходит самый интересный исход. Поэтому здесь
// читаются ЗНАЧЕНИЯ из реестра — ряды, метки и число наблюдений.
//
// # Почему отдельно утверждается ОТВЕРГНУТЫЙ вызов
//
// Отказ — это тот исход, ради которого в разбор происшествия и приходят. Звено
// измерения, стоящее ВНУТРИ решения о доступе, оставило бы каждый отказ
// неизмеренным: снаружи «отвергли за микросекунду» и «отвергли за пять секунд»
// выглядели бы одинаково, и полоса перегрузки на пути отказа была бы невидима.

import (
	"context"
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// rowsOf собирает ряды семейства из реестра.
func rowsOf(t *testing.T, reg *prometheus.Registry, name string) []*dto.Metric {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("сбор величин: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()
		}
	}
	return nil
}

func labelValue(m *dto.Metric, key string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == key {
			return l.GetValue()
		}
	}
	return ""
}

// TestCarrierChainMeasuresTheCallOutermost — задержку меряет САМОЕ ВНЕШНЕЕ звено.
//
// Проба гоняет цепочку целиком, кроме слота решения (его наполняет отдельный
// шаг подъёма), и требует ровно одного наблюдения с полосой того слушателя, для
// которого цепочка собрана.
func TestCarrierChainMeasuresTheCallOutermost(t *testing.T) {
	reg := prometheus.NewRegistry()
	spec := chainSpec()
	spec.Metrics = reg

	var slot decisionSlot
	lat, err := grpcsrv.NewServerLatency(reg)
	if err != nil {
		t.Fatalf("измеритель: %v", err)
	}
	chain := unaryChain(spec, &slot, lat, nil, grpcsrv.ListenerInternal)
	chain = chain[:len(chain)-1]

	_, err = runUnaryChain(chain, context.Background(),
		"/kacho.cloud.demo.v1.WidgetService/Get", nil,
		func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("законный вызов сквозь цепочку отвергнут — проба ниже была бы вакуумна: %v", err)
	}

	rows := rowsOf(t, reg, "kacho_grpc_server_handling_seconds")
	if len(rows) != 1 {
		t.Fatalf("рядов задержки %d, ожидался 1 — носитель поднял бы слушателя, "+
			"который служит и не измеряет", len(rows))
	}
	if got := labelValue(rows[0], "listener"); got != "internal" {
		t.Fatalf("полоса слушателя = %q, ожидалась internal: цепочка обязана нести полосу того "+
			"слушателя, для которого собрана, иначе оба ряда сольются в один", got)
	}
	if got := labelValue(rows[0], "outcome"); got != "ok" {
		t.Fatalf("исход = %q, ожидался ok", got)
	}
}

// TestCarrierMeasuresACallRefusedByTheDecisionLink — отвергнутый вызов ИЗМЕРЕН.
//
// Слот решения намеренно оставлен пустым: так вызов доходит до отказа, который
// на живом процессе даёт звено прав. Если бы измерение стояло внутри решения,
// этот ряд не появился бы вовсе — а это тот самый исход, ради которого метрику
// и смотрят.
func TestCarrierMeasuresACallRefusedByTheDecisionLink(t *testing.T) {
	reg := prometheus.NewRegistry()
	spec := chainSpec()
	spec.Metrics = reg

	var slot decisionSlot
	lat, err := grpcsrv.NewServerLatency(reg)
	if err != nil {
		t.Fatalf("измеритель: %v", err)
	}
	chain := unaryChain(spec, &slot, lat, nil, grpcsrv.ListenerPublic)

	if _, err := runUnaryChain(chain, context.Background(),
		"/kacho.cloud.demo.v1.WidgetService/Get", nil,
		func(context.Context, any) (any, error) { return "ok", nil }); err == nil {
		t.Fatal("предпосылка пробы нарушена: пустой слот решения обязан отвергнуть вызов")
	}

	rows := rowsOf(t, reg, "kacho_grpc_server_handling_seconds")
	if len(rows) != 1 {
		t.Fatalf("отвергнутый вызов не измерен (рядов %d): звено измерения стоит внутри решения о "+
			"доступе, и всякий отказ остаётся без длительности — то есть невидим ровно там, где "+
			"на него смотрят", len(rows))
	}
	if got := labelValue(rows[0], "outcome"); got != "error" {
		t.Fatalf("исход отвергнутого вызова = %q, ожидался error", got)
	}
}

// TestBothCarrierListenersObserveIntoOneRegistryUnderTheirOwnLane — оба
// слушателя, поднятые носителем, пишут в ОДИН реестр, но в РАЗНЫЕ ряды.
//
// Проба идёт через настоящий сервер и настоящий вызов по сети, а не через
// сборщик цепочки: утверждается, что измеритель доехал до обоих серверов,
// собранных [serverPair], — то есть свойство подъёма, а не сборки списка.
func TestBothCarrierListenersObserveIntoOneRegistryUnderTheirOwnLane(t *testing.T) {
	reg := prometheus.NewRegistry()
	spec := chainSpec()
	spec.Metrics = reg
	spec.PublicCreds = insecure.NewCredentials()
	spec.InternalCreds = insecure.NewCredentials()

	var slot decisionSlot
	public, internal, err := serverPair(spec, &slot)
	if err != nil {
		t.Fatalf("пара слушателей: %v", err)
	}

	desc := &grpc.ServiceDesc{
		ServiceName: "kacho.cloud.demo.v1.WidgetService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Get",
			Handler: func(_ any, ctx context.Context, dec func(any) error,
				itc grpc.UnaryServerInterceptor) (any, error) {
				in := new(emptypb.Empty)
				if err := dec(in); err != nil {
					return nil, err
				}
				h := func(ctx context.Context, req any) (any, error) { return new(emptypb.Empty), nil }
				if itc == nil {
					return h(ctx, in)
				}
				return itc(ctx, in, &grpc.UnaryServerInfo{
					FullMethod: "/kacho.cloud.demo.v1.WidgetService/Get",
				}, h)
			},
		}},
	}

	for lane, srv := range map[string]*grpc.Server{"public": public, "internal": internal} {
		srv.RegisterService(desc, nil)
		lis := bufconn.Listen(1 << 20)
		go func(s *grpc.Server) { _ = s.Serve(lis) }(srv)
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatalf("%s: клиент: %v", lane, err)
		}
		// Отказ ожидаем — слот решения пуст; предмет пробы в том, что вызов
		// ИЗМЕРЕН, а не в том, что он прошёл.
		_ = conn.Invoke(context.Background(), "/kacho.cloud.demo.v1.WidgetService/Get",
			new(emptypb.Empty), new(emptypb.Empty))
		_ = conn.Close()
		srv.Stop()
	}

	rows := rowsOf(t, reg, "kacho_grpc_server_handling_seconds")
	lanes := map[string]bool{}
	for _, r := range rows {
		lanes[labelValue(r, "listener")] = true
	}
	if !lanes["public"] || !lanes["internal"] {
		t.Fatalf("полосы наблюдённых рядов = %v, ожидались обе (public, internal): "+
			"слушатель, чья полоса не появилась, служит и не измеряет", lanes)
	}
}

// probeLatency — измеритель над одноразовым реестром для проб, чей предмет НЕ
// он.
//
// Реестр у каждого вызова свой: общий сделал бы две пробы одного пакета
// зависимыми через состояние процесса, и вторая падала бы на «серия уже
// зарегистрирована» — отказ, не имеющий отношения к её предмету.
func probeLatency(t *testing.T) *grpcsrv.ServerLatency {
	t.Helper()
	l, err := grpcsrv.NewServerLatency(prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("измеритель пробы: %v", err)
	}
	return l
}
