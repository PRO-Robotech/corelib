// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// links_test.go — доказательство того, что пять измерений контура ПРОВЯЗАНЫ, а
// не объявлены.
//
// Каждое доказывается ПАРОЙ: инъекция (провязку убрали — проба краснеет и
// называет предмет) и законный близнец той же формы (проба молчит). Без второй
// половины проба ловит форму, а не существо, и первый же ложный срабат её
// отключит.
//
// Пробы гоняют ТЕ ЖЕ функции, что и носитель (`unaryChain`, `decisionLink`,
// звенья из `links.go`). Проба, повторяющая логику звена своей копией,
// доказывала бы свойство копии.
package servicehost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/proxytuple"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// runUnaryChain сворачивает цепочку звеньев так же, как это делает grpc-go:
// первое звено — самое внешнее.
func runUnaryChain(chain []grpc.UnaryServerInterceptor, ctx context.Context, method string,
	req any, h grpc.UnaryHandler) (any, error) {
	info := &grpc.UnaryServerInfo{FullMethod: method}
	next := h
	for i := len(chain) - 1; i >= 0; i-- {
		link, downstream := chain[i], next
		next = func(c context.Context, r any) (any, error) {
			return link(c, r, info, downstream)
		}
	}
	return next(ctx, req)
}

// captureLogger — журнал, чей вывод можно прочитать.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})), &buf
}

// ── измерение 1: журнал доступа ─────────────────────────────────────────────

const panicMethod = "/kacho.cloud.demo.v1.WidgetService/Get"

// accessLogMarker — метка записи журнала доступа. Отличает её от записи
// восстановления паники, которая печатает то же имя метода по другому поводу.
const accessLogMarker = "grpc unary"

func panicking(context.Context, any) (any, error) {
	panic("разыменование nil на пути запроса")
}

// TestAccessLogRecordsThePanickingCall — законный близнец: контур, собранный
// носителем, оставляет строку журнала о ПАНИКОВАВШЕМ вызове.
//
// Это самый тяжёлый исход и единственный, который прежняя раскладка звеньев
// теряла: раскрутка стека проходит мимо записи, стоящей внутри восстановления.
func TestAccessLogRecordsThePanickingCall(t *testing.T) {
	log, buf := captureLogger()
	spec := chainSpec()
	spec.Logger = log

	var slot decisionSlot
	chain := unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic)
	// Слот решения о доступе на этой пробе не нужен: паника случается ниже него.
	// Убираем последнее звено, чтобы вызов дошёл до обработчика.
	chain = chain[:len(chain)-1]

	_, err := runUnaryChain(chain, context.Background(), panicMethod, nil, panicking)
	if status.Code(err) != codes.Internal {
		t.Fatalf("паника не превратилась в codes.Internal (%v) — проба меряет не тот исход", err)
	}
	line := buf.String()
	if !strings.Contains(line, accessLogMarker) || !strings.Contains(line, panicMethod) {
		t.Fatalf("паниковавший вызов не оставил строки журнала — самый тяжёлый исход "+
			"остаётся единственным ненаблюдаемым:\n%s", line)
	}
	if !strings.Contains(line, "code=Internal") {
		t.Fatalf("строка журнала не называет исхода вызова:\n%s", line)
	}
	t.Logf("законный близнец: строка есть — %s", strings.TrimSpace(line))
}

