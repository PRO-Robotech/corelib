// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// audit_test.go — доказательство того, что отказы старта, которым нужен
// СЛУЖИМЫЙ НАБОР RPC, способны упасть, и что падают они на существе.
//
// Все пробы гоняют ТУ ЖЕ функцию, что и носитель (`audit`), на синтетическом
// входе. Проба, повторяющая логику отказа своей копией, доказывала бы свойство
// копии.
//
// # Почему вход синтетический, а не «дескриптор переведённого сервиса»
//
// У первого переведённого сервиса производителя половины этих входов НЕТ вовсе:
// в каталоге его домена ноль строк `scope_filtered` и ноль скрывающих
// существование. Прогон на нём подтвердил бы только то, что отказ бывает, и
// молчал бы о том, отличает ли он законное от незаконного. Поэтому пары
// «инъекция + законный близнец» собираются здесь.
package servicehost

import (
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// lawfulCatalog — каталог домена-демо: два метода, ни один не сужается и ничего
// не скрывает. Форма повторяет то, что производит вывод из аннотаций.
func lawfulCatalog() catalogView {
	return catalogView{rows: map[servicecontract.MethodFQN]catalogRow{
		"/kacho.cloud.demo.v1.WidgetService/Get": {
			Method: "/kacho.cloud.demo.v1.WidgetService/Get",
			Domain: "kacho.cloud.demo.v1",
		},
		"/kacho.cloud.demo.v1.WidgetService/List": {
			Method: "/kacho.cloud.demo.v1.WidgetService/List",
			Domain: "kacho.cloud.demo.v1",
		},
	}}
}

// lawfulServed — служимый набор, в точности покрытый каталогом выше.
func lawfulServed() servedSet {
	return servedSet{methods: []servicecontract.MethodFQN{
		"/kacho.cloud.demo.v1.WidgetService/Get",
		"/kacho.cloud.demo.v1.WidgetService/List",
	}}
}

// lawfulMap — карта, которую реально спрашивает звено решения о доступе.
func lawfulMap() authz.RPCMap {
	return authz.RPCMap{
		"/kacho.cloud.demo.v1.WidgetService/Get":  {Public: true},
		"/kacho.cloud.demo.v1.WidgetService/List": {Public: true},
	}
}

// naAxes — дескриптор домена, у которого обе судимые каталогом оси объявлены
// неприменимыми. Это законно РОВНО ПОКА каталог молчит; как только в нём
// появляется соответствующая строка, заявление становится находкой.
func naAxes() servicecontract.Spec {
	return servicecontract.Spec{
		Service: "kacho-demo",
		Narrowers: servicecontract.NotApplicable[map[servicecontract.MethodFQN]servicecontract.ListNarrower](
			"ни один метод демо не сужается по правам"),
		HideExistence: servicecontract.NotApplicable[map[servicecontract.ObjectType]servicecontract.NotFoundFormat](
			"демо не скрывает существование ни одного типа"),
		StreamBudget: servicecontract.NotApplicable[time.Duration](
			"демо не служит серверных стримов"),
	}
}

// streamingCatalog — тот же каталог, у которого ОДИН метод отдаёт серверный
// стрим. Признак ставится там же, где его снимает носитель, — в строке
// каталога, выведенной из дескриптора метода.
func streamingCatalog() catalogView {
	cat := lawfulCatalog()
	row := cat.rows[streamMethod]
	row.ServerStreaming = true
	cat.rows[streamMethod] = row
	return cat
}

// streamMethod — метод, которому в пробах ниже приписывается стрим.
const streamMethod servicecontract.MethodFQN = "/kacho.cloud.demo.v1.WidgetService/Get"

// TestAuditAcceptsLawfulTree — положительный контроль. Первым намеренно: пока он
// красный, каждое отрицание ниже утверждает «отказано» по чужой причине.
func TestAuditAcceptsLawfulTree(t *testing.T) {
	c, err := audit(naAxes(), lawfulServed(), lawfulCatalog(), lawfulMap())
	if err != nil {
		t.Fatalf("законный набор отвергнут — все отрицания ниже вакуумны: %v", err)
	}
	if c.methods != 2 || c.domains != 1 {
		t.Fatalf("перепись не сходится: %+v", c)
	}
	t.Logf("перепись: %s", c)
}

func refusesAudit(t *testing.T, s servicecontract.Spec, sv servedSet, cat catalogView, m authz.RPCMap, mustName ...string) string {
	t.Helper()
	_, err := audit(s, sv, cat, m)
	if err == nil {
		t.Fatal("набор принят — отказ не способен упасть")
	}
	for _, want := range mustName {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("отказ не называет %q — по нему нечего чинить:\n%v", want, err)
		}
	}
	return err.Error()
}

// ── О2: служится RPC, у которого нет строки каталога его домена ─────────────

func TestO2_ServedMethodWithoutCatalogRowRefusesStart(t *testing.T) {
	sv := lawfulServed()
	sv.methods = append(sv.methods, "/kacho.cloud.demo.v1.WidgetService/Delete")
	msg := refusesAudit(t, naAxes(), sv, lawfulCatalog(), lawfulMap(),
		"/kacho.cloud.demo.v1.WidgetService/Delete")
	t.Logf("О2 (а) красный: %s", msg)
}

