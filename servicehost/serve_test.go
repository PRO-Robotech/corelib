// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// TestServedSetIsTakenFromTheServerNotFromWhatWasSaid — служимый набор снимается
// У СЕРВЕРА, а не с того, что сервис о себе объявил.
//
// Разница несущая и проверяется прямой инъекцией: регистрация, сделанная В
// ОБХОД переданного регистратора — прямо на сервере, — всё равно попадает в
// набор. Пока набор собирался бы записывающей обёрткой, такая регистрация была
// бы ей невидима, и «служить RPC и не отдать его дескриптор» перестало бы быть
// одной операцией.
func TestServedSetIsTakenFromTheServerNotFromWhatWasSaid(t *testing.T) {
	srv := grpcsrv.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "kacho.cloud.demo.v1.WidgetService",
		HandlerType: (*any)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Get"}, {MethodName: "List"}},
	}, nil)

	got := servedOf(srv)
	want := map[servicecontract.MethodFQN]bool{
		"/kacho.cloud.demo.v1.WidgetService/Get":  true,
		"/kacho.cloud.demo.v1.WidgetService/List": true,
	}
	for m := range want {
		if !contains(got.methods, m) {
			t.Fatalf("метод %q служится, но в наборе его нет: %v", m, got.methods)
		}
	}
	// Платформенные регистрации конструктор ставит сам — набор обязан их видеть,
	// иначе изъятие нечего было бы истекать.
	if !contains(got.methods, "/grpc.health.v1.Health/Check") {
		t.Fatalf("здоровье служится, но в наборе его нет: %v", got.methods)
	}
}

// TestPlatformExemptionsAllStillHaveASubject — самоистечение перечня изъятий.
//
// Запись, чью службу конструктор сервера БОЛЬШЕ НЕ регистрирует, — находка:
// иначе изъятие переживает свой предмет и молча наследует следующую слепую
// зону. Предикат снятия здесь внешний — поведение `grpcsrv.NewServer`, — а не
// выведенный из этого же перечня.
func TestPlatformExemptionsAllStillHaveASubject(t *testing.T) {
	srv := grpcsrv.NewServer()
	registered := map[string]bool{}
	for name := range srv.GetServiceInfo() {
		registered[name] = true
	}
	t.Logf("перепись: конструктор сервера зарегистрировал служб %d, изъятий объявлено %d",
		len(registered), len(platformServices))
	if len(registered) == 0 {
		t.Fatal("конструктор не зарегистрировал ни одной службы — предикат снятия ничего не читает")
	}
	for name, p := range platformServices {
		if !registered[name] {
			t.Fatalf("изъятие %q больше не имеет предмета: конструктор сервера эту службу не "+
				"регистрирует. Причина, записанная при заведении: %s. Запись, которой нечего "+
				"исключать, — находка, а не наследство", name, p.why)
		}
	}
	// Зеркально: каждая зарегистрированная НЕ-Kachō служба обязана быть названа.
	// Иначе конструктор однажды добавит четвёртую, и она молча получит проход.
	for name := range registered {
		if strings.HasPrefix(name, "kacho.") {
			continue
		}
		if _, named := platformServices[name]; !named {
			t.Fatalf("конструктор сервера регистрирует %q, и перечень изъятий о ней молчит: "+
				"либо назвать её с причиной, либо она обязана иметь строку каталога", name)
		}
	}
}

// TestChainRefusesBeforeTheDecisionLinkIsInstalled — звено решения о доступе
// ставится ПОСЛЕ того, как отказы старта прошли, поэтому у слота есть момент,
// когда он пуст. Проба даёт этому моменту производителя и требует fail-closed.
//
// Без неё ветка «слот пуст» была бы защитной и недостижимой — то есть тем самым
// мёртвым стражем, который выглядит защитой и не проверяет ничего.
func TestChainRefusesBeforeTheDecisionLinkIsInstalled(t *testing.T) {
	var slot decisionSlot
	unary := slot.unary()

	_, err := unary(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.demo.v1.WidgetService/Get"},
		func(context.Context, any) (any, error) { return "должен был не доехать", nil })
	if err == nil {
		t.Fatal("пустой слот пропустил запрос — решение о доступе не принималось никем")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("код отказа = %v, ждали Unavailable (состояние посадки разрешением не бывает)", got)
	}

	// Законный близнец: слот заполнен → запрос доезжает до обработчика.
	slot.install(authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-demo",
		Map:         authz.RPCMap{"/kacho.cloud.demo.v1.WidgetService/Get": {Public: true}},
		Cache:       authz.NewCache(0),
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			// Освобождённый метод до решателя не доходит; решатель здесь стоит
			// затем, чтобы конструктор звена не отказал, а не чтобы отвечать.
			return false, nil
		}),
	}))
	got, err := unary(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.demo.v1.WidgetService/Get"},
		func(context.Context, any) (any, error) { return "доехал", nil })
	if err != nil {
		t.Fatalf("заполненный слот отверг освобождённый метод: %v", err)
	}
	if got != "доехал" {
		t.Fatalf("обработчик не исполнился: %v", got)
	}
}

// TestRegistrarCannotReachTheServer — форма регистрации.
//
// Регистратор получает `grpc.ServiceRegistrar` — интерфейс с ЕДИНСТВЕННЫМ
// методом. Приделать своё звено не к чему: объекта сервера у вызывающего не
// остаётся. Проба фиксирует это утверждением о ТИПЕ, а не о намерении: если
// когда-нибудь в сигнатуру вернут `*grpc.Server`, она перестанет
// компилироваться.
func TestRegistrarCannotReachTheServer(t *testing.T) {
	var r Registrar = func(reg grpc.ServiceRegistrar) {
		if _, isServer := reg.(*grpc.Server); isServer {
			// Сам по себе факт, что под интерфейсом лежит сервер, дырой не является:
			// достать его можно только приведением типа, а это видно в диффе как
			// приведение типа. Существенно то, что СИГНАТУРА его не отдаёт.
			t.Log("под интерфейсом лежит сервер; отдаётся он всё равно интерфейсом")
		}
	}
	srv := grpcsrv.NewServer()
	r(srv)
}

func contains(xs []servicecontract.MethodFQN, x servicecontract.MethodFQN) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
