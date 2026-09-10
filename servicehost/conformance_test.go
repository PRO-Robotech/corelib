// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// conformance_test.go — свойства КОНТУРА, которые до носителя каждый сервис
// держал у себя своей пробой.
//
// # Почему эти пробы переехали, а не были удалены
//
// Композиционный корень первого переведённого сервиса нёс 1176 строк проб, и их
// предмет исчезает вместе с корнем: собственной сборки цепочки у сервиса больше
// нет. Но СВОЙСТВА, которые они утверждали, никуда не делись — они стали
// свойствами носителя, то есть общими на все семь. Удалить пробу вместе с её
// предметом можно только назвав, что именно она держала и где это держится
// теперь; здесь — то самое «теперь».
//
// Перенесены: порядок звеньев (восстановление паники внешним, личность до
// решения о доступе), идентичность цепочек обоих слушателей, сведение исхода
// процесса из ошибок двух слушателей. Остались у сервиса: разделение
// public/internal при регистрации (свойство его домена, а не контура),
// самоотчёт о посадке и не-gRPC диагностический слушатель.
package servicehost

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// chainSpec — дескриптор, от которого отталкиваются пробы контура. Срок жизни
// подписки объявлен ВЕЛИЧИНОЙ и намеренно отличается от границы обработки: обе
// полосы обязаны брать СВОЁ число, и проба, где они совпадают, этого не
// различила бы.
func chainSpec() servicecontract.Spec {
	return servicecontract.Spec{
		Service:         "kacho-demo",
		Logger:          slog.Default(),
		Forwarders:      servicecontract.Value(grpcsrv.NewTrustedForwarders("spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway")),
		TrustDomain:     servicecontract.Value(grpcsrv.NewTrustDomain("kacho.cloud")),
		TrustDomainKnob: "KACHO_DEMO_AUTHZ_TRUST_DOMAIN",
		HandlingBudget:  30 * time.Second,
		StreamBudget:    servicecontract.Value(30 * time.Minute),
	}
}