// TestO2_PlatformServiceIsExemptAndNamed — законный близнец, и он несущий.
// Носитель служит не только доменные RPC: здоровье и рефлексия приезжают вместе
// с сервером, строк каталога у них нет и быть не может. Без ИМЕНОВАННОГО
// изъятия ни один процесс не поднялся бы вовсе; без ЗАКРЫТОГО перечня изъятие
// превратилось бы в корзину «всё, что не наше», куда однажды попадёт доменный RPC.
func TestO2_PlatformServiceIsExemptAndNamed(t *testing.T) {
	sv := lawfulServed()
	for name := range platformServices {
		sv.methods = append(sv.methods, servicecontract.MethodFQN("/"+name+"/Check"))
	}
	if _, err := audit(naAxes(), sv, lawfulCatalog(), lawfulMap()); err != nil {
		t.Fatalf("платформенная регистрация объявлена находкой: %v", err)
	}
}

// TestO2_UnknownNonKachoServiceIsNotExempt — обратная половина того же: изъятие
// работает по ЗАКРЫТОМУ ИМЕНИ, а не по признаку «не наш пакет». Иначе любой
// чужой пакет молча получал бы освобождение.
func TestO2_UnknownNonKachoServiceIsNotExempt(t *testing.T) {
	sv := lawfulServed()
	sv.methods = append(sv.methods, "/some.third.party.v1.Service/Do")
	refusesAudit(t, naAxes(), sv, lawfulCatalog(), lawfulMap(), "some.third.party.v1.Service")
}

// ── О9: у выведенного домена НОЛЬ строк каталога ────────────────────────────

func TestO9_DerivedDomainWithZeroCatalogRowsRefusesStart(t *testing.T) {
	sv := lawfulServed()
	sv.methods = append(sv.methods, "/kacho.cloud.ghost.v1.GhostService/Get")
	cat := lawfulCatalog()
	// Строка у метода есть, но домен не дал каталогу НИ ОДНОЙ записи: ровно тот
	// случай, когда «ноль целей» неотличим от «всё синхронно».
	cat.rows["/kacho.cloud.ghost.v1.GhostService/Get"] = catalogRow{
		Method: "/kacho.cloud.ghost.v1.GhostService/Get",
		Domain: "kacho.cloud.ghost.v1",
	}
	m := lawfulMap()
	m["/kacho.cloud.ghost.v1.GhostService/Get"] = authz.RPCEntry{Public: true}

	// Домен есть в служимом наборе и в строках, но карта, которую спрашивает
	// звено решения, о нём не знает ни одной записи.
	delete(m, "/kacho.cloud.ghost.v1.GhostService/Get")
	refusesAudit(t, naAxes(), sv, cat, m, "kacho.cloud.ghost.v1")
}

// TestO9_EveryDerivedDomainContributesRows — законный близнец: домен, давший
// карте хотя бы одну запись, находкой не является.
func TestO9_EveryDerivedDomainContributesRows(t *testing.T) {
	if _, err := audit(naAxes(), lawfulServed(), lawfulCatalog(), lawfulMap()); err != nil {
		t.Fatalf("домен со строками объявлен пустым: %v", err)
	}
}

// ── О3: каталог объявляет сужение, дескриптор сужателя не несёт ─────────────

func TestO3_DeclaredNarrowingWithoutWiringRefusesStart(t *testing.T) {
	cat := lawfulCatalog()
	row := cat.rows["/kacho.cloud.demo.v1.WidgetService/List"]
	row.ScopeFiltered = true
	cat.rows["/kacho.cloud.demo.v1.WidgetService/List"] = row

	msg := refusesAudit(t, naAxes(), lawfulServed(), cat, lawfulMap(),
		"/kacho.cloud.demo.v1.WidgetService/List", "Narrowers")
	t.Logf("О3 (а) красный: %s", msg)
}

// TestO3_NotApplicableIsJudgedByTheCatalog — то же зеркально и это ГЛАВНОЕ в
// О3: заявление «не применимо» истекает САМО. Пока каталог молчит — законно;
// появилась строка `scope_filtered` — то же самое заявление стало находкой,
// и никто не обязан о нём вспоминать.
func TestO3_NotApplicableIsJudgedByTheCatalog(t *testing.T) {
	// (б) каталог молчит → то же заявление принимается.
	if _, err := audit(naAxes(), lawfulServed(), lawfulCatalog(), lawfulMap()); err != nil {
		t.Fatalf("заявление «не применимо» отвергнуто при молчащем каталоге: %v", err)
	}
	// (а) каталог заговорил → то же заявление краснеет.
	cat := lawfulCatalog()
	row := cat.rows["/kacho.cloud.demo.v1.WidgetService/List"]
	row.ScopeFiltered = true
	cat.rows["/kacho.cloud.demo.v1.WidgetService/List"] = row
	refusesAudit(t, naAxes(), lawfulServed(), cat, lawfulMap(), "Narrowers")
}

