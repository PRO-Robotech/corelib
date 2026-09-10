// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// surface_refusal_test.go — доказательство того, что отказы ПРОФИЛЯ не-gRPC
// поверхности способны упасть, и падают на существе.
//
// Форма та же, что у refusal_test.go, и это не совпадение: у профиля тот же
// механизм — объявление, конструктор, отказ, называющий ось. Каждая проба —
// ПАРА: инъекция настоящим входом плюс законный близнец той же формы.
package servicecontract_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/servicecontract"
)

// lawfulSurface — законный профиль ДИАГНОСТИЧЕСКОЙ поверхности, от которого
// отталкивается каждая инъекция.
//
// Взята именно диагностика: у неё аутентификация снята осознанно, то есть она
// единственная законно проходит ось [servicecontract.Surface.Auth] через
// `NotApplicable`. Начинать с поверхности, где аутентификация есть, значило бы
// не иметь положительного контроля на самом спорном сочетании.
func lawfulSurface() servicecontract.Surface {
	return servicecontract.Surface{
		Service: "kacho-demo",
		Name:    "метрики",
		Mode:    servicecontract.ModeProduction,
		Addr:    servicecontract.Value(":9102"),
		Handler: http.NewServeMux(),
		Reach:   servicecontract.ReachClusterInternal,
		Auth: servicecontract.NotApplicable[servicecontract.SurfaceAuthMech](
			"отдаётся только скрейпу внутри кластера и несёт лишь счётчики процесса"),
		ReadHeaderBudget: 5 * time.Second,
		RequestBudget:    servicecontract.Value(30 * time.Second),
		IdleBudget:       60 * time.Second,
		ShutdownBudget:   5 * time.Second,
	}
}

// TestLawfulSurfaceIsAccepted — положительный контроль. Пока он красный, каждое
// отрицание ниже утверждает «отказано» по чужой причине.
func TestLawfulSurfaceIsAccepted(t *testing.T) {
	d, err := servicecontract.NewSurface(lawfulSurface())
	if err != nil {
		t.Fatalf("законный профиль отвергнут — все отрицания ниже вакуумны: %v", err)
	}
	if !d.Accepted() || !d.Enabled() {
		t.Fatalf("профиль принят, но не объявляет себя поднимаемым: accepted=%v enabled=%v",
			d.Accepted(), d.Enabled())
	}
}

// refusesSurface прогоняет инъекцию и требует, чтобы отказ НАЗЫВАЛ ось.
func refusesSurface(t *testing.T, s servicecontract.Surface, mustName ...string) string {
	t.Helper()
	d, err := servicecontract.NewSurface(s)
	if err == nil {
		t.Fatalf("профиль принят, хотя обязан быть отвергнут")
	}
	if d.Accepted() {
		t.Fatalf("отказ вернул принятый профиль — по нему подняли бы слушатель")
	}
	msg := err.Error()
	for _, name := range mustName {
		if !strings.Contains(msg, name) {
			t.Fatalf("отказ не называет %q — по нему нечего чинить:\n%s", name, msg)
		}
	}
	return msg
}

// TestSurfaceRefusesUndeclaredAuth — ЦЕНТРАЛЬНЫЙ отказ профиля: слушатель без
// объявленного решения об аутентификации не поднимается.
//
// Пара обязательна и здесь особенно: «объявлено» и «не объявлено» — состояния
// ОДНОГО поля, и проба, знающая только про красное, зеленела бы на профиле,
// который отвергает вообще всё.
func TestSurfaceRefusesUndeclaredAuth(t *testing.T) {
	s := lawfulSurface()
	s.Auth = servicecontract.Axis[servicecontract.SurfaceAuthMech]{}
	refusesSurface(t, s, "Auth", "не объявлено")

	// Законный близнец 1: снятие с причиной.
	if _, err := servicecontract.NewSurface(lawfulSurface()); err != nil {
		t.Fatalf("снятие с причиной отвергнуто: %v", err)
	}
	// Законный близнец 2: аутентификация есть и названа.
	named := lawfulSurface()
	named.Auth = servicecontract.Value[servicecontract.SurfaceAuthMech](
		"подпись вебхука провайдера личности")
	if _, err := servicecontract.NewSurface(named); err != nil {
		t.Fatalf("названный механизм отвергнут: %v", err)
	}
}