// TestLatencyIsOutermostAccessLogNextAndDecisionIsLast — порядок звеньев.
//
// Измеритель задержки обязан быть ПЕРВЫМ (самым внешним): он накрывает всё, что
// процесс делает ради вызова, включая отказ, произведённый любым звеном ниже.
// Стоя внутри решения о доступе, он оставил бы неизмеренным каждый отказ по
// правам — исход, ради которого в разбор происшествия и приходят.
//
// Журнал доступа — ВТОРЫМ: он записывает исход любого вызова, включая
// паниковавший. Стоя внутри восстановления паники, он пропускал бы ровно тот
// исход, который тяжелее всех.
//
// Восстановление паники — ТРЕТЬИМ: оно оборачивает всё нижележащее, включая сами
// звенья. Иначе паника В ЗВЕНЕ уронит процесс, и дефект на пути запроса одного
// тенанта прекратит обслуживание всех.
//
// Решение о доступе обязано быть ПОСЛЕДНИМ: оно читает уже извлечённого
// субъекта, а решение по ещё не извлечённой личности решением не является.
func TestLatencyIsOutermostAccessLogNextAndDecisionIsLast(t *testing.T) {
	var slot decisionSlot
	lat := probeLatency(t)
	unary := unaryChain(chainSpec(), &slot, lat, nil, grpcsrv.ListenerPublic)
	stream := streamChain(chainSpec(), &slot, lat, nil, grpcsrv.ListenerPublic)

	// Дескриптор пробы гейта мутаций не несёт (ось не объявлена), поэтому его
	// звена в цепочке нет — отсюда семь, а не восемь. Длина утверждается вместе
	// с причиной: правка длины обязана быть правкой контура, а не побочным
	// следствием чужого изменения.
	if len(unary) != 7 || len(stream) != 7 {
		t.Fatalf("цепочка изменила длину: unary %d, stream %d. Это не косметика — "+
			"позиции звеньев несут причину каждая, и правка длины обязана быть правкой контура",
			len(unary), len(stream))
	}
	// Звенья распознаются ПО СУЩЕСТВУ (то же значение, что отдаёт конструктор),
	// а не по имени переменной.
	if got, want := funcID(unary[0]), funcID(lat.UnaryServerInterceptor(grpcsrv.ListenerPublic)); got != want {
		t.Fatalf("первым unary-звеном стоит %s, а обязан — измеритель задержки (%s): "+
			"внутри решения о доступе он не измерил бы ни одного отказа по правам", got, want)
	}
	if got, want := funcID(stream[0]), funcID(lat.StreamServerInterceptor(grpcsrv.ListenerPublic)); got != want {
		t.Fatalf("первым stream-звеном стоит %s, а обязан — измеритель задержки (%s)", got, want)
	}
	if got, want := funcID(unary[1]), funcID(accessLogUnary(chainSpec().Logger)); got != want {
		t.Fatalf("вторым unary-звеном стоит %s, а обязан — журнал доступа (%s): "+
			"внутри восстановления паники он не увидел бы паниковавший вызов", got, want)
	}
	if got, want := funcID(stream[1]), funcID(accessLogStream(chainSpec().Logger)); got != want {
		t.Fatalf("вторым stream-звеном стоит %s, а обязан — журнал доступа (%s)", got, want)
	}
	wantUnary := funcID(grpcsrv.UnaryPanicRecovery(chainSpec().Logger))
	if got := funcID(unary[2]); got != wantUnary {
		t.Fatalf("третьим unary-звеном стоит %s, а обязано — восстановление паники (%s)", got, wantUnary)
	}
	wantStream := funcID(grpcsrv.StreamPanicRecovery(chainSpec().Logger))
	if got := funcID(stream[2]); got != wantStream {
		t.Fatalf("третьим stream-звеном стоит %s, а обязано — восстановление паники (%s)", got, wantStream)
	}
	// Верхняя граница обработки — сразу за восстановлением: срок обязан накрывать
	// и вопрос о доступе, и запросы к своей БД.
	if got, want := funcID(unary[3]), funcID(handlingBudgetUnary(time.Second)); got != want {
		t.Fatalf("четвёртым unary-звеном стоит %s, а обязана — верхняя граница обработки (%s)", got, want)
	}
	// На стриме третьим стоит звено СРОКА ЖИЗНИ ПОДПИСКИ — другое звено и другая
	// величина. Совпадение имён («граница») здесь обманчиво: у запроса и у
	// подписки разные предметы, и подмена одного другим рвала бы подписку по
	// потолку одиночного вызова.
	if got, want := funcID(stream[3]), funcID(streamBudgetLink(time.Second)); got != want {
		t.Fatalf("четвёртым stream-звеном стоит %s, а обязан — срок жизни подписки (%s)", got, want)
	}
	// Последним — слот решения о доступе. Проверяем ПОВЕДЕНИЕМ, а не именем:
	// пустой слот отказывает, и это его единственная наблюдаемая примета.
	_, err := unary[len(unary)-1](context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.demo.v1.WidgetService/Get"},
		func(context.Context, any) (any, error) { return nil, nil })
	if err == nil {
		t.Fatal("последнее unary-звено не является слотом решения о доступе: пустой слот обязан отказать")
	}
}