// TestO3_WiredNarrowerSatisfiesTheCatalog — вторая законная сторона: проводка
// предъявлена ровно на объявленный каталогом метод → молчание.
func TestO3_WiredNarrowerSatisfiesTheCatalog(t *testing.T) {
	cat := lawfulCatalog()
	row := cat.rows["/kacho.cloud.demo.v1.WidgetService/List"]
	row.ScopeFiltered = true
	cat.rows["/kacho.cloud.demo.v1.WidgetService/List"] = row

	s := naAxes()
	s.Narrowers = servicecontract.Value(map[servicecontract.MethodFQN]servicecontract.ListNarrower{
		"/kacho.cloud.demo.v1.WidgetService/List": stubNarrower(),
	})
	if _, err := audit(s, lawfulServed(), cat, lawfulMap()); err != nil {
		t.Fatalf("предъявленная проводка объявлена находкой: %v", err)
	}
}

// ── О4: дескриптор несёт сужатель на метод, которого каталог таковым не звал ──

// TestO4_WiringWithoutDeclarationRefusesStart — зеркальная половина О3, и без
// неё ПРОПАЖА ПРОВОДКИ невидима: сужатель, потерянный при переносе слоёв, не
// краснеет нигде, потому что каталог о нём не спрашивает.
func TestO4_WiringWithoutDeclarationRefusesStart(t *testing.T) {
	s := naAxes()
	s.Narrowers = servicecontract.Value(map[servicecontract.MethodFQN]servicecontract.ListNarrower{
		"/kacho.cloud.demo.v1.WidgetService/Get": stubNarrower(),
	})
	msg := refusesAudit(t, s, lawfulServed(), lawfulCatalog(), lawfulMap(),
		"/kacho.cloud.demo.v1.WidgetService/Get", "Narrowers")
	t.Logf("О4 (а) красный: %s", msg)
}

// TestO4_NilNarrowerIsNotWiring — проводка, объявленная нулевым указателем, —
// не проводка. Без этой половины «сужатель предъявлен» удовлетворялось бы
// записью карты, которая на запросе паникует.
func TestO4_NilNarrowerIsNotWiring(t *testing.T) {
	cat := lawfulCatalog()
	row := cat.rows["/kacho.cloud.demo.v1.WidgetService/List"]
	row.ScopeFiltered = true
	cat.rows["/kacho.cloud.demo.v1.WidgetService/List"] = row

	s := naAxes()
	s.Narrowers = servicecontract.Value(map[servicecontract.MethodFQN]servicecontract.ListNarrower{
		"/kacho.cloud.demo.v1.WidgetService/List": nil,
	})
	refusesAudit(t, s, lawfulServed(), cat, lawfulMap(), "Narrowers")
}

// hidingOnBothSides выставляет признак скрытия ТАМ ЖЕ, где он живёт на реальном
// пути, — и в строке каталога, и в записи карты прав.
//
// Две структуры, один источник: обе приезжают из одной аннотации дескриптора.
// Пробы выставляли признак только в каталоге, и это работало, пока судья читал
// каталог. Судья сведён с рантаймом (`authz.HidesExistenceOnDeny` смотрит запись
// карты), поэтому проба, правящая одну сторону, описывала бы состояние, которого
// на реальном пути не бывает: аннотация не может приехать в каталог и не приехать
// в карту.
func hidingOnBothSides(cat catalogView, m authz.RPCMap, method string,
	ot servicecontract.ObjectType) (catalogView, authz.RPCMap) {
	row := cat.rows[servicecontract.MethodFQN(method)]
	row.HideExistence = true
	row.ObjectType = ot
	cat.rows[servicecontract.MethodFQN(method)] = row

	e := m[method]
	e.HideExistence = true
	m[method] = e
	return cat, m
}

// ── О5: каталог называет тип среди скрывающих, форма отказа не объявлена ────

func TestO5_HiddenTypeWithoutRefusalFormRefusesStart(t *testing.T) {
	cat := lawfulCatalog()
	row := cat.rows["/kacho.cloud.demo.v1.WidgetService/Get"]
	row.HideExistence = true
	row.ObjectType = "demo_widget"
	cat.rows["/kacho.cloud.demo.v1.WidgetService/Get"] = row

	msg := refusesAudit(t, naAxes(), lawfulServed(), cat, lawfulMap(), "demo_widget", "HideExistence")
	t.Logf("О5 (а) красный: %s", msg)
}

// ownerVoicedType — тип, у которого ДЕЙСТВИТЕЛЬНО есть текст владельца, вместе
// с этим текстом.
//
// Имя типа выписано, а форма — НЕТ: она берётся у того, кто ею отвечает
// (`pkg/authz`). Выписанная форма разошлась бы с производителем ровно тем
// способом, который отказ О5 и ловит, — и проба зеленела бы на своей копии.
// Отсутствие типа в таблице делает пробу невозможной, и это сказано вслух:
// иначе «тип сняли» выглядело бы как «проверка прошла».
func ownerVoicedType(t *testing.T) (servicecontract.ObjectType, servicecontract.NotFoundFormat) {
	t.Helper()
	const ot = "vpc_network"
	form, ok := authz.OwnerNotFoundFormat(ot)
	if !ok {
		t.Fatalf("тип %q больше не значится в таблице промахов владельцев — законного близнеца "+
			"построить не из чего. Возьмите любой действующий тип: проба обязана утверждать "+
			"сходимость объявления с производителем текста, а не молчать", ot)
	}
	return ot, servicecontract.NotFoundFormat(form)
}