// TestAccessLogInsideRecoveryLosesThePanickingCall — ИНЪЕКЦИЯ: та же цепочка с
// журналом ВНУТРИ восстановления паники. Записи об исходе вызова не остаётся.
//
// Проба доказывает, что порядок в `unaryChain` держит наблюдаемое свойство, а не
// оформление: перестановка двух звеньев — единственное отличие от близнеца выше.
//
// # Почему предикат ищет МЕТКУ журнала, а не имя метода
//
// Первая редакция искала имя метода и краснела на исправной инъекции: имя метода
// печатает и восстановление паники — своей, отдельной записью о том, ЧТО
// случилось. Запись журнала доступа отвечает на другой вопрос — чем КОНЧИЛСЯ
// вызов, — и обязана быть у КАЖДОГО вызова, а не только у паниковавшего.
// Считать одну за другую значит объявить измерение исполненным по чужой строке.
func TestAccessLogInsideRecoveryLosesThePanickingCall(t *testing.T) {
	log, buf := captureLogger()
	injected := []grpc.UnaryServerInterceptor{
		grpcsrv.UnaryPanicRecovery(log),
		accessLogUnary(log), // ← провязка сдвинута внутрь: это и есть дефект
	}
	_, err := runUnaryChain(injected, context.Background(), panicMethod, nil, panicking)
	if status.Code(err) != codes.Internal {
		t.Fatalf("паника не превратилась в codes.Internal (%v) — инъекция построена не про тот предмет", err)
	}
	if strings.Contains(buf.String(), accessLogMarker) {
		t.Fatalf("журнал ВНУТРИ восстановления всё-таки записал исход вызова — "+
			"значит порядок звеньев ничего не решает и проба-близнец вакуумна:\n%s", buf.String())
	}
	// Контроль объёма: восстановление СВОЮ запись оставило, то есть журнал вообще
	// пишет и «пусто» здесь означает «нет записи об исходе», а не «нет вывода».
	if !strings.Contains(buf.String(), panicMethod) {
		t.Fatalf("в журнале нет вообще ничего — проба не отличила бы потерянную запись "+
			"от неработающего вывода:\n%s", buf.String())
	}
	t.Log("инъекция: журнал внутри восстановления запись об исходе теряет — порядок несущий")
}

// TestAccessLogRecordsAnOrdinaryCallToo — контроль объёма: журнал пишет не только
// про панику. Без него «строк нет» и «строк нет ПРО ЭТОТ исход» неразличимы.
func TestAccessLogRecordsAnOrdinaryCallToo(t *testing.T) {
	log, buf := captureLogger()
	_, err := runUnaryChain([]grpc.UnaryServerInterceptor{accessLogUnary(log)},
		context.Background(), panicMethod, nil,
		func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("обычный вызов отвергнут: %v", err)
	}
	if !strings.Contains(buf.String(), "code=OK") {
		t.Fatalf("обычный вызов не попал в журнал:\n%s", buf.String())
	}
}

// ── измерение 2: бюджет отказов ─────────────────────────────────────────────

// budgetSpec — дескриптор для проверки того, что величина ДОЕЗЖАЕТ до механизма.
// Решение принимает сам сервис, поэтому ребра и сети здесь нет.
func budgetSpec(budget servicecontract.Axis[float64], deny authz.CheckClient) servicecontract.Spec {
	s := chainSpec()
	s.Authz = servicecontract.AuthzSelf
	s.SelfCheck = deny
	s.DenyBudget = budget
	return s
}

// denyStub — решатель, отвечающий отказом. Именованный тип, а не функция:
// значения функционального типа несравнимы, и проба провязки не смогла бы
// утверждать «вернули ТОГО ЖЕ решателя, а не обёртку вокруг него».
type denyStub struct{}

func (denyStub) Check(context.Context, string, string, string) (bool, error) { return false, nil }

func denyingClient() authz.CheckClient { return denyStub{} }

// budgetProbeMap — карта с одним методом, чей объект берётся прямо из запроса.
func budgetProbeMap() authz.RPCMap {
	return authz.RPCMap{
		"/kacho.cloud.demo.v1.WidgetService/Get": {
			Relation: "v_get",
			Extract: authz.StaticExtractor("demo_widget", func(req any) (string, error) {
				id, _ := req.(string)
				return id, nil
			}),
		},
	}
}

func principalCtx() context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr_alice", DisplayName: "usr_alice"})
}

// stormOutcome прогоняет шторм отказов через собранное носителем звено и
// возвращает, сколько вызовов отсеклось бюджетом.
func stormOutcome(t *testing.T, spec servicecontract.Spec) uint64 {
	t.Helper()
	intr, closeEdge, err := decisionLink(spec, budgetProbeMap())
	if err != nil {
		t.Fatalf("звено решения о доступе не собралось: %v", err)
	}
	if closeEdge != nil {
		defer closeEdge()
	}
	ctx := principalCtx()
	for i := 0; i < 40; i++ {
		_, _ = intr.Unary()(ctx, fmt.Sprintf("wid_%02d", i),
			&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.demo.v1.WidgetService/Get"},
			func(context.Context, any) (any, error) { return "handled", nil })
	}
	return intr.Metrics().RateLimited
}

