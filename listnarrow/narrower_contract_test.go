// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// narrower_contract_test.go — контракт единственного сужателя списков.
//
// Проверяется то, что раньше решалось по-разному в четырёх почти дословных копиях и
// у пятого сервиса — по-своему. Каждое утверждение здесь фиксирует ПОЛЯРНОСТЬ, то
// есть наблюдаемый исход, а не факт вызова:
//
//   - безымянный вызывающий получает ОТКАЗ, безусловно и ДО ветки «провязана ли
//     модель». Порядок принципиален: пока отсечка жила за этой веткой, посадка без
//     модели отдавала всю страницу кому угодно;
//   - «модель не провязана» — ОТКАЗ, а не «да». Состояние посадки разрешением не
//     бывает;
//   - аварийный режим остаётся, но каждый его случай СЧИТАЕТСЯ и НАЗЫВАЕТСЯ: иначе
//     он становится тихим штатным, и «им пользуются» неотличимо от «им не
//     пользуются»;
//   - предикат страницы — ПОЛЕ, а не константа пакета: сведение его к одному
//     значению меняло бы видимость, а это продуктовое решение по каждому ресурсу.
//
// У каждого отрицания есть ПАРНЫЙ ПОЛОЖИТЕЛЬНЫЙ: «отказано» без него неотличимо от
// «отказывает всегда», и отрицание зеленело бы сильнее всего именно на полностью
// сломанном пути.
package listnarrow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/operations"
)

const (
	testResourceType = "vpc_subnet"
	testAction       = "vpc.subnets.list"
)

// namedCaller — контекст с названным тенантным вызывающим.
func namedCaller() context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr_alice"})
}

// stubClient — приёмная сторона: отвечает по контракту BatchCheck (вердикт на каждый
// вопрос, в порядке вопросов).
type stubClient struct {
	mu       sync.Mutex
	calls    int
	checks   int
	maxBatch int
	allow    map[string]bool // id → видим
	relSeen  map[string]int  // отношение → сколько вопросов задано
	err      error           // если задана — возвращается вместо ответа
	short    bool            // вернуть ответ не той длины
	inflight atomic.Int64    // текущее число одновременных вызовов
	peak     atomic.Int64    // пик одновременности
	delay    time.Duration   // задержка ответа (для замера одновременности)
	onCall   func(n int)     // хук
}

func newStub(allow ...string) *stubClient {
	s := &stubClient{allow: map[string]bool{}, relSeen: map[string]int{}}
	for _, id := range allow {
		s.allow[id] = true
	}
	return s
}

func (s *stubClient) BatchCheck(ctx context.Context, checks []Check) ([]bool, error) {
	cur := s.inflight.Add(1)
	for {
		peak := s.peak.Load()
		if cur <= peak || s.peak.CompareAndSwap(peak, cur) {
			break
		}
	}
	defer s.inflight.Add(-1)

	s.mu.Lock()
	s.calls++
	n := s.calls
	s.checks += len(checks)
	if len(checks) > s.maxBatch {
		s.maxBatch = len(checks)
	}
	for _, c := range checks {
		s.relSeen[c.RequiredRelation]++
	}
	err, short := s.err, s.short
	s.mu.Unlock()

	if s.onCall != nil {
		s.onCall(n)
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	out := make([]bool, 0, len(checks))
	for _, c := range checks {
		out = append(out, s.allow[c.ResourceID])
	}
	if short && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out, nil
}

func (s *stubClient) stats() (calls, checks, maxBatch int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.checks, s.maxBatch
}

func (s *stubClient) relations() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.relSeen {
		out[k] = v
	}
	return out
}

// logSink — перехват записей с уровнем: «громко» проверяется уровнем, а не наличием
// слова в тексте.
type logSink struct{ buf bytes.Buffer }

func (s *logSink) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&s.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (s *logSink) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(s.buf.String()), "\n") {
		if ln == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(ln), &rec), "запись не разобралась: %s", ln)
		out = append(out, rec)
	}
	return out
}

// testConfig — посадка со всеми полями, названными явно.
func testConfig() Config {
	return Config{
		Relations:       map[string][]string{"": {"v_get"}},
		Timeout:         time.Second,
		Parallelism:     5,
		CacheTTL:        5 * time.Second,
		CacheMaxEntries: 1000,
	}
}

func ids(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("sub-%04d", i))
	}
	return out
}

// ───────────────── полярность: безымянный вызывающий ─────────────────