// TestO5_DeclaredFormSatisfiesTheCatalog — законный близнец.
func TestO5_DeclaredFormSatisfiesTheCatalog(t *testing.T) {
	ot, form := ownerVoicedType(t)

	cat, rights := hidingOnBothSides(lawfulCatalog(), lawfulMap(),
		"/kacho.cloud.demo.v1.WidgetService/Get", ot)

	s := naAxes()
	s.HideExistence = servicecontract.Value(map[servicecontract.ObjectType]servicecontract.NotFoundFormat{
		ot: form,
	})
	if _, err := audit(s, lawfulServed(), cat, rights); err != nil {
		t.Fatalf("объявленная форма отказа объявлена находкой: %v", err)
	}
}

// TestO5_DeclaredFormDivergingFromTheOwnerVoiceIsRefused — форма, разошедшаяся с
// той, которой РЕАЛЬНО ответит звено решения о доступе.
//
// Это второе место об одном предмете: объявление в дескрипторе и таблица, из
// которой берётся текст. Отвечает таблица, поэтому разошедшееся объявление
// описывает не действительность — и без этого отказа расхождение не краснело бы
// нигде.
func TestO5_DeclaredFormDivergingFromTheOwnerVoiceIsRefused(t *testing.T) {
	ot, form := ownerVoicedType(t)

	cat, rights := hidingOnBothSides(lawfulCatalog(), lawfulMap(),
		"/kacho.cloud.demo.v1.WidgetService/Get", ot)

	s := naAxes()
	// Форма правильной ФОРМЫ (ровно один `%s`) и неправильного ТЕКСТА: проверка
	// обязана ловить именно расхождение, а не подстановку.
	s.HideExistence = servicecontract.Value(map[servicecontract.ObjectType]servicecontract.NotFoundFormat{
		ot: "Resource %s does not exist",
	})
	msg := refusesAudit(t, s, lawfulServed(), cat, rights, string(ot), "расходится")
	if !strings.Contains(msg, string(form)) {
		t.Fatalf("отказ не назвал форму, которой ответит звено, — чинить по нему нечего:\n%s", msg)
	}
	t.Logf("расхождение объявления с голосом владельца красное: %s", msg)
}

// TestO5_TypeWithoutOwnerVoiceIsRefused — тип, у которого текста владельца нет
// вовсе: звено ответило бы нейтральным «not found», не похожим ни на один ответ
// владельца, — то есть той самой приметой, ради устранения которой скрытие и
// делается.
func TestO5_TypeWithoutOwnerVoiceIsRefused(t *testing.T) {
	const absent = "demo_widget"
	if _, ok := authz.OwnerNotFoundFormat(absent); ok {
		t.Fatalf("тип %q завели в таблицу промахов владельцев — проба потеряла предмет "+
			"и обязана взять другой отсутствующий тип, а не зеленеть", absent)
	}
	cat, rights := hidingOnBothSides(lawfulCatalog(), lawfulMap(),
		"/kacho.cloud.demo.v1.WidgetService/Get", absent)

	s := naAxes()
	s.HideExistence = servicecontract.Value(map[servicecontract.ObjectType]servicecontract.NotFoundFormat{
		absent: "Widget %s not found",
	})
	refusesAudit(t, s, lawfulServed(), cat, rights, absent, "таблице промахов владельцев")
}

// TestO5_FormWithoutSingleVerbIsRefused — форма отказа обязана нести РОВНО один
// `%s` под идентификатор. Скрытие существования работает ровно настолько,
// насколько текст неотличим от настоящего промаха владельца; форма без
// подстановки или с двумя подстановками даёт отличимый текст, то есть оракул.
func TestO5_FormWithoutSingleVerbIsRefused(t *testing.T) {
	cat := lawfulCatalog()
	row := cat.rows["/kacho.cloud.demo.v1.WidgetService/Get"]
	row.HideExistence = true
	row.ObjectType = "demo_widget"
	cat.rows["/kacho.cloud.demo.v1.WidgetService/Get"] = row

	for _, bad := range []servicecontract.NotFoundFormat{
		"Widget not found",
		"Widget %s %s not found",
		"Widget %d not found",
	} {
		s := naAxes()
		s.HideExistence = servicecontract.Value(map[servicecontract.ObjectType]servicecontract.NotFoundFormat{
			"demo_widget": bad,
		})
		refusesAudit(t, s, lawfulServed(), cat, lawfulMap(), "demo_widget")
	}
}

// TestO5_FormForATypeTheCatalogNeverHidesIsRefused — самоистечение и здесь:
// объявленная форма отказа, которой больше нечего скрывать, — находка. Иначе
// запись переживает свой предмет и утверждает, что платформа отвечает голосом
// владельца там, где владельца уже нет.
func TestO5_FormForATypeTheCatalogNeverHidesIsRefused(t *testing.T) {
	s := naAxes()
	s.HideExistence = servicecontract.Value(map[servicecontract.ObjectType]servicecontract.NotFoundFormat{
		"demo_ghost": "Ghost %s not found",
	})
	refusesAudit(t, s, lawfulServed(), lawfulCatalog(), lawfulMap(), "demo_ghost")
}

