// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

// links.go — звенья цепочки, которые носитель ставит САМ из значений
// дескриптора. Каждое живёт здесь, а не в сервисе, ровно по причине пакета:
// поля интерсепторного типа в [servicecontract.Spec] нет, поэтому принести своё
// звено нельзя, а значит и порядок остаётся один на все сервисы.

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// ── журнал доступа ──────────────────────────────────────────────────────────

// accessLogUnary — построчная запись исхода КАЖДОГО вызова: метод, gRPC-код,
// длительность. Без PII: коррелируем по имени метода и коду, не по субъекту.
//
// # Почему запись ПОСЛЕ возврата, а не через defer
//
// Так звено видит обычный возврат `codes.Internal`, который отдаёт
// восстановление паники этажом ниже, и записывает его как исход. Пара «снаружи
// восстановления + запись после возврата» и есть механизм: паниковавший вызов
// доходит до этой строки уже нормальным возвратом.
//
// Обратная граница тоже обязательна и названа: звено остаётся НАД извлечением
// личности и решением о доступе, потому что паникует и фильтр. Стоя под ними,
// оно пропустило бы ровно тот исход, ради которого стоит снаружи.
func accessLogUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := h(ctx, req)
		logger.Info("grpc unary",
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"duration_ms", time.Since(start).Milliseconds())
		return resp, err
	}
}

// accessLogStream — то же для стримов.
//
// Прежняя (снятая) реализация одного сервиса вела журнал только для unary, и
// стрим-вызов не оставлял ни строки. Здесь оба вида пишутся одинаково: «у нас
// тут стримов немного» — не основание, стрим-обработчик паникует и отвергается
// ровно так же, а длительность у него тем более осмысленна.
func accessLogStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		start := time.Now()
		err := h(srv, ss)
		logger.Info("grpc stream",
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"duration_ms", time.Since(start).Milliseconds())
		return err
	}
}

// ── верхняя граница обработки ───────────────────────────────────────────────

// capDeadline возвращает контекст со сроком НЕ ПОЗЖЕ now+budget. Более строгий
// срок вызывающего возвращается как есть: окно не расширяется никогда — иначе
// клиент, попросивший секунду, получал бы наш бюджет и держал бы ресурс дольше,
// чем сам согласился ждать.
//
// Общая на обе полосы, потому что арифметика у них одна; РАЗНЫЕ у них величины,
// и приходят они из разных полей дескриптора.
func capDeadline(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	limit := time.Now().Add(budget)
	if dl, ok := ctx.Deadline(); ok && !dl.After(limit) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, limit)
}

// handlingBudgetUnary кладёт верхнюю границу на обработку вызова целиком —
// включая вопрос о доступе и запросы к своей БД.
//
// Ветки «границы нет» здесь НЕТ намеренно: величина обязательна в дескрипторе
// ([servicecontract.Spec.HandlingBudget]), поэтому неположительного значения до
// этого места не доезжает. Прежняя (снятая) реализация одного сервиса несла
// `timeout<=0 → passthrough` — и эта ветка была ровно тем местом, где граница
// исчезала молча: композиционный корень просто не задавал число.
func handlingBudgetUnary(budget time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		ctx, cancel := capDeadline(ctx, budget)
		defer cancel()
		return h(ctx, req)
	}
}

// streamBudgetLink — срок жизни ПОДПИСКИ: подменяет контекст стрима
// ограниченным СВОЕЙ величиной ([servicecontract.Spec.StreamBudget]).
//
// # Почему величина СВОЯ, а не граница обработки запроса
//
// Для запроса «верхняя граница обработки» — ровно её предмет. Для серверного
// стрима та же величина означает другое: срок, по истечении которого контекст
// стрима истекает, обработчик выходит, и клиент видит разрыв — причём как
// сетевой сбой, а не как наш отказ. Распространить одно правило на два вида
// вызовов с принципиально разным сроком жизни значило бы прочесть намеренно
// узкую семантику за общий случай, поэтому величины две, и вторая обязана
// превосходить первую (это судит [servicecontract.New]).
//
// # Когда этого звена в цепочке НЕТ вовсе
//
// Ось допускает названное изъятие «серверных стримов не служу». Тогда носитель
// звена не ставит, и стрим-цепочка сроком не накрывается — состояние честное:
// накрывать нечего. Что заявление верно, судит отказ старта [auditStreamBudget]
// (О11) по СЛУЖИМОМУ набору, а не по слову автора: появился первый служимый
// серверный стрим — процесс не поднимается.
//
// Граница изъятия, названная прямо: доменных стримов при нём нет, но
// платформенные (наблюдение за здоровьем, рефлексия) сервер регистрирует сам, и
// они остаются без срока. Это ровно то, что объявлено словами «стримы этой
// посадки границей не накрываются»; прежняя редакция рвала наблюдение за
// здоровьем каждые полминуты и называла это верхней границей обработки.
func streamBudgetLink(budget time.Duration) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		ctx, cancel := capDeadline(ss.Context(), budget)
		defer cancel()
		return h(srv, &budgetedStream{ServerStream: ss, ctx: ctx})
	}
}

// budgetedStream подменяет Context() обёрнутого стрима на ограниченный сроком.
type budgetedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *budgetedStream) Context() context.Context { return s.ctx }

// ── загрузочный гейт мутаций ────────────────────────────────────────────────