// TestIDs_UnnamedCallerIsRefusedBeforeTheWiringBranch — отсечка безымянного стоит
// ПЕРВОЙ. Модель здесь не провязана вовсе (nil), и именно поэтому проба различающая:
// если отсечка стоит ЗА веткой посадки, ответ будет про посадку, а не про личность.
func TestIDs_UnnamedCallerIsRefusedBeforeTheWiringBranch(t *testing.T) {
	got, err := IDs(context.Background(), nil, testResourceType, testAction, []string{"sub-1"})
	require.Error(t, err, "запрос никого не назвал — страница не отдаётся ни при какой посадке")
	assert.Equal(t, codes.Unauthenticated, status.Code(err),
		"ответ обязан быть про ЛИЧНОСТЬ, а не про то, что оператор прописал в конфиге")
	assert.Empty(t, got)
}

// TestPage_UnnamedCallerIsRefusedBeforeTheWiringBranch — второй вход в тот же
// контракт. Пропустить его значило бы оставить дыру ровно для списков, фильтрующих
// записи, а не голые идентификаторы.
func TestPage_UnnamedCallerIsRefusedBeforeTheWiringBranch(t *testing.T) {
	type rec struct{ id string }
	got, err := Page(context.Background(), nil, testResourceType, testAction,
		[]rec{{"sub-1"}}, func(r rec) string { return r.id })
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Empty(t, got)
}

// TestIDs_AnonymityIsNotAnIdentity — именованная анонимность личностью не является.
// Тип объявляет отправитель заголовков, поэтому проверять надо само слово.
func TestIDs_AnonymityIsNotAnIdentity(t *testing.T) {
	for _, p := range []operations.Principal{
		{Type: "user", ID: operations.AnonymousPrincipalID},
		{Type: "system", ID: "bootstrap"},
		{Type: "user", ID: ""},
		{Type: "", ID: "usr_alice"},
		{Type: "robot", ID: "rb_1"},        // тип не называет тенантного субъекта
		{Type: "user", ID: "usr_a#member"}, // разделитель FGA — субъект был бы сконструирован
		{Type: "user", ID: "usr_a:usr_b"},  // сдвиг границы type:id
	} {
		ctx := operations.WithPrincipal(context.Background(), p)
		_, err := IDs(ctx, nil, testResourceType, testAction, []string{"sub-1"})
		require.Error(t, err, "principal %+v", p)
		assert.Equal(t, codes.Unauthenticated, status.Code(err), "principal %+v", p)
	}
}

// TestIDs_NamedCallerGetsPastTheIdentityGate — ПАРНЫЙ ПОЛОЖИТЕЛЬНЫЙ к трём выше.
// Без него «безымянный отвергнут» зеленело бы на сужателе, отвергающем всех.
func TestIDs_NamedCallerGetsPastTheIdentityGate(t *testing.T) {
	cli := newStub("sub-0000")
	n := New(cli, testConfig())
	got, err := IDs(namedCaller(), n, testResourceType, testAction, ids(2))
	require.NoError(t, err)
	assert.Equal(t, []string{"sub-0000"}, got)
}

// ───────────────── полярность: модель не провязана ─────────────────

// TestIDs_UnwiredModelRefusesNeverPassthrough — «модели здесь нет» не есть «да».
// Вызывающий НАЗВАН, поэтому исход относится именно к посадке.
func TestIDs_UnwiredModelRefusesNeverPassthrough(t *testing.T) {
	for name, n := range map[string]*Narrower{
		"сужателя нет вовсе":       nil,
		"сужатель без собеседника": New(nil, testConfig()),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := IDs(namedCaller(), n, testResourceType, testAction, ids(3))
			require.Error(t, err, "спросить негде — страницу отдавать не на каком основании")
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
			assert.Empty(t, got, "нефильтрованная страница НИКОГДА не отдаётся")
		})
	}
}

// TestPage_UnwiredModelRefusesNeverPassthrough — тот же контракт вторым входом.
func TestPage_UnwiredModelRefusesNeverPassthrough(t *testing.T) {
	type rec struct{ id string }
	got, err := Page(namedCaller(), New(nil, testConfig()), testResourceType, testAction,
		[]rec{{"sub-1"}, {"sub-2"}}, func(r rec) string { return r.id })
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, got)
}

// ───────────────── аварийный режим: остаётся, но НАБЛЮДАЕМ ─────────────────