// ── О5б: тип, о котором СПРОСЯТ порт, обязан иметь голос владельца ──────────

// probedSpec — дескриптор сервиса, ПРИНЁСШЕГО порт существования. Пока порта
// нет, спрашивать некого и отказ молчит по построению.
// covering — типы, которые объявляет охватом подделка пробы. Пусто у неё быть
// не может: пустой охват — отдельная находка О5в, и подставлять его молча
// значило бы красить соседние оси чужой причиной.
func probedSpec(covering ...string) servicecontract.Spec {
	s := naAxes()
	s.Existence = typedProbe{types: covering}
	return s
}

// mapScopedOn — карта прав, чей единственный метод пообъектен на названном типе.
//
// Имя метода НАСТОЯЩЕЕ: вывод пообъектных типов резолвит запрос через глобальный
// реестр, поэтому синтетическое имя дало бы пустой набор — и проба утверждала бы
// про пустоту, а не про тип.
func mapScopedOn(objectType string) authz.RPCMap {
	return authz.RPCMap{
		probedMethod: {
			Relation: "v_update",
			Extract: authz.StaticExtractor(objectType, func(req any) (string, error) {
				id, _ := req.(string)
				return id, nil
			}),
		},
	}
}

func probedServed() servedSet {
	return servedSet{methods: []servicecontract.MethodFQN{probedMethod}}
}

func probedCatalog() catalogView {
	return catalogView{rows: map[servicecontract.MethodFQN]catalogRow{
		probedMethod: {
			Method: probedMethod,
			Domain: "kacho.cloud.vpc.v1",
		},
	}}
}

// TestO5b_ProbedTypeWithoutAnOwnerVoiceRefusesStart — предмет находки: множество
// типов, о которых спрашивают порт, ШИРЕ того, что осматривает О5.
//
// О5 судит типы, названные КАТАЛОГОМ среди скрывающих. Порт спрашивается о
// пообъектных типах, ВЫВЕДЕННЫХ из карты прав. Тип, попавший во второе множество
// и не попавший в первое, доезжал до сокрытия неосмотренным — и отвечал
// нейтральным «not found», не похожим ни на один ответ владельца.
func TestO5b_ProbedTypeWithoutAnOwnerVoiceRefusesStart(t *testing.T) {
	const absent = "demo_widget"
	if _, ok := authz.OwnerNotFoundFormat(absent); ok {
		t.Fatalf("тип %q завели в таблицу промахов владельцев — проба потеряла предмет", absent)
	}
	msg := refusesAudit(t, probedSpec(absent), probedServed(), probedCatalog(), mapScopedOn(absent),
		absent, "Existence")
	t.Logf("О5б красный: %s", msg)
}

// TestO5b_ProbedTypeWithAnOwnerVoiceIsAccepted — ЗАКОННЫЙ БЛИЗНЕЦ той же формы:
// тот же порт, та же карта, тип с голосом владельца → молчание.
//
// Без него отказ ловил бы форму («порт принесён»), а не существо («за тип нечем
// говорить»), и закрыл бы перевод любому сервису со сверкой существования.
func TestO5b_ProbedTypeWithAnOwnerVoiceIsAccepted(t *testing.T) {
	ot, _ := ownerVoicedType(t)
	c, err := audit(probedSpec(string(ot)), probedServed(), probedCatalog(), mapScopedOn(string(ot)))
	if err != nil {
		t.Fatalf("тип с голосом владельца объявлен находкой: %v", err)
	}
	if c.probed != 1 {
		t.Fatalf("перепись насчитала %d спрашиваемых типов вместо 1 — «находок нет» неотличимо "+
			"от «ничего не осмотрено»: %s", c.probed, c)
	}
}

// TestO5b_SilentWhenNoProbeIsBrought — вторая законная сторона: порта нет →
// спрашивать некого → отказ молчит, каким бы ни был набор типов.
func TestO5b_SilentWhenNoProbeIsBrought(t *testing.T) {
	if _, err := audit(naAxes(), probedServed(), probedCatalog(), mapScopedOn("demo_widget")); err != nil {
		t.Fatalf("отказ сработал у сервиса без порта существования: %v", err)
	}
}

// TestO5b_ProbeWithNothingToAskAboutRefusesStart — ноль целей есть отказ: порт
// принесён, а пообъектных типов из карты не вывелось ни одного.
func TestO5b_ProbeWithNothingToAskAboutRefusesStart(t *testing.T) {
	refusesAudit(t, probedSpec("vpc_network"), lawfulServed(), lawfulCatalog(), lawfulMap(), "Existence")
}

// ── О11: служимые серверные стримы против оси срока жизни подписки ──────────
//
// Четыре ветви, и каждая со своей парой «инъекция + законный близнец»:
// (1) стрим служится, ось не объявлена вовсе; (2) стрим служится, ось объявлена
// неприменимой — истечение заявления; (3) величина объявлена, а стримов нет —
// проводка без предмета; (4) величина объявлена и стрим служится — молчание.

