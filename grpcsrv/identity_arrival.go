// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package grpcsrv — identity_arrival.go: отказ при ОБЪЯВЛЕННОЙ и не приехавшей
// личности (приёмка KAN-WIRE-1, сценарии KAN-W2-02…KAN-W2-04, предмет `ПР-1`).
//
// # Предмет: рассинхрон даёт ПОТЕРЮ личности, а не отказ
//
// Пространство имён личности несёт край, а читает слушатель. Пока приёмник умел
// спросить только про СВОЮ приставку, о чужой он не узнавал НИЧЕГО: пересланная
// личность для него не приезжала вовсе, и он читал это как «личности нет».
// Отказом это не кончалось — отсутствие личности в этом тракте отказом не
// является намеренно: фоновые пути ходят без неё, и запасное значение
// существует по решению. Значит переход обязан принести СВОЙ отказ.
//
// # Различает ОДИН факт, и он положительный
//
// Отвергается запрос, в котором личность БЫЛА ОБЪЯВЛЕНА и не приехала:
//
//   - пир прислал ключ ФОРМЫ личности под пространством имён, которого эта
//     сборка не читает — то есть назвал кого-то именем, которого здесь нет;
//   - либо прислал часть НАШЕГО подсемейства личности, не назвав ни типа, ни
//     идентификатора.
//
// Запрос, не приславший ничего похожего на личность, не отвергается. Это и есть
// законная безымянность, и на ней стоят фоновые согласователи: признак
// положительный, поэтому ложных отказов на них нет BY CONSTRUCTION — им нечего
// прислать, чтобы под него попасть.
//
// # Почему не «доверенный отправитель без личности — отказ»
//
// Такое правило выглядит проще и НЕВЕРНО: круг доверенных отправителей
// перечисляет не только край. Служба, законно передающая личность инициатора на
// одном пути, на другом звонит соседу ЗА СЕБЯ — и делает это тем же
// сертификатом. Отказ по членству в круге остановил бы её вторую полосу, а
// заодно проверенную пробу, утверждающую, что доверенный отправитель без
// пересланной личности личности НЕ ПОЛУЧАЕТ.
//
// # Почему отказ только у пира, чью пересылку мы почитаем
//
// «Личность объявлена и не приехала» — утверждение о пересылке, которую этот
// слушатель принял бы. У пира вне круга пересланная личность снимается и так,
// поэтому его рассинхрон ничего не теряет: терять нечего.
package grpcsrv

import (
	"context"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/principalwire"
)

// identityArrivalOutcome — что запрос сказал о личности. Закрытый словарь:
// значение метки не берётся из данных запроса, поэтому число рядов не растёт с
// числом обслуженных арендаторов.
type identityArrivalOutcome string

const (
	// arrivalPresent — личность приехала целиком и нашими ключами.
	arrivalPresent identityArrivalOutcome = "present"
	// arrivalAbsent — похожего на личность не приехало ничего. ЗАКОННАЯ
	// безымянность: фоновые пути, внутренние согласователи, прямой вызов.
	arrivalAbsent identityArrivalOutcome = "absent"
	// arrivalForeignNamespace — личность названа под пространством имён, которого
	// эта сборка не читает. Объявлена и не приехала.
	arrivalForeignNamespace identityArrivalOutcome = "foreign_namespace"
	// arrivalIncomplete — наше подсемейство личности приехало частью: ни типа, ни
	// идентификатора. Объявлена и не приехала.
	arrivalIncomplete identityArrivalOutcome = "incomplete"
	// arrivalUntrusted — пересылку этого пира слушатель не почитает; его
	// личность снимается, и «объявлена и не приехала» о нём не утверждение.
	arrivalUntrusted identityArrivalOutcome = "untrusted"
)

// refused — обязан ли исход кончиться отказом.
func (o identityArrivalOutcome) refused() bool {
	return o == arrivalForeignNamespace || o == arrivalIncomplete
}