// TestIDs_BreakglassPassesAndIsCounted — аварийный режим отдаёт страницу (он для того
// и есть), но каждый его случай считается и называется. Считается ОТДЕЛЬНО от записи:
// по логу видно, что проход БЫЛ, и не видно, что его НЕ БЫЛО.
func TestIDs_BreakglassPassesAndIsCounted(t *testing.T) {
	sink := &logSink{}
	cfg := testConfig()
	cfg.Breakglass = true
	n := New(nil, cfg).WithLogger(sink.logger())

	before := n.Counts()
	require.Zero(t, before.Breakglass, "прочитанный ноль обязан быть отличим от отсутствия счётчика")

	got, err := IDs(namedCaller(), n, testResourceType, testAction, ids(3))
	require.NoError(t, err)
	assert.Equal(t, ids(3), got, "аварийный режим отдаёт страницу целиком — в этом его смысл")

	after := n.Counts()
	assert.Equal(t, uint64(1), after.Breakglass, "срабатывание обязано быть посчитано")

	recs := sink.records(t)
	require.Len(t, recs, 1, "срабатывание обязано быть названо")
	assert.Equal(t, "WARN", recs[0]["level"])
	assert.Equal(t, "user:usr_alice", recs[0]["subject"], "запись обязана называть субъекта")
	assert.Equal(t, testResourceType, recs[0]["resource_type"], "и метод/тип, на котором сработало")
}

// TestIDs_BreakglassStillRequiresAnIdentity — аварийный режим снимает вопрос о
// ПРАВАХ, но не требование ЛИЧНОСТИ. Иначе он же и есть исходная дыра.
func TestIDs_BreakglassStillRequiresAnIdentity(t *testing.T) {
	cfg := testConfig()
	cfg.Breakglass = true
	n := New(nil, cfg)
	_, err := IDs(context.Background(), n, testResourceType, testAction, ids(3))
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Zero(t, n.Counts().Breakglass, "до личности аварийный режим не доходит — считать нечего")
}

// ───────────────── предикат страницы — ПОЛЕ ─────────────────

// TestVisibleIDs_PredicateIsAFieldAndTotal — предикат берётся из посадки по ТИПУ, а
// запись под пустым ключом — умолчание для типа, не названного поимённо. Тотальность
// и есть то, что позволяет гейту паритета прочитать объявление и сверить его с
// каталогом потипно.
func TestVisibleIDs_PredicateIsAFieldAndTotal(t *testing.T) {
	cli := newStub()
	cfg := testConfig()
	cfg.Relations = map[string][]string{
		"":         {"v_get"},
		"iam_role": {"viewer", "v_list"},
	}
	n := New(cli, cfg)

	_, err := IDs(namedCaller(), n, "iam_role", "iam.roles.list", []string{"rol-1"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"viewer": 1, "v_list": 1}, cli.relations(),
		"названный тип обязан спрашиваться СВОИМ предикатом")

	cli2 := newStub()
	n2 := New(cli2, cfg)
	_, err = IDs(namedCaller(), n2, "vpc_subnet", testAction, []string{"sub-1"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"v_get": 1}, cli2.relations(),
		"неназванный тип обязан брать умолчание, а не оставаться без предиката")
}

// TestNew_RefusesAPredicateWithoutADefault — посадка без умолчания оставила бы тип
// без предиката, и такой тип прошёл бы БЕЗ вопроса. Это не настройка, а дыра, поэтому
// сборка сужателя её не принимает.
func TestNew_RefusesAPredicateWithoutADefault(t *testing.T) {
	cfg := testConfig()
	cfg.Relations = map[string][]string{"vpc_subnet": {"v_get"}}
	n := New(newStub(), cfg)
	_, err := IDs(namedCaller(), n, "vpc_gateway", "vpc.gateways.list", []string{"gw-1"})
	require.Error(t, err, "тип без предиката обязан быть отказом, а не молчаливым пропуском")
	assert.Equal(t, codes.Internal, status.Code(err))
}

// ───────────────── форма вопроса: партии и веер ─────────────────

// TestVisibleIDs_ContractPageIsAskedInBatchesNotOneByOne — предельная страница
// оплачивается партиями, а не поштучно, и партии идут ограниченным веером.
func TestVisibleIDs_ContractPageIsAskedInBatchesNotOneByOne(t *testing.T) {
	cli := newStub()
	cli.delay = 20 * time.Millisecond
	n := New(cli, testConfig())

	_, err := IDs(namedCaller(), n, testResourceType, testAction, ids(1000))
	require.NoError(t, err)

	calls, checks, maxBatch := cli.stats()
	assert.Equal(t, 1000, checks, "вопросов ровно по строке страницы")
	assert.Equal(t, 10, calls, "1000 строк одним отношением — 10 партий, а не 1000 запросов")
	assert.Equal(t, MaxBatchSize, maxBatch, "партия не превышает контрактный предел приёмной стороны")
	assert.LessOrEqual(t, int(cli.peak.Load()), 5, "веер ограничен посадкой")
	assert.Greater(t, int(cli.peak.Load()), 1, "и он именно веер, а не очередь")
}

