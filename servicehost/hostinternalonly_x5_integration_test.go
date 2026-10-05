// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// hostinternalonly_x5_integration_test.go — integration-проба правки Х5
// (NTF-4 §1.1, DoD S3 п.8б; замысел issue-2919 З18): носитель в форме «только
// внутренний слушатель», поднятый ЦЕЛИКОМ через [Serve] на настоящем слушателе
// loopback с mTLS.
//
// Предпосылка — `TestX5Premise_PairCarrierComesUpAndJudgesTheInternalListener`
// (та же фикстура, форма — пара): пока она красная, красное здесь — сломанный
// вопрос.
//
// Что утверждается:
//   - форма поднимает один внутренний слушатель, и цепочка на нём судит так же,
//     как внутренняя половина пары (круг пересылающих, решатель, исход на
//     проводе) — часть носителя NTF4-122;
//   - публичный регистратор в форме — отказ старта, регистратор не зван;
//   - падение внутреннего слушателя — ошибка [Serve], а не нулевой исход;
//   - [PostureOf] выводит форму самоотчёта из дескриптора, переданного в [Serve].
package servicehost

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/observability"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// internalOnlySpec — близнец [x5Fixture.pairSpec], отличающийся ФОРМОЙ: ось
// формы с причиной, и поля публичного слушателя, которых у формы нет, сняты.
// Внутренний адрес, транспорт, круг, решатель — те же.
func (f *x5Fixture) internalOnlySpec(internalAddr string) servicecontract.Spec {
	s := f.pairSpec("", internalAddr)
	s.HostForm = servicecontract.HostInternalOnly
	s.HostFormReason = "проба Х5: публичного входа у процесса нет по построению"
	s.PublicAddr = ""
	s.PublicCreds = nil
	s.Admission = servicecontract.Value(servicecontract.Admission{
		Internal: grpcsrv.PlatformInternalAdmission(),
	})
	return s
}

// startInternalOnly принимает дескриптор формы и поднимает носитель.
func (f *x5Fixture) startInternalOnly(t *testing.T, in string) *running {
	t.Helper()
	d, err := servicecontract.New(f.internalOnlySpec(in))
	if err != nil {
		t.Fatalf("законный дескриптор формы «только внутренний слушатель» отвергнут: %v", err)
	}
	return startServe(t, d, nil, x5Registrar(f.handled))
}

// TestX5_InternalOnlyCarrierJudgesForwardedIdentityOnItsOnlyListener — форма
// поднимается и судит пересланную личность на своём единственном слушателе:
//
//	(а) пир края с личностью администратора: вопросов Check 1, вызовов метода 1, OK;
//	(б) пир M вне круга с той же личностью: PERMISSION_DENIED "permission denied",
//	    вопросов Check 0, вызовов метода 0 — личность снята до звена прав.
//
// (а) и (б) отличаются ровно SAN пира. Близнец по форме — предпосылка (пара).
func TestX5_InternalOnlyCarrierJudgesForwardedIdentityOnItsOnlyListener(t *testing.T) {
	f := newX5Fixture(t)
	in := freeAddr(t)
	r := f.startInternalOnly(t, in)
	if err := awaitListening(t, r, in); err != nil {
		t.Fatalf("носитель формы «только внутренний слушатель» не поднял внутренний слушатель: %v", err)
	}

	if o := f.callAs(t, in, f.pki.edge, x5Ping, false); o.code != codes.OK || f.handled.ping.Load() != 1 {
		t.Fatalf("Ping не дошёл до метода на слушателе формы: %v, вызовов %d", o, f.handled.ping.Load())
	}
	if o := f.callAs(t, in, f.pki.edge, x5Get, true); o.code != codes.OK {
		t.Fatalf("(а) пир края с личностью администратора получил %v", o)
	}
	if c, h := f.check.calls.Load(), f.handled.get.Load(); c != 1 || h != 1 {
		t.Fatalf("(а) вопросов Check %d, вызовов метода %d; ожидалось 1 и 1", c, h)
	}
	o := f.callAs(t, in, f.pki.outside, x5Get, true)
	if o.code != codes.PermissionDenied || o.msg != "permission denied" {
		t.Fatalf("(б) пир вне круга получил %v; ожидался PermissionDenied \"permission denied\"", o)
	}
	if c, h := f.check.calls.Load(), f.handled.get.Load(); c != 1 || h != 1 {
		t.Fatalf("(б) пересланная личность не снята до звена прав: вопросов Check %d, вызовов метода %d "+
			"(ожидались прежние 1 и 1)", c, h)
	}
}

