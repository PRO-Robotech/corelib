// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

// admission_test.go — потолок темпа доезжает до ОБОИХ слушателей носителя.
//
// # Что здесь утверждается, а что держат соседи
//
// Поведение самого механизма на проводе (отказ сверх темпа, покрытие каждого
// метода дескриптора, удержание слота на время обработчика) уже доказано в
// `pkg/grpcsrv/admission_wire_test.go` и здесь не переписывается — второе место
// об одном предмете разъехалось бы молча.
//
// Предмет ЗДЕСЬ — проводка: величины доезжают неизменёнными, ключи у слушателей
// разные, регистрация идёт ЧЕРЕЗ обёртку у ОБОИХ, а объявленное изъятие даёт
// ноль ограничителей, а не пустышку. Каждая проба — пара: отрицание и законный
// близнец той же формы.
//
// Прогон через саму [Serve] здесь невозможен и это названо честно: она проходит
// отказы старта, которым нужен служимый набор реальных доменов, и синтетическая
// служба до них не доживает. Поэтому проба берёт ту же функцию раздачи
// ([admission.handOut]), которую зовёт [Serve], — и она у неё единственный
// вызывающий.

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// admissionSpec — дескриптор с объявленной осью. Величины КРОШЕЧНЫЕ намеренно:
// предмет — исход, а не числа посадки.
func admissionSpec(axis servicecontract.Axis[servicecontract.Admission]) servicecontract.Spec {
	s := acceptableSpec()
	s.Admission = axis
	return s
}

func tinyPair() servicecontract.Admission {
	return servicecontract.Admission{
		Public:   grpcsrv.AdmissionLimits{ReadPerSec: 1, MutationPerSec: 1, BurstFactor: 2, InFlight: 2},
		Internal: grpcsrv.AdmissionLimits{ReadPerSec: 3, MutationPerSec: 3, BurstFactor: 2, InFlight: 4},
	}
}

// TestCarrierArmsBothListenersWithTheDeclaredLimits — величины доезжают
// НЕИЗМЕНЁННЫМИ и на оба слушателя.
//
// Утверждается равенство, а не «ограничитель собрался»: набор, доехавший
// наполовину (публичные величины на оба слушателя), выглядит взведённым и душит
// наш собственный поток намерения — класс заклинивания головы очереди.
func TestCarrierArmsBothListenersWithTheDeclaredLimits(t *testing.T) {
	adm, err := buildAdmission(admissionSpec(servicecontract.Value(tinyPair())))
	if err != nil {
		t.Fatalf("объявленная ось не собралась: %v", err)
	}
	if adm.public == nil || adm.internal == nil {
		t.Fatalf("ограничитель собран не на обоих слушателях: public=%v internal=%v",
			adm.public != nil, adm.internal != nil)
	}
	if got, want := adm.public.Limits(), tinyPair().Public; got != want {
		t.Fatalf("величины публичного слушателя изменились по дороге: %s, объявлено %s", got, want)
	}
	if got, want := adm.internal.Limits(), tinyPair().Internal; got != want {
		t.Fatalf("величины внутреннего слушателя изменились по дороге: %s, объявлено %s", got, want)
	}
	// Имя листенера — не косметика: по нему счётчик отвергнутых отличает
	// публичный поток от внутреннего, и без него «ноль отказов» неразличимо.
	if adm.public.Listener() == adm.internal.Listener() {
		t.Fatalf("оба ограничителя названы %q — счётчику нечем различить потоки",
			adm.public.Listener())
	}
}

// TestCarrierLeavesListenersUnguardedOnADeclaredExemption — законный близнец:
// объявленное изъятие даёт НОЛЬ ограничителей и регистратор БЕЗ обёртки.
//
// Тождество регистратора утверждается прямо: обёртка-пропускалка выглядела бы в
// трассировке так же, как настоящая, и «потолка нет» стало бы неотличимо от
// «потолок ничего не отверг».
func TestCarrierLeavesListenersUnguardedOnADeclaredExemption(t *testing.T) {
	spec := admissionSpec(servicecontract.NotApplicable[servicecontract.Admission](
		"фикстура в процессе: слушатели наружу не выставлены"))
	adm, err := buildAdmission(spec)
	if err != nil {
		t.Fatalf("изъятие не собралось: %v", err)
	}
	if adm.public != nil || adm.internal != nil {
		t.Fatal("на объявленном изъятии собран ограничитель — процесс отчитался бы взведённым")
	}

	pub, in := &capturingRegistrar{}, &capturingRegistrar{}
	var gotPub, gotIn grpc.ServiceRegistrar
	adm.handOut(pub, in,
		func(r grpc.ServiceRegistrar) { gotPub = r },
		func(r grpc.ServiceRegistrar) { gotIn = r })
	if gotPub != grpc.ServiceRegistrar(pub) || gotIn != grpc.ServiceRegistrar(in) {
		t.Fatal("на изъятии регистратор всё равно обёрнут — обёртка без предмета неотличима от настоящей")
	}
}

// TestCarrierHandsOutEveryListenerThroughTheLimiter — ОБА регистратора получают
// обёрнутый регистратор, а не сервер.
//
// Инъекция настоящим входом: потерять обёртку у ОДНОГО слушателя — самый тихий
// исход правки, потому что процесс поднимается и пишет в журнал «ограничитель
// взведён» про вторую половину.
func TestCarrierHandsOutEveryListenerThroughTheLimiter(t *testing.T) {
	adm, err := buildAdmission(admissionSpec(servicecontract.Value(tinyPair())))
	if err != nil {
		t.Fatalf("объявленная ось не собралась: %v", err)
	}
	pub, in := &capturingRegistrar{}, &capturingRegistrar{}
	var gotPub, gotIn grpc.ServiceRegistrar
	adm.handOut(pub, in,
		func(r grpc.ServiceRegistrar) { gotPub = r },
		func(r grpc.ServiceRegistrar) { gotIn = r })

	if gotPub == grpc.ServiceRegistrar(pub) {
		t.Fatal("публичный регистратор отдан БЕЗ обёртки — слушатель без потолка")
	}
	if gotIn == grpc.ServiceRegistrar(in) {
		t.Fatal("внутренний регистратор отдан БЕЗ обёртки — слушатель без потолка")
	}
}