// TestVisibleIDs_BudgetBelongsToTheRequest — бюджет операции выводится из глубины волн
// и per-call срока, а не задаётся независимой ручкой: иначе арифметика «сумма ожиданий
// помещается в бюджет» держалась бы на надежде.
func TestVisibleIDs_BudgetBelongsToTheRequest(t *testing.T) {
	n := New(newStub(), testConfig())
	assert.Equal(t, 2, n.WorstCaseDepth(), "10 партий веером 5 — две волны")
	assert.Equal(t, 3*time.Second, n.Budget(), "две волны по секунде плюс запас 50%")
}

// ───────────────── fail-closed ─────────────────

// TestVisibleIDs_MisalignedAnswerFailsClosed — ответ не той длины выровнять с
// вопросами нельзя, и страница, отфильтрованная смещённым ответом, неверна так, что
// вызывающий этого не обнаружит.
func TestVisibleIDs_MisalignedAnswerFailsClosed(t *testing.T) {
	cli := newStub("sub-0000")
	cli.short = true
	n := New(cli, testConfig())
	got, err := IDs(namedCaller(), n, testResourceType, testAction, ids(3))
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Empty(t, got)
}

// TestVisibleIDs_PeerErrorFailsClosed — отказ соседа не есть «никто ничего не видит»
// и не есть «все видят всё»: он есть отказ.
func TestVisibleIDs_PeerErrorFailsClosed(t *testing.T) {
	cli := newStub()
	cli.err = status.Error(codes.Internal, "boom")
	n := New(cli, testConfig())
	got, err := IDs(namedCaller(), n, testResourceType, testAction, ids(3))
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Empty(t, got)
}

// TestVisibleIDs_SoftPassSeparatesMisconfigurationFromOutage — мягкий проход остаётся
// задокументированным исключением, но обязан различать «сосед сейчас не отвечает» и
// «мы стучимся не туда»: второе само не пройдёт никогда.
func TestVisibleIDs_SoftPassSeparatesMisconfigurationFromOutage(t *testing.T) {
	t.Run("настройка — громко", func(t *testing.T) {
		sink := &logSink{}
		cli := newStub()
		cli.err = status.Error(codes.Unimplemented, "no such method")
		cfg := testConfig()
		cfg.SoftPassOnPeerFailure = true
		n := New(cli, cfg).WithLogger(sink.logger())

		got, err := IDs(namedCaller(), n, testResourceType, testAction, ids(2))
		require.NoError(t, err)
		assert.Equal(t, ids(2), got)
		assert.Equal(t, uint64(1), n.Counts().SoftPassMisconfigured)
		assert.Zero(t, n.Counts().SoftPassTransient)
		recs := sink.records(t)
		require.Len(t, recs, 1)
		assert.Equal(t, "ERROR", recs[0]["level"], "постоянное состояние не называется предупреждением о временном")
	})
	t.Run("сбой — предупреждением, но со счётчиком", func(t *testing.T) {
		sink := &logSink{}
		cli := newStub()
		cli.err = status.Error(codes.Unavailable, "peer down")
		cfg := testConfig()
		cfg.SoftPassOnPeerFailure = true
		n := New(cli, cfg).WithLogger(sink.logger())

		_, err := IDs(namedCaller(), n, testResourceType, testAction, ids(2))
		require.NoError(t, err)
		assert.Equal(t, uint64(1), n.Counts().SoftPassTransient)
		assert.Zero(t, n.Counts().SoftPassMisconfigured)
		recs := sink.records(t)
		require.Len(t, recs, 1)
		assert.Equal(t, "WARN", recs[0]["level"])
	})
	t.Run("здоровый путь ничего не считает", func(t *testing.T) {
		cfg := testConfig()
		cfg.SoftPassOnPeerFailure = true
		n := New(newStub("sub-0000"), cfg)
		_, err := IDs(namedCaller(), n, testResourceType, testAction, ids(2))
		require.NoError(t, err)
		c := n.Counts()
		assert.Zero(t, c.SoftPassMisconfigured)
		assert.Zero(t, c.SoftPassTransient)
	})
}

// ───────────────── кэш: только положительные вердикты ─────────────────