// TestBothListenersRefuseIdenticallyOnTheWire_HostInternalOnly — расширение
// `TestBothListenersRefuseIdenticallyOnTheWire` на форму Х5: внутренний сервер
// формы и внутренняя половина пары, поднятые [Serve] с одной фикстурой, дают
// вызывающему ОДИН И ТОТ ЖЕ исход на каждом входе.
//
// Инъекция, на которой проба обязана краснеть: форма собирает цепочку без звена
// прав — тогда `Get` без личности и `Get` пира вне круга доходят до метода
// (`OK`) на форме и расходятся с парой. Законный близнец внутри пробы — `Ping`
// (изъятие): на обоих доходит до метода, иначе «оба отказали» зеленело бы на
// всём сломанном.
func TestBothListenersRefuseIdenticallyOnTheWire_HostInternalOnly(t *testing.T) {
	pairF, formF := newX5Fixture(t), newX5Fixture(t)

	pairIn := freeAddr(t)
	pd, err := servicecontract.New(pairF.pairSpec(freeAddr(t), pairIn))
	if err != nil {
		t.Fatalf("фикстура: дескриптор пары отвергнут: %v", err)
	}
	pr := startServe(t, pd, noRegistrar(), x5Registrar(pairF.handled))
	if err := awaitListening(t, pr, pairIn); err != nil {
		t.Fatalf("фикстура: пара не поднялась: %v", err)
	}

	formIn := freeAddr(t)
	fr := formF.startInternalOnly(t, formIn)
	if err := awaitListening(t, fr, formIn); err != nil {
		t.Fatalf("форма «только внутренний слушатель» не поднялась: %v", err)
	}

	for _, c := range []struct {
		name    string
		method  string
		outside bool
		headers bool
		want    codes.Code
	}{
		{"Ping — изъятие, близнец", x5Ping, false, false, codes.OK},
		{"Get без личности", x5Get, false, false, codes.PermissionDenied},
		{"Get пира вне круга с личностью администратора", x5Get, true, true, codes.PermissionDenied},
	} {
		call := func(f *x5Fixture, addr string) outcome {
			peer := f.pki.edge
			if c.outside {
				peer = f.pki.outside
			}
			return f.callAs(t, addr, peer, c.method, c.headers)
		}
		pair, form := call(pairF, pairIn), call(formF, formIn)
		if pair != form {
			t.Fatalf("%s: внутренняя половина пары ответила %v, внутренний сервер формы — %v. "+
				"«internal = доверенный» — запрещённое допущение: цепочка у формы обязана быть той же", c.name, pair, form)
		}
		if form.code != c.want {
			t.Fatalf("%s: оба ответили %v, ожидалось %v", c.name, form, c.want)
		}
	}
	if h := formF.handled.get.Load(); h != 0 {
		t.Fatalf("на форме Get без права дошёл до метода %d раз(а)", h)
	}
}

// TestX5_InternalOnlyRefusesAPublicRegistrar — публичный регистратор в форме, где
// публичного слушателя нет: отказ старта, регистратор не зван, слушатель не
// поднят. Дельта к близнецу (TestX5_InternalOnlyCarrierJudges…) одна —
// публичный регистратор не nil.
func TestX5_InternalOnlyRefusesAPublicRegistrar(t *testing.T) {
	f := newX5Fixture(t)
	in := freeAddr(t)
	d, err := servicecontract.New(f.internalOnlySpec(in))
	if err != nil {
		t.Fatalf("законный дескриптор формы отвергнут: %v", err)
	}
	var publicCalls int
	public := func(grpc.ServiceRegistrar) { publicCalls++ }
	r := startServe(t, d, public, x5Registrar(f.handled))
	select {
	case serr := <-r.done:
		r.done <- serr
		if serr == nil {
			t.Fatal("носитель формы принял публичный регистратор и вернул nil")
		}
		if publicCalls != 0 {
			t.Fatalf("публичный регистратор зван %d раз(а) в форме без публичного слушателя", publicCalls)
		}
		t.Logf("красный: %v", serr)
	case <-time.After(10 * time.Second):
		t.Fatal("носитель формы с публичным регистратором поднялся и служит — отказа старта не было")
	}
}