// TestStreamChainDropsTheBudgetLinkOnANotApplicableAxis — вторая половина оси
// на уровне ЦЕПОЧКИ: изъятие «серверных стримов не служу» снимает звено срока
// целиком, а не подменяет его границей обработки запроса.
//
// Пара «инъекция + законный близнец» здесь в одном теле: та же цепочка с
// объявленной величиной звено несёт (проверено выше и повторено сравнением
// длин), с изъятием — нет. Без этой пробы изъятие могло бы молча означать
// «взять унарную величину», то есть ровно тот разрыв, ради которого ось и
// заведена.
func TestStreamChainDropsTheBudgetLinkOnANotApplicableAxis(t *testing.T) {
	var slot decisionSlot
	lat := probeLatency(t)
	withValue := streamChain(chainSpec(), &slot, lat, nil, grpcsrv.ListenerPublic)

	na := chainSpec()
	na.StreamBudget = servicecontract.NotApplicable[time.Duration]("демо не служит серверных стримов")
	withoutValue := streamChain(na, &slot, lat, nil, grpcsrv.ListenerPublic)

	if len(withoutValue) != len(withValue)-1 {
		t.Fatalf("изъятие не сняло звена срока: с величиной %d звеньев, с изъятием %d",
			len(withValue), len(withoutValue))
	}
	for _, link := range withoutValue {
		if funcID(link) == funcID(streamBudgetLink(time.Second)) {
			t.Fatal("звено срока жизни подписки осталось в цепочке при объявленном изъятии — " +
				"значит изъятие означает не «сроком не накрываем», а что-то другое")
		}
	}
	// Порядок остальных звеньев изъятием не меняется: измеритель остаётся
	// внешним, журнал — вторым, восстановление паники — третьим.
	if got, want := funcID(withoutValue[0]), funcID(lat.StreamServerInterceptor(grpcsrv.ListenerPublic)); got != want {
		t.Fatalf("снятие звена срока переставило измеритель задержки: первым стоит %s", got)
	}
	if got, want := funcID(withoutValue[1]), funcID(accessLogStream(chainSpec().Logger)); got != want {
		t.Fatalf("снятие звена срока переставило журнал доступа: вторым стоит %s", got)
	}
	if got, want := funcID(withoutValue[2]), funcID(grpcsrv.StreamPanicRecovery(chainSpec().Logger)); got != want {
		t.Fatalf("снятие звена срока переставило восстановление паники: третьим стоит %s", got)
	}
}

// TestChainBuilderIsDeterministic — строитель цепочки на одном дескрипторе даёт
// одно и то же.
//
// # Чего эта проба НЕ утверждает, и почему это сказано в заголовке
//
// Прежняя её редакция называлась «оба слушателя получают одну и ту же цепочку», а
// в теле сравнивала `unaryChain(spec,&slot)` с ним же. Это детерминизм строителя,
// и только он: правка `Serve`, освобождающая внутренний слушатель от звена,
// оставляла пробу зелёной, потому что тела `Serve` она не касалась вовсе.
// Заголовок был шире тела — и ровно на ту величину, ради которой проба писалась.
//
// Свойство «обоим слушателям подаётся одно» теперь держится ДВУМЯ вещами, и обе
// вне этой пробы: построением (`serverPair` строит цепочку один раз и передаёт её
// обоим) и наблюдаемой проверкой `TestBothListenersRefuseIdenticallyOnTheWire`,
// которая поднимает оба сервера и сверяет то, что видит вызывающий.
//
// Детерминизм при этом остаётся нужным сам по себе: без него сравнение чего бы то
// ни было со строителем ничего не значит.
func TestChainBuilderIsDeterministic(t *testing.T) {
	var slot decisionSlot
	spec := chainSpec()
	first, second := unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic), unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic)
	if len(first) != len(second) {
		t.Fatalf("строитель дал цепочки разной длины: %d против %d", len(first), len(second))
	}
	for i := range first {
		if funcID(first[i]) != funcID(second[i]) {
			t.Fatalf("звено %d различается между двумя сборками одного дескриптора: %s против %s",
				i, funcID(first[i]), funcID(second[i]))
		}
	}
}