// TestVisibleIDs_OnlyPositiveVerdictsAreCached — отрицательный вердикт не кешируется,
// иначе свежая выдача не была бы видна до истечения окна; положительный кешируется,
// иначе окно не имело бы смысла.
func TestVisibleIDs_OnlyPositiveVerdictsAreCached(t *testing.T) {
	cli := newStub("sub-0000")
	n := New(cli, testConfig())

	_, err := IDs(namedCaller(), n, testResourceType, testAction, ids(2))
	require.NoError(t, err)
	_, checksAfterFirst, _ := cli.stats()
	require.Equal(t, 2, checksAfterFirst)

	_, err = IDs(namedCaller(), n, testResourceType, testAction, ids(2))
	require.NoError(t, err)
	_, checksAfterSecond, _ := cli.stats()
	assert.Equal(t, 3, checksAfterSecond,
		"положительный взят из окна, отрицательный спрошен заново")
}

// TestVisibleIDs_RevocationBecomesVisibleWhenTheWindowExpires — окно и есть весь
// механизм отзыва; часы управляемые, поэтому проба детерминирована.
func TestVisibleIDs_RevocationBecomesVisibleWhenTheWindowExpires(t *testing.T) {
	cli := newStub("sub-0000")
	cfg := testConfig()
	cfg.CacheTTL = 5 * time.Second
	n := New(cli, cfg)
	now := time.Now()
	n.WithClock(func() time.Time { return now })

	_, err := IDs(namedCaller(), n, testResourceType, testAction, []string{"sub-0000"})
	require.NoError(t, err)

	cli.mu.Lock()
	delete(cli.allow, "sub-0000")
	cli.mu.Unlock()

	got, err := IDs(namedCaller(), n, testResourceType, testAction, []string{"sub-0000"})
	require.NoError(t, err)
	assert.Equal(t, []string{"sub-0000"}, got, "внутри окна вердикт ещё действует")

	now = now.Add(6 * time.Second)
	got, err = IDs(namedCaller(), n, testResourceType, testAction, []string{"sub-0000"})
	require.NoError(t, err)
	assert.Empty(t, got, "по истечении окна отзыв виден")
}

// ───────────────── порядок и дубли ─────────────────

// TestVisibleIDs_KeepsCursorOrderAndPaysOnceForDuplicates — страница уже упорядочена
// курсором; переупорядочивание сломало бы пагинацию, а повтор id не должен стоить
// второго вопроса.
func TestVisibleIDs_KeepsCursorOrderAndPaysOnceForDuplicates(t *testing.T) {
	cli := newStub("b", "a")
	n := New(cli, testConfig())
	got, err := IDs(namedCaller(), n, testResourceType, testAction, []string{"a", "b", "a", "c"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "a"}, got, "порядок входа сохранён, включая повтор")
	_, checks, _ := cli.stats()
	assert.Equal(t, 3, checks, "за повтор платят один раз")
}

// TestVisibleIDs_EmptyPageAsksNothing — пустая страница не стоит обращения к соседу.
func TestVisibleIDs_EmptyPageAsksNothing(t *testing.T) {
	cli := newStub()
	n := New(cli, testConfig())
	got, err := IDs(namedCaller(), n, testResourceType, testAction, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	calls, _, _ := cli.stats()
	assert.Zero(t, calls)
}

// TestVisibleIDs_SecondRelationOnlyAsksWhatTheFirstDenied — отношения последовательны
// по построению: второе спрашивается только у отказанных первым. Это и есть то, что
// удерживает стоимость на «одно отношение на объект, пока одно не разрешило».
func TestVisibleIDs_SecondRelationOnlyAsksWhatTheFirstDenied(t *testing.T) {
	cli := newStub("rol-1") // «viewer» разрешит первому, второму — нет
	cfg := testConfig()
	cfg.Relations = map[string][]string{"": {"viewer", "v_list"}}
	n := New(cli, cfg)

	got, err := IDs(namedCaller(), n, "iam_role", "iam.roles.list", []string{"rol-1", "rol-2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"rol-1"}, got)
	assert.Equal(t, map[string]int{"viewer": 2, "v_list": 1}, cli.relations(),
		"второе отношение спрошено только про отказанный объект")
}

// TestVisibleIDs_ErrorFromTheWireIsNotSwallowedAsDeny — ошибка транспорта не есть
// отказ модели.
func TestVisibleIDs_ErrorFromTheWireIsNotSwallowedAsDeny(t *testing.T) {
	cli := newStub()
	cli.err = errors.New("not a grpc status")
	n := New(cli, testConfig())
	_, err := IDs(namedCaller(), n, testResourceType, testAction, ids(2))
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}