// TestX5_InternalListenerFailureIsTheServeOutcome — адрес внутреннего слушателя
// занят: [Serve] возвращает ошибку, процесс не завершился бы кодом 0. Близнец —
// тот же дескриптор на свободном адресе поднимается (TestX5_InternalOnlyCarrierJudges…).
func TestX5_InternalListenerFailureIsTheServeOutcome(t *testing.T) {
	f := newX5Fixture(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("фикстура: занять адрес: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	in := occupied.Addr().String()

	r := f.startInternalOnly(t, in)
	select {
	case serr := <-r.done:
		r.done <- serr
		if serr == nil {
			t.Fatal("внутренний слушатель формы не поднялся, а Serve вернул nil — процесс завершился бы кодом 0")
		}
		if !strings.Contains(serr.Error(), in) {
			t.Fatalf("исход Serve не называет адрес слушателя %s:\n%v", in, serr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve не вернул управление при занятом адресе единственного слушателя")
	}
}

// TestX5_PostureOfDerivesTheListenerFormFromTheDescriptor — форма самоотчёта
// выводится ОДНОЙ функцией из дескриптора, который передаётся в [Serve]:
// пара → (pair, ложь); без gRPC-слушателей → (pair, истина); только внутренний →
// (internal_only, ложь). Запись, собранная из её значений, печатает
// `listener_form` = pair | none | internal_only.
func TestX5_PostureOfDerivesTheListenerFormFromTheDescriptor(t *testing.T) {
	f := newX5Fixture(t)
	pair, err := servicecontract.New(f.pairSpec(freeAddr(t), freeAddr(t)))
	if err != nil {
		t.Fatalf("фикстура: дескриптор пары отвергнут: %v", err)
	}
	noGRPC, err := servicecontract.New(servicecontract.Spec{
		Service:     "kacho-x5probe-sender",
		Mode:        servicecontract.ModeDev,
		HostForm:    servicecontract.HostNoGRPC,
		Forwarders:  servicecontract.NotApplicable[grpcsrv.TrustedForwarders]("gRPC-слушателей нет"),
		TrustDomain: servicecontract.NotApplicable[grpcsrv.TrustDomain]("gRPC-слушателей нет"),
		DBSSLMode:   servicecontract.Value("require"),
	})
	if err != nil {
		t.Fatalf("фикстура: дескриптор без gRPC-слушателей отвергнут: %v", err)
	}
	internalOnly, err := servicecontract.New(f.internalOnlySpec(freeAddr(t)))
	if err != nil {
		t.Fatalf("законный дескриптор формы «только внутренний слушатель» отвергнут: %v", err)
	}

	for _, c := range []struct {
		name     string
		d        servicecontract.Descriptor
		wantForm observability.ListenerForm
		wantNone bool
		wantKey  string
	}{
		{"пара", pair, observability.ListenerFormPair, false, "pair"},
		{"без gRPC-слушателей", noGRPC, observability.ListenerFormPair, true, "none"},
		{"только внутренний", internalOnly, observability.ListenerFormInternalOnly, false, "internal_only"},
	} {
		t.Run(c.name, func(t *testing.T) {
			form, none := PostureOf(&c.d)
			if form != c.wantForm || none != c.wantNone {
				t.Fatalf("PostureOf = (%v, %v), ожидалось (%v, %v)", form, none, c.wantForm, c.wantNone)
			}
			rec, rerr := observability.NewBootPosture(observability.BootPosture{
				Service: "kacho-x5probe", ListenerForm: form, NoServedServices: none,
			})
			if rerr != nil {
				t.Fatalf("запись из значений PostureOf отвергнута конструктором записи: %v", rerr)
			}
			var buf bytes.Buffer
			observability.LogBootPosture(observability.NewSlogger(&buf), rec)
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("строка самоотчёта не JSON: %v", err)
			}
			if got := line["listener_form"]; got != c.wantKey {
				t.Fatalf("listener_form = %v, ожидалось %q", got, c.wantKey)
			}
		})
	}
}