// TestO11_ServedServerStreamWithUndeclaredAxisRefusesStart — ветвь (1). Ось не
// объявлена вовсе: до носителя такой дескриптор доезжает только мимо
// конструктора, но отказ обязан быть способен упасть и здесь — иначе решение
// «что делать со стримом» принимало бы умолчание.
func TestO11_ServedServerStreamWithUndeclaredAxisRefusesStart(t *testing.T) {
	s := naAxes()
	s.StreamBudget = servicecontract.Axis[time.Duration]{}
	msg := refusesAudit(t, s, lawfulServed(), streamingCatalog(), lawfulMap(),
		string(streamMethod), "СЕРВЕРНЫЙ СТРИМ", "StreamBudget")
	t.Logf("О11 ветвь (1) красная: %s", msg)
}

// TestO11_ServedServerStreamExpiresTheNotApplicableClaim — ветвь (2), и она же
// самоистечение изъятия: заявление «серверных стримов не служу» перестаёт быть
// верным в тот момент, когда процесс начинает служить первый. Отказ обязан
// назвать и метод, и саму причину — иначе автор не поймёт, какое из его
// заявлений устарело.
func TestO11_ServedServerStreamExpiresTheNotApplicableClaim(t *testing.T) {
	msg := refusesAudit(t, naAxes(), lawfulServed(), streamingCatalog(), lawfulMap(),
		string(streamMethod), "ИСТЕКЛО", "демо не служит серверных стримов")
	t.Logf("О11 ветвь (2) красная: %s", msg)
}

// TestO11_DeclaredValueWithoutAnyServedStreamRefusesStart — ветвь (3),
// ПОЛОЖИТЕЛЬНЫЙ БЛИЗНЕЦ оси. Без неё ось ловила бы форму («автор что-то
// объявил»), а не предмет: величина пережила бы снятие последней подписки и
// продолжала бы утверждать решение про стримы там, где стримов нет.
func TestO11_DeclaredValueWithoutAnyServedStreamRefusesStart(t *testing.T) {
	s := naAxes()
	s.StreamBudget = servicecontract.Value(30 * time.Minute)
	msg := refusesAudit(t, s, lawfulServed(), lawfulCatalog(), lawfulMap(), "StreamBudget", "НЕТ НИ ОДНОГО")
	t.Logf("О11 ветвь (3) красная: %s", msg)
}

// TestO11_DeclaredValueWithAServedStreamIsAccepted — ветвь (4): решение принято
// и предмет у него есть. Это и есть ДВЕРЬ, которой у отказа не было: сервис со
// служимой подпиской обязан иметь способ подняться.
func TestO11_DeclaredValueWithAServedStreamIsAccepted(t *testing.T) {
	s := naAxes()
	s.StreamBudget = servicecontract.Value(30 * time.Minute)
	c, err := audit(s, lawfulServed(), streamingCatalog(), lawfulMap())
	if err != nil {
		t.Fatalf("объявленный срок жизни подписки при служимом стриме отвергнут — двери нет: %v", err)
	}
	if c.streams != 1 {
		t.Fatalf("перепись насчитала %d стримов, а служится один: %s", c.streams, c)
	}
}

// TestO11_UnaryMethodsAreSilent — ЗАКОННЫЙ БЛИЗНЕЦ ветвей (1) и (2): те же
// методы без стрима при объявленном изъятии → молчание. Без него отказ краснел
// бы на каждом сервисе, то есть ловил бы «служимый набор непуст», а не «в нём
// есть подписка».
func TestO11_UnaryMethodsAreSilent(t *testing.T) {
	c, err := audit(naAxes(), lawfulServed(), lawfulCatalog(), lawfulMap())
	if err != nil {
		t.Fatalf("унарный набор объявлен находкой: %v", err)
	}
	if c.streams != 0 {
		t.Fatalf("перепись насчитала %d стримов в унарном наборе: %s", c.streams, c)
	}
	// Перепись обязана НАЗЫВАТЬ ноль, а не подразумевать его: «стримов нет» и
	// «стримы не считали» иначе неразличимы.
	if !strings.Contains(c.String(), "стримов 0") {
		t.Fatalf("перепись не называет числа стримов: %s", c)
	}
}

// TestO11_PlatformStreamIsNotASubject — граница ветвей (1)-(3), названная
// прямо: наблюдение за здоровьем отдаёт стрим и приезжает вместе с сервером, а
// не с доменом. Строк каталога у него нет, поэтому предметом оси он не
// является, и изъятие «стримов не служу» остаётся верным при нём.
//
// Без этой пробы первая же платформенная служба сделала бы отказ красным на
// КАЖДОМ процессе, и его сняли бы целиком.
func TestO11_PlatformStreamIsNotASubject(t *testing.T) {
	sv := lawfulServed()
	sv.methods = append(sv.methods, "/grpc.health.v1.Health/Watch")
	c, err := audit(naAxes(), sv, lawfulCatalog(), lawfulMap())
	if err != nil {
		t.Fatalf("платформенный стрим засчитан предметом оси: %v", err)
	}
	if c.streams != 0 {
		t.Fatalf("перепись засчитала платформенный стрим в число доменных: %s", c)
	}
	if c.exempted != 1 {
		t.Fatalf("платформенная служба не попала в изъятия: %s", c)
	}
}