// TestForwarderCircleReachesTheChain — круг отправителей ДОЕЗЖАЕТ до звена
// цепочки, которую строит носитель.
//
// Проба нужна потому, что круг легко «объявить и не провязать»: дескриптор его
// несёт, отказ старта его проверяет, а до транспорта он не доходит — и тогда
// переданную личность принимает любой проверенный пир. Здесь фиксируется, что
// значение действительно уезжает в пару звеньев извлечения личности.
//
// Про ОБА слушателя проба не утверждает ничего, и прежний её заголовок это
// обещал напрасно: тело строит одну цепочку. Что цепочка у слушателей общая —
// предмет `serverPair` и `TestBothListenersRefuseIdenticallyOnTheWire`.
func TestForwarderCircleReachesTheChain(t *testing.T) {
	spec := chainSpec()
	circle, _ := spec.Forwarders.Get()
	pair := grpcsrv.PrincipalExtractUnary(grpcsrv.NewTrustDomain("kacho.cloud"), circle)
	if len(pair) != 2 {
		t.Fatalf("пара звеньев личности изменила состав: %d", len(pair))
	}
	var slot decisionSlot
	chain := unaryChain(spec, &slot, probeLatency(t), nil, grpcsrv.ListenerPublic)
	// Пара извлечения личности стоит НЕПОСРЕДСТВЕННО перед решением о доступе —
	// её позиция отсчитывается от хвоста, а не от головы: иначе проба ломалась бы
	// при появлении любого звена выше и краснела бы на исправном контуре.
	at := len(chain) - 1 - len(pair)
	for i, want := range pair {
		if funcID(chain[at+i]) != funcID(want) {
			t.Fatalf("звено %d цепочки не совпадает со звеном пары извлечения личности", at+i)
		}
	}
}

// TestServeResultSurfacesInternalListenerFailure — исход процесса.
//
// Крах ВНУТРЕННЕГО слушателя обязан дать ненулевой исход. Если считать исход
// только по публичной ошибке, отмена контекста погасит публичный слушатель, тот
// по контракту вернёт nil, и процесс завершится успехом: оркестратор не
// перезапустит его, а вся админ-плоскость тихо станет недоступной.
func TestServeResultSurfacesInternalListenerFailure(t *testing.T) {
	boom := errors.New("внутренний слушатель упал")
	if got := serveResult(nil, boom); !errors.Is(got, boom) {
		t.Fatalf("крах внутреннего слушателя дал исход %v — процесс завершился бы успехом", got)
	}
	pub := errors.New("публичный слушатель упал")
	if got := serveResult(pub, boom); !errors.Is(got, pub) {
		t.Fatalf("исход = %v, ждали ошибку публичного слушателя (первичный сигнал отказа)", got)
	}
	if got := serveResult(nil, nil); got != nil {
		t.Fatalf("штатное завершение дало ошибку %v", got)
	}
}

// funcID — ИМЯ КОНСТРУКТОРА, породившего это значение-функцию.
//
// # Почему не полное имя символа
//
// Замыкание в таблице символов называется по МЕСТУ ВСТРАИВАНИЯ: одно и то же
// звено, собранное носителем и собранное пробой, получает разные имена
// (`…unaryChain.UnaryPanicRecovery.func1` против
// `…TestSomething.UnaryPanicRecovery.func2`). Сравнение полных имён краснело бы
// на верном коде — и это первая редакция пробы делала.
//
// Поэтому берётся сегмент, стоящий ПЕРЕД хвостом `.funcN`, — имя конструктора
// звена. Оно не зависит ни от места встраивания, ни от порядкового номера
// замыкания, зато меняется при подмене звена другим. Имя локальной переменной в
// нём не участвует вовсе.
func funcID(v any) string {
	f := runtime.FuncForPC(reflect.ValueOf(v).Pointer())
	if f == nil {
		return "<неизвестное значение-функция>"
	}
	parts := strings.Split(f.Name(), ".")
	// Хвост вида `funcN` (в т.ч. `func1.2`) отбрасываем целиком.
	for len(parts) > 1 && strings.HasPrefix(parts[len(parts)-1], "func") {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return f.Name()
	}
	return parts[len(parts)-1]
}
