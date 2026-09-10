// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

// Звено, восстанавливающее панику обработчика, — ОДНО на платформу.
//
// # Предмет
//
// grpc-go панику обработчика НЕ восстанавливает: она уходит из серверной
// горутины и завершает процесс. Значит дефект в обработке ОДНОГО запроса
// ОДНОГО тенанта прекращает обслуживание ВСЕХ — вместе с in-flight запросами,
// исполнителем операций и дренажами очередей. Одно nil-разыменование на пути
// запроса равносильно kill -9 по сервису, и вызвать его вправе любой, кому
// этот RPC доступен (CWE-248 / CWE-755 — отказ в обслуживании).
//
// # Почему звено одно
//
// До сведения таких звеньев было пять: четыре в сервисах и одно на крае. Они
// расходились по трём осям, и каждое расхождение — отдельное поведение
// платформы на один и тот же отказ:
//
//   - ТЕКСТ КЛИЕНТУ: четверо отдавали «internal error», край — «internal server
//     error». Два разных ответа на одно и то же условие.
//   - ОТСУТСТВУЮЩИЙ ЖУРНАЛ: двое переживали `logger == nil` (и молча ничего не
//     записывали), трое разыменовывали его в самой обработке паники — то есть
//     звено роняло процесс ровно там, где обязано было его спасти.
//   - ОТСУТСТВУЮЩЕЕ ОПИСАНИЕ ВЫЗОВА: один защищался от `info == nil`, остальные
//     нет; край не записывал метод вовсе, поэтому паника в журнале не была
//     привязана к запросу.
//
// Сведение берёт по каждой оси САМУЮ УЗКУЮ семантику, а не среднюю:
// фиксированный «internal error»; журнал обязателен, но его отсутствие не
// вправе стать вторым падением; вызов всегда назван. Общее звено не принимает
// ничего, что отвергала бы строгая из прежних реализаций.
//
// # Место в цепочке
//
// Звено ставится НАД всем, что должно быть защищено, и ПОД тем, что обязано
// наблюдать исход:
//
//	наблюдение (метрики / журнал доступа)   ← снаружи
//	  восстановление паники                 ← здесь
//	    фильтры личности и доступа, тайм-ауты, обработчик
//
// Верхняя граница — оно обязано перехватывать панику не только обработчика, но
// и любого звена ниже, включая сами фильтры доступа: фильтр — обычный код и
// паникует так же.
//
// Нижняя граница — оно обязано оставаться ВНУТРИ наблюдения. Наблюдающее звено
// снаружи видит обычный возврат `codes.Internal` и записывает исход; наблюдающее
// звено внутри было бы пропущено раскруткой стека, и паника не попала бы ни в
// одну метрику и ни в одну строку журнала доступа — то есть самый тяжёлый класс
// отказов стал бы единственным ненаблюдаемым.
//
// # Что уходит клиенту
//
// Только фиксированный текст. Значение паники несёт трассу, внутренние адреса и
// значения запроса; его место — журнал сервера (security.md §Hardening #1:
// ветка «непредвиденное» отдаёт фиксированный непрозрачный текст и никогда не
// эхает внутреннюю деталь).

import (
	"context"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// panicRecoveredMessage — единственный текст, который восстановленная паника
// отдаёт клиенту. Часть контракта ошибок (api-conventions.md): тон фиксирован и
// меняется осознанно, а не попутно.
const panicRecoveredMessage = "internal error"

// UnaryPanicRecovery — unary-звено восстановления паники.
//
// logger == nil допустим и НЕ приводит к отказу: звено переходит на
// slog.Default(). Разыменовать здесь отсутствующий журнал значило бы уронить
// процесс внутри обработки паники, то есть отменить собственный предмет; а
// промолчать значило бы погасить падение невидимо, и «ноль паник за всю жизнь
// сервиса» стало бы неотличимо от «паники не записываются».
func UnaryPanicRecovery(logger *slog.Logger) grpc.UnaryServerInterceptor {
	log := loggerOrDefault(logger)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logRecoveredPanic(log, unaryMethod(info), r)
				// Ответ обнуляется явно: частично собранное тело не вправе
				// уехать рядом с ошибкой.
				resp = nil
				err = status.Error(codes.Internal, panicRecoveredMessage)
			}
		}()
		return handler(ctx, req)
	}
}

// StreamPanicRecovery — stream-звено восстановления паники. Stream-листенер не
// освобождён: паника stream-обработчика завершает процесс тем же способом.
func StreamPanicRecovery(logger *slog.Logger) grpc.StreamServerInterceptor {
	log := loggerOrDefault(logger)
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logRecoveredPanic(log, streamMethod(info), r)
				err = status.Error(codes.Internal, panicRecoveredMessage)
			}
		}()
		return handler(srv, ss)
	}
}

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// unaryMethod / streamMethod — описание вызова может отсутствовать; звену
// нельзя падать на этом, но и терять привязку паники к запросу нельзя.
func unaryMethod(info *grpc.UnaryServerInfo) string {
	if info == nil {
		return "<unknown method>"
	}
	return info.FullMethod
}

func streamMethod(info *grpc.StreamServerInfo) string {
	if info == nil {
		return "<unknown method>"
	}
	return info.FullMethod
}

// logRecoveredPanic пишет причину и трассу на сервер. Наружу не уходит ничего
// из этого — см. panicRecoveredMessage.
func logRecoveredPanic(log *slog.Logger, method string, r any) {
	log.Error("recovered panic in gRPC handler",
		slog.String("method", method),
		slog.Any("panic", r),
		slog.String("stack", string(debug.Stack())),
	)
}