// bootGateUnary отвергает создание ресурса, пока путь доставки намерений
// регистрации не поднят.
//
// Что закрывается: без гейта сервис, у которого дренаж регистраций или сосед
// лежит, продолжает принимать создание — и каждый созданный в этом окне ресурс
// остаётся БЕЗ владельца, потому что намерение никуда не доехало. Отказ хуже
// молчания только на вид: молчание здесь означает выданный доступ, которого
// никто не выдавал.
//
// Стрим-аналога нет, и это не пропуск: мутация в этом продукте всегда unary —
// `Create`/`Update`/`Delete` возвращают `Operation`, стрим-мутации не
// существует. Звено, которое не может сработать, — мёртвый вес, и ставить его
// «для симметрии» значило бы завести ровно то, что этот пакет вычищает.
func bootGateUnary(gate servicecontract.BootGate) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if IsGatedMutation(info.FullMethod) {
			if err := gate.GuardMutation(); err != nil {
				return nil, err
			}
		}
		return h(ctx, req)
	}
}

// IsGatedMutation сообщает, записывает ли этот метод намерение регистрации, — то
// есть подпадает ли он под загрузочный гейт.
//
// Форма — `/<пакет>.<Служба>/Create`, кроме служб `Internal*`: админ-каталог
// (типы дисков, регионы, зоны) владельца не заводит и намерения регистрации не
// порождает, поэтому гейтить его значило бы закрыть админ-путь по причине, к
// нему не относящейся.
//
// Чтение — по служимому имени метода, а не по перечню: восьмой ресурс попадает
// под гейт в момент появления, а не тогда, когда кто-нибудь вспомнит дополнить
// список.
//
// # Сколько копий этого предиката в дереве СЕГОДНЯ — четыре, а не одна
//
// Прежняя редакция этой шапки говорила «перенесён из трёх одинаковых копий,
// живших по одной в каждом сервисе», и это описывало НАМЕРЕНИЕ фазы, а не
// дерево: три копии стоят на месте (`services/{vpc,compute,nlb}/internal/fgaboot`),
// каждая дословно совпадает с этой. Здесь заведена ЧЕТВЁРТАЯ — канонная, — а три
// снимаются вместе с переводом своих сервисов на носитель, не раньше: снять их
// сейчас значило бы оставить сервисы без гейта до самого перевода. Читающий эту
// строку и решивший, что дублей нет, не пошёл бы их искать — поэтому счёт назван.
//
// Предикат ЭКСПОРТИРОВАН ради второго читателя: сервис вправе спросить у
// носителя, есть ли у него вообще гейтируемая мутация, — и заявление «гейт мне не
// нужен» тогда судится тем же кодом, который гейт исполняет, а не своей копией
// предиката в пробе сервиса.
func IsGatedMutation(fullMethod string) bool {
	if !strings.HasSuffix(fullMethod, "/Create") {
		return false
	}
	return !strings.Contains(fullMethod, ".Internal")
}

// ── путь отказа: сокрытие существования ─────────────────────────────────────

// existenceAwareCheck — решатель, доводящий отказ до формы, неотличимой от
// настоящего промаха владельца.
//
// # Что решает и почему без него отказ отличим
//
// Владелец модели отвечает отказом одинаково и на несуществующий объект, и на
// существующий чужой: у него нет нашей базы, а разбирать прозу его причины
// носитель не станет (почему — в godoc адаптера `pkg/authz/authziam`, куда он
// уехал вместе со своим разбором). Значит различить «объекта
// нет» и «объект есть, но не твой» может ТОЛЬКО тот, у кого объект лежит в базе,
// — и это мы сами. Без такой сверки мутация по обоим случаям отвечает
// `PermissionDenied`, и вызывающий читает по коду ответа то, что сокрытие
// обязано было закрыть:
//
//   - объекта нет      → пропускаем к обработчику, он вернёт дословный промах
//     владельца из своей БД (`ErrNoPath`);
//   - объект есть      → обработчик НЕ вызывается, звено отвечает `NotFound`
//     текстом владельца (`ErrHideExistence`) — «есть, но не твой» звучит как
//     «нет такого»;
//   - проба ошиблась   → отказ остаётся отказом (fail-closed): недоступность
//     собственной БД не есть основание пропустить вызов.
//
// Ошибка транспорта до владельца модели НИКОГДА не скрывается: она уходит выше
// как есть и отрабатывает fail-closed. Скрыть перебой под `NotFound` значило бы
// сказать вызывающему «такого объекта нет», когда мы просто не смогли спросить.
type existenceAwareCheck struct {
	inner  authz.CheckClient
	probe  servicecontract.ExistenceProbe
	scoped map[string]struct{} // пообъектные типы, ВЫВЕДЕННЫЕ из карты прав
}

// Check — порт решателя. Утвердительный ответ и ошибку отдаёт как есть; работает
// только на отказе.
func (c *existenceAwareCheck) Check(ctx context.Context, subjectID, relation, object string) (bool, error) {
	allowed, err := c.inner.Check(ctx, subjectID, relation, object)
	if err != nil || allowed {
		return allowed, err
	}
	objectType, objectID, ok := splitFGAObject(object)
	if !ok {
		return false, nil
	}
	if _, scoped := c.scoped[objectType]; !scoped {
		// Не пообъектный тип (проект, аккаунт, кластер): существование там не
		// скрывается, скрывать нечего.
		return false, nil
	}
	exists, perr := c.probe.ObjectExists(ctx, objectType, objectID)
	if perr != nil {
		// Fail-closed: отказ остаётся отказом.
		return false, nil
	}
	if !exists {
		return false, authz.ErrNoPath
	}
	return false, authz.ErrHideExistence
}

var _ authz.CheckClient = (*existenceAwareCheck)(nil)

// splitFGAObject разбирает объект модели `<тип>:<id>`. ok ложен, если форма не
// та: разделителя нет либо одна из половин пуста.
func splitFGAObject(object string) (objectType, objectID string, ok bool) {
	i := strings.IndexByte(object, ':')
	if i <= 0 || i == len(object)-1 {
		return "", "", false
	}
	return object[:i], object[i+1:], true
}