// ── перепись: «ноль находок» обязано быть отличимо от «ноль прочитанного» ────

// TestAuditRefusesAnEmptyServedSet — ноль служимых методов есть ОТКАЗ, а не
// успех. Раскатка в ноль целей печатала одиннадцать «пропущено» и выходила
// успехом; здесь этот исход невозможен.
func TestAuditRefusesAnEmptyServedSet(t *testing.T) {
	refusesAudit(t, naAxes(), servedSet{}, lawfulCatalog(), lawfulMap(), "ноль")
}

// TestCensusIsAssertedNotAssumed — перепись обязана называть объём осмотренного.
func TestCensusIsAssertedNotAssumed(t *testing.T) {
	c, err := audit(naAxes(), lawfulServed(), lawfulCatalog(), lawfulMap())
	if err != nil {
		t.Fatalf("законный набор отвергнут: %v", err)
	}
	s := c.String()
	for _, want := range []string{"методов", "доменов"} {
		if !strings.Contains(s, want) {
			t.Fatalf("перепись не называет %q: %q", want, s)
		}
	}
}

// ── вывод домена из имени службы (К-2) ──────────────────────────────────────

// TestDomainIsDerivedNotDeclared — домен снимается с имени зарегистрированной
// службы. Разбор НЕ вправе вернуть пустую строку молча: иначе «не разобрали» и
// «у домена ноль строк» стали бы одним и тем же исходом.
func TestDomainIsDerivedNotDeclared(t *testing.T) {
	for method, want := range map[servicecontract.MethodFQN]string{
		"/kacho.cloud.geo.v1.RegionService/Get":       "kacho.cloud.geo.v1",
		"/kacho.cloud.operation.OperationService/Get": "kacho.cloud.operation",
	} {
		got, err := domainOf(method)
		if err != nil {
			t.Fatalf("domainOf(%q) отказал: %v", method, err)
		}
		if got != want {
			t.Fatalf("domainOf(%q) = %q, ждали %q", method, got, want)
		}
	}
	for _, bad := range []servicecontract.MethodFQN{"", "/", "NoSlash", "/NoDot/Get", "/trailing."} {
		if got, err := domainOf(bad); err == nil {
			t.Fatalf("domainOf(%q) молча вернул %q вместо отказа — «не разобрали» стало бы неотличимо от «ноль строк»", bad, got)
		}
	}
}

// stubNarrower — проводка, отличная от нулевого указателя. Её ответы здесь не
// проверяются: О3/О4 требуют НАЛИЧИЕ проводки, а не её правильность (правильность
// — предмет конформанса, и это названо остаточным риском, а не закрыто).
func stubNarrower() servicecontract.ListNarrower {
	return listnarrow.New(nil, listnarrow.Config{})
}

// ── О5в: ОХВАТ пробы против того, о чём её спросят ──────────────────────────
//
// Предмет отдельный от О5б. Там судится, есть ли у пообъектного типа ГОЛОС
// владельца; здесь — умеет ли проба вообще о нём ответить. Тип, прошедший О5б и
// не прошедший О5в, доезжает до сверки, получает ошибку «неизвестный тип», и
// вызывающий отрабатывает fail-closed: отказ в правах там, где соседний тип того
// же сервиса отвечает промахом владельца (задача продукта #1931).
//
// Четыре ветви, каждая своим входом: охват покрывает всё (молчание) · охват уже
// карты (находка по имени непокрытого) · охват шире карты (находка по имени
// лишнего) · охват пуст (находка об охвате, без поимённого перечня поверх неё).

// mapScopedOnTwo — карта прав с ДВУМЯ пообъектными методами, каждый на своём
// типе. Нужна ровно затем, чтобы инъекция роняла ТОЛЬКО проверяемое: при одном
// типе «охват уже карты» неотличим от «охват пуст», и обе половины О5в
// краснели бы одним входом.
//
// Имена методов НАСТОЯЩИЕ: вывод пообъектных типов резолвит запрос через
// глобальный реестр, поэтому синтетическое имя дало бы пустой набор.
func mapScopedOnTwo(first, second string) authz.RPCMap {
	return authz.RPCMap{
		probedMethod: {
			Relation: "v_update",
			Extract: authz.StaticExtractor(first, func(req any) (string, error) {
				id, _ := req.(string)
				return id, nil
			}),
		},
		probedMethodTwo: {
			Relation: "v_update",
			Extract: authz.StaticExtractor(second, func(req any) (string, error) {
				id, _ := req.(string)
				return id, nil
			}),
		},
	}
}

func twoScopedServed() servedSet {
	return servedSet{methods: []servicecontract.MethodFQN{
		probedMethod,
		probedMethodTwo,
	}}
}

func twoScopedCatalog() catalogView {
	return catalogView{rows: map[servicecontract.MethodFQN]catalogRow{
		probedMethod: {
			Method: probedMethod,
			Domain: "kacho.cloud.vpc.v1",
		},
		probedMethodTwo: {
			Method: probedMethodTwo,
			Domain: "kacho.cloud.vpc.v1",
		},
	}}
}