// TestDenyBudgetReachesTheDecisionLink — законный близнец: объявленная величина
// доезжает до механизма, и шторм отказов отсекается.
func TestDenyBudgetReachesTheDecisionLink(t *testing.T) {
	got := stormOutcome(t, budgetSpec(servicecontract.Value(2.0), denyingClient()))
	if got == 0 {
		t.Fatal("объявленный бюджет отказов не отсёк ни одного вызова — величина до механизма " +
			"не доехала, ветка ResourceExhausted недостижима, а её счётчик навсегда нулевой")
	}
	t.Logf("законный близнец: бюджет 2/с отсёк %d вызовов из 40", got)
}

// TestDenyBudgetNotApplicableLeavesTheCutOffOff — ИНЪЕКЦИЯ провязки: ось
// объявлена неприменимой, отсечки нет, счётчик нулевой.
//
// Это ровно то состояние, в котором дескриптор оказался бы, если бы поля не
// существовало вовсе, — и именно поэтому оно обязано быть ДОСТИЖИМО ТОЛЬКО через
// названную причину: молчаливым его отличить не от чего.
func TestDenyBudgetNotApplicableLeavesTheCutOffOff(t *testing.T) {
	na := servicecontract.NotApplicable[float64]("решатель в том же процессе, ронять шторму некого")
	if got := stormOutcome(t, budgetSpec(na, denyingClient())); got != 0 {
		t.Fatalf("отсечка сработала при объявленной неприменимости (%d) — значит величина берётся "+
			"не из дескриптора", got)
	}
	t.Log("инъекция: без величины отсечки нет — то есть отсекает именно она")
}

// TestDenyBudgetMustBeDeclaredAndPositive — отказ старта: ось не объявлена либо
// объявлена нулём. Ноль механизм читает как «ограничения нет», поэтому
// молчаливого нуля здесь быть не может.
func TestDenyBudgetMustBeDeclaredAndPositive(t *testing.T) {
	for _, tc := range []struct {
		name string
		axis servicecontract.Axis[float64]
	}{
		{"не объявлена", servicecontract.Axis[float64]{}},
		{"объявлена нулём", servicecontract.Value(0.0)},
		{"объявлена отрицательной", servicecontract.Value(-1.0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := acceptableSpec()
			s.DenyBudget = tc.axis
			mustRefuse(t, s, "DenyBudget")
		})
	}
}

// ── измерение 3: верхняя граница обработки ──────────────────────────────────

// deadlineOfCall прогоняет вызов через цепочку и возвращает срок, который увидел
// обработчик.
func deadlineOfCall(t *testing.T, chain []grpc.UnaryServerInterceptor, ctx context.Context) (time.Time, bool) {
	t.Helper()
	var (
		dl  time.Time
		has bool
	)
	_, err := runUnaryChain(chain, ctx, panicMethod, nil,
		func(c context.Context, _ any) (any, error) {
			dl, has = c.Deadline()
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("вызов отвергнут: %v", err)
	}
	return dl, has
}

// TestHandlingBudgetReachesTheHandler — законный близнец: обработчик видит срок,
// которого во входящем контексте не было.
func TestHandlingBudgetReachesTheHandler(t *testing.T) {
	spec := chainSpec()
	spec.HandlingBudget = 5 * time.Second
	var slot decisionSlot
	chain := unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic)
	chain = chain[:len(chain)-1] // без слота решения: предмет пробы — срок

	dl, has := deadlineOfCall(t, chain, context.Background())
	if !has {
		t.Fatal("обработчик получил контекст БЕЗ срока — граница обработки до него не доехала, " +
			"и вызов вправе держать соединение из ограниченного пула сколько угодно")
	}
	if left := time.Until(dl); left <= 0 || left > 5*time.Second {
		t.Fatalf("срок вне объявленной границы: осталось %v при границе 5s", left)
	}
	t.Logf("законный близнец: обработчик увидел срок, до него %v", time.Until(dl))
}