// TestSurfaceRefusesEmptyAuthMechanism — снятие требует СЛОВ с обеих сторон:
// пустой механизм объявлением не является так же, как пустая причина.
func TestSurfaceRefusesEmptyAuthMechanism(t *testing.T) {
	s := lawfulSurface()
	s.Auth = servicecontract.Value[servicecontract.SurfaceAuthMech]("   ")
	refusesSurface(t, s, "Auth", "пусто")

	empty := lawfulSurface()
	empty.Auth = servicecontract.NotApplicable[servicecontract.SurfaceAuthMech]("")
	// Пустая причина не делает ось объявленной — отказ тот же, что у необъявленной.
	refusesSurface(t, empty, "Auth", "не объявлено")
}

// TestSurfaceRefusesUnauthenticatedExternalReach — пара осей, судящая по
// существу: снятая аутентификация защитима внутренним периметром и публичностью
// материала, снаружи — ничем.
//
// Производителя этого входа в дереве сегодня НЕТ (обе внешние поверхности
// аутентифицируют обработчиком), и это ровно тот случай, когда инъекция
// обязательна: без неё отказ был бы объявлением, а не проверкой.
func TestSurfaceRefusesUnauthenticatedExternalReach(t *testing.T) {
	s := lawfulSurface()
	s.Reach = servicecontract.ReachExternal
	refusesSurface(t, s, "Auth", "ИЗВНЕ")

	// Законный близнец: та же внешняя досягаемость, но аутентификация названа.
	twin := lawfulSurface()
	twin.Reach = servicecontract.ReachExternal
	twin.Auth = servicecontract.Value[servicecontract.SurfaceAuthMech](
		"ключ служебной учётки, проверяется обработчиком на каждом запросе")
	if _, err := servicecontract.NewSurface(twin); err != nil {
		t.Fatalf("внешняя поверхность с названной аутентификацией отвергнута: %v", err)
	}
}

// TestSurfaceRefusesUndeclaredReach — без досягаемости предыдущий отказ судить
// нечем, поэтому ось обязательна сама по себе.
func TestSurfaceRefusesUndeclaredReach(t *testing.T) {
	s := lawfulSurface()
	s.Reach = 0
	refusesSurface(t, s, "Reach")
}

// TestSurfaceRefusesSilentDisableByEmptyAddress — выключение поверхности есть
// ОБЪЯВЛЕНИЕ. Пустая строка им не является, необъявленная ось — тем более.
func TestSurfaceRefusesSilentDisableByEmptyAddress(t *testing.T) {
	byEmpty := lawfulSurface()
	byEmpty.Addr = servicecontract.Value("")
	refusesSurface(t, byEmpty, "Addr", "молчаливое выключение")

	undeclared := lawfulSurface()
	undeclared.Addr = servicecontract.Axis[string]{}
	refusesSurface(t, undeclared, "Addr", "не объявлена")

	// Законный близнец: выключено С ПРИЧИНОЙ — принимается, и профиль сам
	// сообщает, что поднимать нечего.
	off := lawfulSurface()
	off.Addr = servicecontract.NotApplicable[string](
		"на этой посадке скрейпа нет: эндпоинт диагностики профилем не задан")
	off.Handler = nil // выключенной поверхности обслуживать нечего
	d, err := servicecontract.NewSurface(off)
	if err != nil {
		t.Fatalf("объявленное выключение отвергнуто: %v", err)
	}
	if d.Enabled() {
		t.Fatal("профиль объявлен выключенным, а сообщает, что поднимается")
	}
	if d.DisabledBecause() == "" {
		t.Fatal("выключено, а причина не доехала до вызывающего — в журнале это снова тишина")
	}
}

// TestSurfaceRefusesMissingHandler — включённой поверхности обслуживать нечем.
func TestSurfaceRefusesMissingHandler(t *testing.T) {
	s := lawfulSurface()
	s.Handler = nil
	refusesSurface(t, s, "Handler")
}