// classifyIdentityArrival относит входящие метаданные к одному исходу.
//
// forwarded — прочитал ли извлекатель полную личность нашими ключами; берётся у
// того же разбора, что кладёт её в контекст, а не повторяется здесь: второе
// чтение одного предмета разошлось бы с первым молча.
//
// Обход НЕ выходит из цикла досрочно: карта метаданных перебирается в
// неопределённом порядке, и ранний возврат сделал бы исход зависящим от него.
func classifyIdentityArrival(ctx context.Context, forwarded bool) identityArrivalOutcome {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return arrivalAbsent
	}
	var foreign, ourPrincipal bool
	for k := range md {
		bare := principalwire.Bare(k)
		if !principalwire.IsIdentityShaped(bare) {
			continue
		}
		if !principalwire.IsOurs(bare) {
			foreign = true
			continue
		}
		if strings.HasPrefix(bare, principalwire.MetaPrincipalPrefix) {
			ourPrincipal = true
		}
	}
	switch {
	case foreign:
		// Чужое пространство перевешивает даже полную нашу личность: две
		// приставки на одном запросе означают либо сборку в середине перехода,
		// либо попытку назваться дважды. Оба исхода — отказ.
		return arrivalForeignNamespace
	case forwarded:
		return arrivalPresent
	case ourPrincipal:
		return arrivalIncomplete
	default:
		return arrivalAbsent
	}
}

// refusalFor — отказ, который видит вызывающий.
//
// Текст называет ПРЕДМЕТ и следующий шаг: отказ, из которого не видно, что
// делать дальше, разбирают чтением чужого кода. Значений запроса он не несёт.
func refusalFor(o identityArrivalOutcome) error {
	switch o {
	case arrivalForeignNamespace:
		return status.Error(codes.Unauthenticated,
			"forwarded identity did not arrive: the caller named it under an identity key "+
				"namespace this listener does not read; rebuild caller and listener from one tree")
	case arrivalIncomplete:
		return status.Error(codes.Unauthenticated,
			"forwarded identity did not arrive: the caller named part of it without the "+
				"principal type and id")
	default:
		return nil
	}
}

// IdentityArrival — счётчик того, ЧТО запрос сказал о личности.
//
// # Зачем отдельная серия, а не код ответа
//
// «Личность объявлена и не приехала» и «вызов законно пришёл без личности» —
// разные события с одинаковым видом снаружи: в обоих случаях у обработчика
// личности нет. Пока их не различает наблюдение, рассинхрон написания выглядит
// ростом безымянных вызовов, то есть не выглядит ничем. Код ответа этого не
// закрывает: отказ виден только у первого, а второй отказом не является и
// обязан не являться.
//
// Нулевой измеритель прозрачен и ничего не стоит: это законное состояние для
// проб, которым метрики не нужны, — но не для развёрнутого слушателя, где
// измеритель ставит носитель контура безусловно.
type IdentityArrival struct {
	total *prometheus.CounterVec
}

// NewIdentityArrival заводит счётчик и регистрирует его в переданном реестре.
//
// Реестр передаётся, а не берётся глобальный: глобальный делает две пробы
// одного пакета зависимыми через состояние процесса, и вторая падает на «серия
// уже зарегистрирована» — отказ, не имеющий отношения к предмету пробы.
func NewIdentityArrival(reg prometheus.Registerer) (*IdentityArrival, error) {
	a := &IdentityArrival{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kacho_grpc_identity_arrival_total",
			Help: "Что запрос сказал о личности: приехала целиком · законно не приехало ничего · " +
				"названа под чужим пространством имён · названа частью · пересылка пира не почитается. " +
				"Отдельная серия намеренно: без неё рассинхрон написания неотличим от роста " +
				"безымянных вызовов.",
		}, []string{"outcome"}),
	}
	if err := reg.Register(a.total); err != nil {
		return nil, err
	}
	return a, nil
}

// observe отмечает исход. Нулевой измеритель молчит.
func (a *IdentityArrival) observe(o identityArrivalOutcome) {
	if a == nil || a.total == nil {
		return
	}
	a.total.WithLabelValues(string(o)).Inc()
}

// WithIdentityArrival отдаёт звену извлечения личности счётчик исходов.
func WithIdentityArrival(a *IdentityArrival) TrustedPrincipalOption {
	return func(c *trustedPrincipalConfig) { c.arrival = a }
}