// TestChainWithoutTheBudgetLinkLeavesTheCallUnbounded — ИНЪЕКЦИЯ: та же цепочка
// без звена границы. Обработчик получает контекст без срока.
func TestChainWithoutTheBudgetLinkLeavesTheCallUnbounded(t *testing.T) {
	log, _ := captureLogger()
	injected := []grpc.UnaryServerInterceptor{
		accessLogUnary(log),
		grpcsrv.UnaryPanicRecovery(log),
		// звено границы снято — это и есть дефект
	}
	if _, has := deadlineOfCall(t, injected, context.Background()); has {
		t.Fatal("срок появился без звена границы — значит его ставит кто-то другой, " +
			"и близнец выше ничего не доказывает")
	}
	t.Log("инъекция: без звена срока нет — то есть его ставит именно оно")
}

// TestHandlingBudgetNeverWidensTheCallersDeadline — вторая половина существа:
// более строгий срок вызывающего уважается. Иначе клиент, попросивший 50 мс,
// получал бы наш бюджет и держал бы ресурс дольше, чем сам согласился ждать.
func TestHandlingBudgetNeverWidensTheCallersDeadline(t *testing.T) {
	spec := chainSpec()
	spec.HandlingBudget = time.Hour
	var slot decisionSlot
	chain := unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic)
	chain = chain[:len(chain)-1]

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dl, has := deadlineOfCall(t, chain, ctx)
	if !has {
		t.Fatal("срок вызывающего потерян")
	}
	if time.Until(dl) > time.Second {
		t.Fatalf("окно расширено до нашего бюджета: осталось %v при просьбе клиента в 50мс", time.Until(dl))
	}
}

// TestHandlingBudgetMustBeDeclared — отказ старта: величины нет.
func TestHandlingBudgetMustBeDeclared(t *testing.T) {
	for _, v := range []time.Duration{0, -time.Second} {
		s := acceptableSpec()
		s.HandlingBudget = v
		mustRefuse(t, s, "HandlingBudget")
	}
}

// ── измерение 4: загрузочный гейт мутаций ───────────────────────────────────

// refusingGate — путь доставки намерений не поднят.
type refusingGate struct{}

func (refusingGate) GuardMutation() error {
	return status.Error(codes.Unavailable, "IAM-register delivery path is not connected")
}

// openGate — путь доставки поднят.
type openGate struct{}

func (openGate) GuardMutation() error { return nil }

func gateSpec(gate servicecontract.Axis[servicecontract.BootGate]) servicecontract.Spec {
	s := chainSpec()
	s.BootGate = gate
	return s
}

// callThroughChain прогоняет метод через цепочку без слота решения и сообщает,
// дошёл ли вызов до обработчика.
func callThroughChain(t *testing.T, spec servicecontract.Spec, method string) (reached bool, err error) {
	t.Helper()
	var slot decisionSlot
	chain := unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic)
	chain = chain[:len(chain)-1]
	_, err = runUnaryChain(chain, context.Background(), method, nil,
		func(context.Context, any) (any, error) { reached = true; return "ok", nil })
	return reached, err
}

// TestBootGateRefusesCreateWhileTheDeliveryPathIsDown — законный близнец
// провязки: гейт принесён, создание отвергается, обработчик не вызывается.
func TestBootGateRefusesCreateWhileTheDeliveryPathIsDown(t *testing.T) {
	spec := gateSpec(servicecontract.Value[servicecontract.BootGate](refusingGate{}))
	reached, err := callThroughChain(t, spec, "/kacho.cloud.demo.v1.WidgetService/Create")
	if err == nil {
		t.Fatal("создание принято при неподнятом пути доставки — ресурс завёлся бы без владельца")
	}
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("отказ пришёл кодом %v, а обязан — Unavailable (зависимость временно недоступна)", status.Code(err))
	}
	if reached {
		t.Fatal("обработчик вызван вопреки отказу гейта — ресурс всё-таки создан")
	}
}