// TestSurfaceRefusesMissingBudgets — четыре срока, у каждого свой предмет, и ни
// один не имеет нейтрального нуля.
func TestSurfaceRefusesMissingBudgets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(*servicecontract.Surface)
		names  []string
	}{
		{"заголовок", func(s *servicecontract.Surface) { s.ReadHeaderBudget = 0 },
			[]string{"ReadHeaderBudget"}},
		{"простой", func(s *servicecontract.Surface) { s.IdleBudget = 0 },
			[]string{"IdleBudget"}},
		{"гашение", func(s *servicecontract.Surface) { s.ShutdownBudget = 0 },
			[]string{"ShutdownBudget", "порт остаётся занят"}},
		{"запрос не объявлен", func(s *servicecontract.Surface) {
			s.RequestBudget = servicecontract.Axis[time.Duration]{}
		}, []string{"RequestBudget", "не объявлен"}},
		{"запрос уже заголовка", func(s *servicecontract.Surface) {
			s.ReadHeaderBudget = 30 * time.Second
			s.RequestBudget = servicecontract.Value(10 * time.Second)
		}, []string{"RequestBudget", "не больше потолка его заголовка"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := lawfulSurface()
			tc.break_(&s)
			refusesSurface(t, s, tc.names...)
		})
	}

	// Законный близнец потолка запроса: объявленная НЕПРИМЕНИМОСТЬ — то, чем
	// живёт потоковая поверхность.
	streaming := lawfulSurface()
	streaming.RequestBudget = servicecontract.NotApplicable[time.Duration](
		"слой образа едет минутами; потолок записи разорвал бы исправную передачу")
	if _, err := servicecontract.NewSurface(streaming); err != nil {
		t.Fatalf("потоковая поверхность отвергнута: %v", err)
	}
}

// TestSurfaceRefusalNamesEveryFindingAtOnce — отказ по первой находке заставлял
// бы автора узнавать о следующей на следующем старте.
func TestSurfaceRefusalNamesEveryFindingAtOnce(t *testing.T) {
	s := lawfulSurface()
	s.Auth = servicecontract.Axis[servicecontract.SurfaceAuthMech]{}
	s.Reach = 0
	s.ShutdownBudget = 0
	msg := refusesSurface(t, s, "Auth", "Reach", "ShutdownBudget")
	t.Logf("отказ назвал все три оси разом:\n%s", msg)
}

// TestUnacceptedSurfaceStatesNothing — нулевое значение профиля не выдаёт себя
// за объявление ни одним ответом.
func TestUnacceptedSurfaceStatesNothing(t *testing.T) {
	var d servicecontract.SurfaceDescriptor
	if d.Accepted() || d.Enabled() || d.DisabledBecause() != "" {
		t.Fatalf("нулевой профиль отвечает как принятый: accepted=%v enabled=%v because=%q",
			d.Accepted(), d.Enabled(), d.DisabledBecause())
	}
	if !strings.Contains(d.AuthStatement(), "не принят") {
		t.Fatalf("нулевой профиль объявляет решение об аутентификации: %q", d.AuthStatement())
	}
}

// TestAcceptedSurfaceStatesItsAuthDecisionInOneLine — самоотчёт обязан РАЗЛИЧАТЬ
// снятое осознанно и названный механизм. Пока строку собирал вызывающий, в
// журнале они выглядели одинаково — то есть различие, ради которого ось
// заведена, до оператора не доезжало.
func TestAcceptedSurfaceStatesItsAuthDecisionInOneLine(t *testing.T) {
	off, err := servicecontract.NewSurface(lawfulSurface())
	if err != nil {
		t.Fatalf("законный профиль отвергнут: %v", err)
	}
	if !strings.Contains(off.AuthStatement(), "ОСОЗНАННО") {
		t.Fatalf("снятие не объявлено снятием: %q", off.AuthStatement())
	}

	on := lawfulSurface()
	on.Auth = servicecontract.Value[servicecontract.SurfaceAuthMech]("подпись вебхука провайдера")
	d, err := servicecontract.NewSurface(on)
	if err != nil {
		t.Fatalf("названный механизм отвергнут: %v", err)
	}
	if !strings.Contains(d.AuthStatement(), "подпись вебхука провайдера") {
		t.Fatalf("самоотчёт не называет механизм: %q", d.AuthStatement())
	}
	if strings.Contains(d.AuthStatement(), "ОСОЗНАННО") {
		t.Fatalf("названный механизм прочитан как снятие: %q", d.AuthStatement())
	}
}