// twoOwnerVoicedTypes — два ДЕЙСТВУЮЩИХ типа с голосом владельца. Оба обязаны
// его иметь: иначе инъекция О5в роняла бы заодно О5б, и красное приходило бы от
// соседа.
func twoOwnerVoicedTypes(t *testing.T) (string, string) {
	t.Helper()
	const a, b = "vpc_network", "vpc_subnet"
	for _, ot := range []string{a, b} {
		if _, ok := authz.OwnerNotFoundFormat(ot); !ok {
			t.Fatalf("тип %q выбыл из таблицы промахов владельцев — инъекция О5в уронила бы О5б, "+
				"и красное пришло бы от соседней оси", ot)
		}
	}
	return a, b
}

// TestO5v_ControlBothCoveredIsAccepted — КОНТРОЛЬ: всё цело, молчат обе половины
// О5в. Без него отказ ниже ловил бы форму («порт принесён»), а не существо.
func TestO5v_ControlBothCoveredIsAccepted(t *testing.T) {
	a, b := twoOwnerVoicedTypes(t)
	c, err := audit(probedSpec(a, b), twoScopedServed(), twoScopedCatalog(), mapScopedOnTwo(a, b))
	if err != nil {
		t.Fatalf("полный охват объявлен находкой: %v", err)
	}
	if c.probed != 2 || c.covered != 2 {
		t.Fatalf("перепись не сошлась: спрашиваемых %d, охваченных %d — обе величины обязаны быть "+
			"напечатаны, иначе «охват шире» и «охват уже» неразличимы", c.probed, c.covered)
	}
	t.Logf("О5в контроль: %s", c)
}

// TestO5v_TypeOutsideTheProbeCoverageRefusesStart — инъекция НОВОГО свойства:
// охват уже карты. Красное обязано назвать непокрытый тип поимённо — иначе по
// находке нечего чинить.
func TestO5v_TypeOutsideTheProbeCoverageRefusesStart(t *testing.T) {
	a, b := twoOwnerVoicedTypes(t)
	msg := refusesAudit(t, probedSpec(a), twoScopedServed(), twoScopedCatalog(),
		mapScopedOnTwo(a, b), b, "Existence")
	if strings.Contains(msg, "Existence "+a) {
		t.Fatalf("отказ обвинил покрытый тип %q — инъекция уронила не только проверяемое:\n%s", a, msg)
	}
}

// TestO5v_CoverageWithNothingToCoverRefusesStart — ЗЕРКАЛЬНАЯ половина: запись
// охвата, которой больше нечего покрывать. Без неё запись пережила бы снятие
// своего типа молча.
func TestO5v_CoverageWithNothingToCoverRefusesStart(t *testing.T) {
	a, b := twoOwnerVoicedTypes(t)
	const ghost = "vpc_totally_retired"
	msg := refusesAudit(t, probedSpec(a, b, ghost), twoScopedServed(), twoScopedCatalog(),
		mapScopedOnTwo(a, b), ghost, "Existence")
	if strings.Contains(msg, "Existence "+a) || strings.Contains(msg, "Existence "+b) {
		t.Fatalf("отказ обвинил живые типы — зеркальная половина уронила не только проверяемое:\n%s", msg)
	}
}

// TestO5v_EmptyCoverageRefusesStart — пустой охват означает «ни о чём», а не
// «обо всём»: тот же класс, что пустой круг доверенных отправителей.
//
// Поимённого перечня поверх него быть не должно — второе обвинение об одном
// предмете посылало бы чинить каждый тип по отдельности там, где не объявлен
// охват целиком.
func TestO5v_EmptyCoverageRefusesStart(t *testing.T) {
	a, b := twoOwnerVoicedTypes(t)
	msg := refusesAudit(t, probedSpec(), twoScopedServed(), twoScopedCatalog(),
		mapScopedOnTwo(a, b), "ОХВАТ не объявлен")
	if strings.Contains(msg, "Existence "+a) || strings.Contains(msg, "Existence "+b) {
		t.Fatalf("поверх отказа об охвате встал поимённый перечень — два обвинения об одном "+
			"предмете:\n%s", msg)
	}
}

// TestO5v_InjectingTheExistingAxisRedsOnlyIt — ТРЕТИЙ прогон пары: инъекция
// СУЩЕСТВУЮЩЕГО свойства (тип без голоса владельца, О5б) при целом охвате.
// Краснеет только прежняя ось; без этого прогона её молчание в двух проверках
// выше неотличимо от её смерти.
func TestO5v_InjectingTheExistingAxisRedsOnlyIt(t *testing.T) {
	a, _ := twoOwnerVoicedTypes(t)
	const voiceless = "vpc_totally_invented"
	if _, ok := authz.OwnerNotFoundFormat(voiceless); ok {
		t.Fatalf("тип %q завели в таблицу промахов владельцев — инъекция потеряла предмет", voiceless)
	}
	msg := refusesAudit(t, probedSpec(a, voiceless), twoScopedServed(), twoScopedCatalog(),
		mapScopedOnTwo(a, voiceless), voiceless, "голоса владельца")
	if strings.Contains(msg, "в охват\nпробы он не входит") || strings.Contains(msg, "охват\nпробы") {
		t.Fatalf("красное пришло от новой оси, а не от прежней:\n%s", msg)
	}
}