// TestBootGateIsSilentOnEverythingButTenantCreate — ЗАКОННЫЕ БЛИЗНЕЦЫ той же
// формы: чтение и админ-создание через тот же гейт проходят.
//
// Без этой половины гейт ловил бы форму: «отвергает всё» неотличимо от
// «отвергает создание», а разница — весь админ-путь и всё чтение.
func TestBootGateIsSilentOnEverythingButTenantCreate(t *testing.T) {
	spec := gateSpec(servicecontract.Value[servicecontract.BootGate](refusingGate{}))
	for _, method := range []string{
		"/kacho.cloud.demo.v1.WidgetService/Get",
		"/kacho.cloud.demo.v1.WidgetService/List",
		"/kacho.cloud.demo.v1.WidgetService/Update",
		"/kacho.cloud.demo.v1.WidgetService/Delete",
		"/kacho.cloud.demo.v1.InternalWidgetService/Create",
	} {
		reached, err := callThroughChain(t, spec, method)
		if err != nil || !reached {
			t.Fatalf("%s отвергнут загрузочным гейтом (%v): гейт ловит форму вызова, а не его предмет", method, err)
		}
	}
}

// TestChainWithoutTheGateAcceptsCreateWhileTheDeliveryPathIsDown — ИНЪЕКЦИЯ:
// та же посадка без провязанного гейта. Создание принимается.
//
// Это состояние дерева ДО правки: поле дескриптора существовало, читателя у него
// не было, и в окне неподнятого дренажа ресурсы создавались без владельца.
func TestChainWithoutTheGateAcceptsCreateWhileTheDeliveryPathIsDown(t *testing.T) {
	spec := gateSpec(servicecontract.NotApplicable[servicecontract.BootGate](
		"очереди регистраций у демо нет"))
	reached, err := callThroughChain(t, spec, "/kacho.cloud.demo.v1.WidgetService/Create")
	if err != nil || !reached {
		t.Fatalf("создание отвергнуто без провязанного гейта (%v) — значит отвергает не он", err)
	}
	t.Log("инъекция: без провязки создание проходит — то есть отвергает именно гейт")
}

// TestBootGateMustBeDeclared — отказ старта: ось не объявлена вовсе либо
// объявлена пустым значением.
func TestBootGateMustBeDeclared(t *testing.T) {
	s := acceptableSpec()
	s.BootGate = servicecontract.Axis[servicecontract.BootGate]{}
	mustRefuse(t, s, "BootGate")

	s = acceptableSpec()
	s.BootGate = servicecontract.Value[servicecontract.BootGate](nil)
	mustRefuse(t, s, "BootGate")
}

// ── измерение 5: порт существования на пути отказа ──────────────────────────

type probeFunc func(ctx context.Context, objectType, objectID string) (bool, error)

func (f probeFunc) ObjectExists(ctx context.Context, ot, id string) (bool, error) {
	return f(ctx, ot, id)
}

// ProbeableTypes — охват подделки. `probeFunc` отвечает на ЛЮБОЙ тип, поэтому
// перечень у неё открытый: `nil` означает «охват не объявлен».
//
// Это законно только для проб пути отказа, где предмет — исход сверки, а не её
// охват. Пробы охвата (О5в) подставляют `typedProbe`, у которой перечень свой.
func (f probeFunc) ProbeableTypes() []string { return nil }

// typedProbe — подделка, объявляющая ОХВАТ. Нужна там, где предмет — сравнение
// объявленного охвата с пообъектными типами карты прав.
type typedProbe struct {
	types []string
}

func (t typedProbe) ObjectExists(context.Context, string, string) (bool, error) { return true, nil }
func (t typedProbe) ProbeableTypes() []string                                   { return t.types }

const hiddenProbeType = "vpc_network"