// TestCarrierRefusesAnUnusableSetInsteadOfDroppingIt — негодный набор даёт
// ОТКАЗ, а не молчаливый ноль.
//
// Дескриптор такой набор уже отвергает, поэтому проба зовёт сборку напрямую:
// вторая линия обязана называть предмет, иначе ошибка сборки превращается в
// тихое снятие защиты.
func TestCarrierRefusesAnUnusableSetInsteadOfDroppingIt(t *testing.T) {
	bad := tinyPair()
	bad.Internal.BurstFactor = 0.5
	spec := admissionSpec(servicecontract.Value(bad))

	adm, err := buildAdmission(spec)
	if err == nil {
		t.Fatalf("негодный набор принят: public=%v internal=%v",
			adm.public != nil, adm.internal != nil)
	}
	if got := err.Error(); !strings.Contains(got, "внутреннего слушателя") {
		t.Fatalf("отказ не называет слушателя, у которого негоден набор: %s", got)
	}
}

// TestGuardedRegistrarRefusesOverTheRate — поведенческая пара на дескрипторе,
// прошедшем через обёртку носителя.
//
// Проба зовёт переписанный обработчик НАПРЯМУЮ, без сети: предмет — что обёртка
// стоит между цепочкой и обработчиком, а не транспорт. Положительный контроль в
// том же случае: второй субъект проходит, то есть отказ выше — это предел, а не
// сломанный обработчик.
func TestGuardedRegistrarRefusesOverTheRate(t *testing.T) {
	adm, err := buildAdmission(admissionSpec(servicecontract.Value(tinyPair())))
	if err != nil {
		t.Fatalf("объявленная ось не собралась: %v", err)
	}
	cap := &capturingRegistrar{}
	guardedBy(adm.public, cap).RegisterService(admissionProbeDesc(), nil)

	if cap.desc == nil || len(cap.desc.Methods) != 1 {
		t.Fatalf("обёртка не отдала дескриптор дальше: %+v", cap.desc)
	}
	call := func(subject string) error {
		ctx := operations.WithPrincipal(context.Background(),
			operations.Principal{Type: "user", ID: subject})
		_, cerr := cap.desc.Methods[0].Handler(nil, ctx,
			func(any) error { return nil }, nil)
		return cerr
	}

	// Всплеск чтений = 1 × 2 = 2.
	if err := call("usr-1"); err != nil {
		t.Fatalf("первое чтение отвергнуто: %v", err)
	}
	if err := call("usr-1"); err != nil {
		t.Fatalf("второе чтение (в пределах всплеска) отвергнуто: %v", err)
	}
	over := call("usr-1")
	if over == nil {
		t.Fatal("третье чтение сверх всплеска допущено — обёртки на пути нет")
	}
	if got := status.Code(over); got != codes.ResourceExhausted {
		t.Fatalf("код отказа = %v, ждали ResourceExhausted", got)
	}

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: другой субъект проходит. Без него отказ выше был
	// бы неотличим от «обработчик сломался после двух вызовов».
	if err := call("usr-2"); err != nil {
		t.Fatalf("другой субъект отвергнут — предел считается не на вызывающего: %v", err)
	}
}

// TestAdmissionReportStopsWithItsContext — фоновая задача не переживает свой
// контекст.
//
// Предмет — не «печатает», а «возвращает управление»: задача, ждущая общий
// контекст, оставила бы [Serve] висеть после падения слушателя, и процесс-зомби
// был бы неотличим от штатной работы.
func TestAdmissionReportStopsWithItsContext(t *testing.T) {
	adm, err := buildAdmission(admissionSpec(servicecontract.Value(tinyPair())))
	if err != nil {
		t.Fatalf("объявленная ось не собралась: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); adm.report(ctx, slog.Default()) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("задача счёта не вернула управление по отмене контекста")
	}
}

// capturingRegistrar — регистратор, запоминающий дескриптор, который до него
// доехал. Настоящего сервера здесь не нужно: предмет — переписанный обработчик.
type capturingRegistrar struct {
	desc *grpc.ServiceDesc
}

func (c *capturingRegistrar) RegisterService(sd *grpc.ServiceDesc, _ any) { c.desc = sd }

// admissionProbeDesc — служба с одним читательским методом. Имя метода начинается
// с `List`, поэтому классификатор конвенции относит его к чтениям, и бюджет
// берётся читательский — тот, что объявлен в паре величин.
func admissionProbeDesc() *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "kacho.cloud.demo.v1.WidgetService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "ListWidgets",
			Handler: func(_ any, ctx context.Context, dec func(any) error, i grpc.UnaryServerInterceptor) (any, error) {
				in := new(wrapperspb.BytesValue)
				if err := dec(in); err != nil {
					return nil, err
				}
				call := func(context.Context, any) (any, error) { return wrapperspb.Bytes(nil), nil }
				if i == nil {
					return call(ctx, in)
				}
				return i(ctx, in, &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.demo.v1.WidgetService/ListWidgets"}, call)
			},
		}},
		Metadata: "kacho/servicehost/probe.proto",
	}
}