// hidingChecker собирает решателя РУКАМИ — и это надо знать, читая пробы ниже.
//
// Прежняя редакция этой шапки говорила «ровно так, как это делает носитель», и
// утверждение было неверным дважды: набор пообъектных типов носитель ВЫВОДИТ из
// карты прав, а здесь он выписан; и обёртку носитель НАДЕВАЕТ сам, а здесь её
// надевает проба. Формулировка обошлась дорого — под ней снятая в носителе
// провязка оставляла весь прогон зелёным, и обнаружилось это только чужой
// инъекцией.
//
// Что утверждают пробы на этом решателе: ОТВЕТ звена (различает ли оно «нет
// объекта» и «есть, но не твой», и каким текстом). Что оно провязано носителем —
// предмет `wiring_test.go`, где вход берётся у `decisionLink`.
func hidingChecker(probe servicecontract.ExistenceProbe) authz.CheckClient {
	return &existenceAwareCheck{
		inner:  denyingClient(),
		probe:  probe,
		scoped: map[string]struct{}{hiddenProbeType: {}},
	}
}

// refusalSeenByCaller прогоняет отказ через звено решения о доступе и возвращает
// то, что увидел вызывающий.
//
// Метод здесь — МУТАЦИЯ, и это не деталь оформления, а условие осмысленности
// пробы. Пообъектное чтение (`/Get` на глагольном `v_get`) звено скрывает и БЕЗ
// всякого порта — по одной лишь ФОРМЕ вызова. Строй пробу на нём — и она
// зеленела бы с портом и без него, то есть не утверждала бы о порте ничего.
// Мутация формой не скрывается: без порта её отказ приходит `PermissionDenied`,
// и по коду ответа видно, что объект существует.
//
// Первая редакция этой пробы была построена именно на `/Get` и покраснела в
// направлении «без порта отказ обязан быть отличим» — то есть опровергла
// собственную гипотезу. Запись оставлена здесь, чтобы следующий читатель не
// повторил её.
func refusalSeenByCaller(t *testing.T, client authz.CheckClient, objectID string) error {
	t.Helper()
	const mutating = probedMethod
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-demo",
		Cache:       authz.NewCache(0),
		Logger:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Client:      client,
		Map: authz.RPCMap{
			mutating: {
				Relation: "v_update",
				Extract: authz.StaticExtractor(hiddenProbeType, func(req any) (string, error) {
					id, _ := req.(string)
					return id, nil
				}),
			},
		},
	})
	_, err := intr.Unary()(principalCtx(), objectID,
		&grpc.UnaryServerInfo{FullMethod: mutating},
		func(context.Context, any) (any, error) { return "handled", nil })
	return err
}

// TestExistingButForeignObjectAnswersInTheOwnersVoice — законный близнец:
// объект ЕСТЬ и он не наш → вызывающий получает промах владельца ДОСЛОВНО.
func TestExistingButForeignObjectAnswersInTheOwnersVoice(t *testing.T) {
	const id = "netABCDEFGHJKMNPQR"
	form, ok := authz.OwnerNotFoundFormat(hiddenProbeType)
	if !ok {
		t.Fatalf("у типа %q нет текста владельца — пробу не на чем строить", hiddenProbeType)
	}
	err := refusalSeenByCaller(t, hidingChecker(
		probeFunc(func(context.Context, string, string) (bool, error) { return true, nil })), id)

	if status.Code(err) != codes.NotFound {
		t.Fatalf("отказ пришёл кодом %v: по одному лишь коду видно, что объект существует",
			status.Code(err))
	}
	want := fmt.Sprintf(form, id)
	if got := status.Convert(err).Message(); got != want {
		t.Fatalf("текст отказа %q отличим от настоящего промаха владельца %q — то есть оракул", got, want)
	}
	t.Logf("законный близнец: отказ дословно равен промаху владельца — %q", want)
}

// TestAbsentObjectIsLetThroughToTheHandler — вторая половина существа: объекта
// НЕТ → вызов пропускается к обработчику, и промах отдаёт он сам, из своей БД.
//
// Без неё сверка ловила бы форму: «всегда отвечаем NotFound» неотличимо от
// «различаем два случая», а разница — в том, кто отвечает и по каким данным.
func TestAbsentObjectIsLetThroughToTheHandler(t *testing.T) {
	err := refusalSeenByCaller(t, hidingChecker(
		probeFunc(func(context.Context, string, string) (bool, error) { return false, nil })),
		"netABCDEFGHJKMNPQR")
	if err != nil {
		t.Fatalf("вызов по отсутствующему объекту отвергнут звеном (%v) вместо пропуска к обработчику: "+
			"дословный промах владельца сказать было бы некому", err)
	}
}

// TestRefusalWithoutTheProbeIsDistinguishable — ИНЪЕКЦИЯ: тот же отказ БЕЗ
// провязанного порта. Вызывающий видит `PermissionDenied`, то есть отличает
// «есть, но не твой» от «нет такого» по одному лишь коду.
func TestRefusalWithoutTheProbeIsDistinguishable(t *testing.T) {
	err := refusalSeenByCaller(t, denyingClient(), "netABCDEFGHJKMNPQR")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("без порта отказ пришёл кодом %v — значит форму ответа определяет не порт, "+
			"и близнецы выше ничего не доказывают", status.Code(err))
	}
	t.Log("инъекция: без порта отказ отличим — то есть неотличимым его делает именно он")
}

// TestProbeFailureLeavesTheRefusalARefusal — недоступность собственной БД не есть
// основание пропустить вызов: отказ остаётся отказом (fail-closed).
func TestProbeFailureLeavesTheRefusalARefusal(t *testing.T) {
	err := refusalSeenByCaller(t, hidingChecker(
		probeFunc(func(context.Context, string, string) (bool, error) {
			return false, errors.New("база не отвечает")
		})), "netABCDEFGHJKMNPQR")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("сбой пробы дал %v вместо отказа: недоступность своей БД стала пропуском", status.Code(err))
	}
}

// TestHierarchyAnchorsAreNotProbed — тип-якорь (проект, аккаунт, кластер)
// существование не скрывает, и порт по нему не спрашивается: вызывающий назвал
// коллекцию, а её существование секретом не является.
func TestHierarchyAnchorsAreNotProbed(t *testing.T) {
	asked := 0
	c := &existenceAwareCheck{
		inner:  denyingClient(),
		probe:  probeFunc(func(context.Context, string, string) (bool, error) { asked++; return true, nil }),
		scoped: map[string]struct{}{hiddenProbeType: {}},
	}
	allowed, err := c.Check(context.Background(), "user:usr_alice", "v_get", "project:prj_1")
	if allowed || err != nil {
		t.Fatalf("отказ по якорю изменил форму: allowed=%v err=%v", allowed, err)
	}
	if asked != 0 {
		t.Fatalf("порт спрошен по типу-якорю %d раз(а)", asked)
	}
}

// TestTransportFailureIsNeverHidden — ошибка транспорта до владельца модели
// уходит как есть. Скрыть перебой под промахом значило бы сказать вызывающему
// «такого объекта нет», когда мы просто не смогли спросить.
func TestTransportFailureIsNeverHidden(t *testing.T) {
	boom := errors.New("владелец модели недоступен")
	c := &existenceAwareCheck{
		inner: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return false, boom
		}),
		probe:  probeFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
		scoped: map[string]struct{}{hiddenProbeType: {}},
	}
	_, err := c.Check(context.Background(), "user:usr_alice", "v_get", hiddenProbeType+":net1")
	if !errors.Is(err, boom) {
		t.Fatalf("сбой транспорта подменён на %v — перебой отвечает голосом промаха", err)
	}
}

// TestProbeIsWiredOnlyWhenItIsBrought — ФОРМА, которую отдаёт обёртка, а НЕ
// провязка её носителем.
//
// Заголовок прежней редакции обещал «провязку в носителе», а тело зовёт
// `withExistenceHiding` НАПРЯМУЮ — то есть закрепляет ответ обёртки на вопрос,
// который задаёт сама проба. Снять вызов обёртки в `decisionLink` эта проба не
// мешает никак, что и показала чужая инъекция.
//
// Проба остаётся: «принесён → обёрнут, не принесён → не обёрнут» — верное и
// нужное свойство обёртки. Но провязку утверждает `wiring_test.go`, где вход
// берётся у `decisionLink`, а исход читается с ответа вызывающему.
func TestProbeIsWiredOnlyWhenItIsBrought(t *testing.T) {
	spec := chainSpec()
	inner := denyingClient()

	if got := withExistenceHiding(spec, budgetProbeMap(), inner); got != inner {
		t.Fatalf("решатель обёрнут при непринесённом порте (%T) — обёртка спрашивала бы порт, которого нет", got)
	}
	spec.Existence = probeFunc(func(context.Context, string, string) (bool, error) { return false, nil })
	if _, ok := withExistenceHiding(spec, budgetProbeMap(), inner).(*existenceAwareCheck); !ok {
		t.Fatal("порт принесён, а решатель не обёрнут — проводка, которую никто не спросит")
	}
}

// ── общее: законный дескриптор для отказов старта ───────────────────────────

// acceptableSpec — дескриптор, который конструктор ПРИНИМАЕТ. От него
// отталкивается каждая инъекция отказа старта в этом файле; без положительного
// контроля любое «отказано» зеленело бы по чужой причине.
func acceptableSpec() servicecontract.Spec {
	return servicecontract.Spec{
		Service:         "kacho-demo",
		Mode:            servicecontract.ModeDev,
		Forwarders:      servicecontract.Value(grpcsrv.NewTrustedForwarders("spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway")),
		TrustDomain:     servicecontract.Value(grpcsrv.NewTrustDomain("kacho.cloud")),
		TrustDomainKnob: "KACHO_DEMO_AUTHZ_TRUST_DOMAIN",
		ForwarderKnobs: servicecontract.ForwarderKnobs{
			SANs:     "KACHO_DEMO_AUTHZ_TRUSTED_FORWARDER_SANS",
			TrustAny: "KACHO_DEMO_AUTHZ_TRUST_ANY_FORWARDER",
		},
		Authz:          servicecontract.AuthzSelf,
		SelfCheck:      denyingClient(),
		HandlingBudget: 30 * time.Second,
		Metrics:        prometheus.NewRegistry(),
		DBSSLMode:      servicecontract.Value("disable"),
		PublicAddr:     ":9090",
		InternalAddr:   ":9091",
		PublicCreds:    insecure.NewCredentials(),
		InternalCreds:  insecure.NewCredentials(),

		Emits:         servicecontract.NotApplicable[[]proxytuple.Relation]("демо ничего не эмитит"),
		Registers:     servicecontract.NotApplicable[[]servicecontract.ObjectType]("демо не владеет типами модели"),
		Narrowers:     servicecontract.NotApplicable[map[servicecontract.MethodFQN]servicecontract.ListNarrower]("демо ничего не сужает"),
		HideExistence: servicecontract.NotApplicable[map[servicecontract.ObjectType]servicecontract.NotFoundFormat]("демо ничего не скрывает"),
		Delivery:      servicecontract.NotApplicable[servicecontract.DeliveryProvenance]("демо ничего не эмитит"),
		DenyBudget:    servicecontract.Value(100.0),
		AuthzObserve:  func(func() authz.Metrics) {},
		BootGate:      servicecontract.NotApplicable[servicecontract.BootGate]("очереди регистраций у демо нет"),
		StreamBudget:  servicecontract.NotApplicable[time.Duration]("демо не служит серверных стримов"),
		Admission: servicecontract.Value(servicecontract.Admission{
			Public:   grpcsrv.PlatformPublicAdmission(),
			Internal: grpcsrv.PlatformInternalAdmission(),
		}),
	}
}

// mustRefuse требует отказа старта, НАЗЫВАЮЩЕГО предмет: находка без координаты
// действием не является.
func mustRefuse(t *testing.T, s servicecontract.Spec, mustName string) {
	t.Helper()
	_, err := servicecontract.New(s)
	if err == nil {
		t.Fatalf("дескриптор принят — отказ по %s не способен упасть", mustName)
	}
	if !strings.Contains(err.Error(), mustName) {
		t.Fatalf("отказ не называет %q — чинить по нему нечего:\n%v", mustName, err)
	}
}

// TestAcceptableSpecIsAccepted — положительный контроль к mustRefuse.
func TestAcceptableSpecIsAccepted(t *testing.T) {
	if _, err := servicecontract.New(acceptableSpec()); err != nil {
		t.Fatalf("опорный дескриптор отвергнут — все отказы выше вакуумны: %v", err)
	}
}
