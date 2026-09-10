// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// Поведение звена восстановления паники. Утверждается СООБЩЕНИЕ, а не только
// код: фикс, локающий один лишь `codes.Internal`, остаётся зелёным ровно тогда,
// когда содержимое паники снова начинает течь клиенту.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// secretFragment — фрагмент значения паники, по которому проба узнаёт утечку.
// Отдельная константа: искать целиком весь payload недостаточно, утечь может
// и часть (например, только адрес или только имя символа).
const secretFragment = "kacho_internal_secret_marker"

// probeWithRecovery поднимает пробу со звеном и журналом в буфер.
func probeWithRecovery(t *testing.T) (*grpc.ClientConn, *bytes.Buffer, func()) {
	t.Helper()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	cc, closeFn := dialProbe(t,
		[]grpc.UnaryServerInterceptor{grpcsrv.UnaryPanicRecovery(logger)},
		[]grpc.StreamServerInterceptor{grpcsrv.StreamPanicRecovery(logger)})
	return cc, &logBuf, closeFn
}

func probeCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestRecoveredPanicAnswersFixedInternalAndNeverEchoesItsValue — ядро
// требования security.md §Hardening #1: ветка «непредвиденное» отдаёт
// ФИКСИРОВАННЫЙ текст. Паника несёт трассу и внутренние значения — их место
// в журнале, не в ответе.
func TestRecoveredPanicAnswersFixedInternalAndNeverEchoesItsValue(t *testing.T) {
	cc, _, closeFn := probeWithRecovery(t)
	defer closeFn()

	err := cc.Invoke(probeCtx(t), probeBoomMethod, &emptypb.Empty{}, &emptypb.Empty{})
	if err == nil {
		t.Fatal("паника обработчика не превратилась в ошибку — клиент получил успех")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("ответ не является gRPC-статусом: %v", err)
	}
	if st.Code() != codes.Internal {
		t.Fatalf("код ответа %s, ожидался %s", st.Code(), codes.Internal)
	}
	if st.Message() != "internal error" {
		t.Fatalf("сообщение клиенту %q — контракт требует фиксированного %q",
			st.Message(), "internal error")
	}
	if strings.Contains(err.Error(), secretFragment) {
		t.Fatalf("значение паники утекло клиенту целиком или частью: %v", err)
	}
}

// TestRecoveredPanicLeavesTheServerServing — «живой процесс»: следующий запрос
// по тому же соединению обслуживается. Без этого утверждения проба не отличала
// бы «панику превратили в ошибку» от «сервер после этого мёртв».
func TestRecoveredPanicLeavesTheServerServing(t *testing.T) {
	cc, _, closeFn := probeWithRecovery(t)
	defer closeFn()
	ctx := probeCtx(t)

	if err := cc.Invoke(ctx, probeBoomMethod, &emptypb.Empty{}, &emptypb.Empty{}); err == nil {
		t.Fatal("паника обработчика не превратилась в ошибку")
	}
	if err := cc.Invoke(ctx, probeFineMethod, &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("после восстановленной паники сервер не обслуживает: %v", err)
	}
}

// TestRecoveredPanicIsRecordedWithMethodAndStack — «ноль отказов за всю жизнь
// контроля обязано быть заметно» (security.md #8). Звено, которое гасит панику
// молча, превращает падение в невидимую деградацию.
func TestRecoveredPanicIsRecordedWithMethodAndStack(t *testing.T) {
	cc, logBuf, closeFn := probeWithRecovery(t)
	defer closeFn()

	_ = cc.Invoke(probeCtx(t), probeBoomMethod, &emptypb.Empty{}, &emptypb.Empty{})

	logged := logBuf.String()
	if !strings.Contains(logged, secretFragment) {
		t.Fatalf("значение паники не записано в журнал — причина потеряна:\n%s", logged)
	}
	if !strings.Contains(logged, probeBoomMethod) {
		t.Fatalf("в журнале нет метода %s — паника не привязана к запросу:\n%s",
			probeBoomMethod, logged)
	}
	if !strings.Contains(logged, "grpcsrv") && !strings.Contains(logged, "goroutine") {
		t.Fatalf("в журнале нет трассы — диагностировать нечем:\n%s", logged)
	}
}

// TestStreamPanicIsRecoveredToo — stream-листенер не освобождён: паника
// stream-обработчика роняет процесс тем же способом.
func TestStreamPanicIsRecoveredToo(t *testing.T) {
	cc, _, closeFn := probeWithRecovery(t)
	defer closeFn()

	stream, err := cc.NewStream(probeCtx(t), &grpc.StreamDesc{ServerStreams: true}, probeBoomStream)
	if err != nil {
		t.Fatalf("открытие стрима: %v", err)
	}
	if err := stream.SendMsg(&emptypb.Empty{}); err != nil && status.Code(err) != codes.Internal {
		t.Fatalf("отправка в стрим: %v", err)
	}
	rerr := stream.RecvMsg(&emptypb.Empty{})
	if rerr == nil {
		t.Fatal("паника stream-обработчика не превратилась в ошибку")
	}
	st, _ := status.FromError(rerr)
	if st.Code() != codes.Internal {
		t.Fatalf("код ответа стрима %s, ожидался %s", st.Code(), codes.Internal)
	}
	if st.Message() != "internal error" {
		t.Fatalf("сообщение стрима %q — контракт требует фиксированного %q",
			st.Message(), "internal error")
	}
	if strings.Contains(rerr.Error(), secretFragment) {
		t.Fatalf("значение паники утекло в стрим: %v", rerr)
	}
}

// TestRecoveryLinkCatchesPanicFromAnInnerInterceptor — обоснование ПОРЯДКА:
// звено обязано ловить панику не только обработчика, но и всего, что ниже него
// в цепочке, включая сами фильтры доступа. Фильтр — обычный Go-код и паникует
// так же; именно поэтому звено стоит НАД ними, а не рядом с обработчиком.
func TestRecoveryLinkCatchesPanicFromAnInnerInterceptor(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	panickingFilter := func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		panic(panicPayload)
	}
	cc, closeFn := dialProbe(t,
		[]grpc.UnaryServerInterceptor{grpcsrv.UnaryPanicRecovery(logger), panickingFilter},
		nil)
	defer closeFn()

	// Fine-обработчик исправен: паникует именно звено ниже.
	err := cc.Invoke(probeCtx(t), probeFineMethod, &emptypb.Empty{}, &emptypb.Empty{})
	if err == nil {
		t.Fatal("паника ВНУТРЕННЕГО интерсептора не перехвачена — звено стоит не там")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal || st.Message() != "internal error" {
		t.Fatalf("паника интерсептора дала %s/%q, ожидалось %s/%q",
			st.Code(), st.Message(), codes.Internal, "internal error")
	}
}

// TestRecoveryLinkSurvivesAbsentLogger — звено, которое само разыменовывает
// отсутствующий журнал, превращает восстановленную панику обратно в падение
// процесса, то есть отменяет собственный предмет. Из пяти прежних реализаций
// три падали ровно так; общее звено обязано пережить это и всё равно записать
// причину.
func TestRecoveryLinkSurvivesAbsentLogger(t *testing.T) {
	cc, closeFn := dialProbe(t,
		[]grpc.UnaryServerInterceptor{grpcsrv.UnaryPanicRecovery(nil)}, nil)
	defer closeFn()

	err := cc.Invoke(probeCtx(t), probeBoomMethod, &emptypb.Empty{}, &emptypb.Empty{})
	if err == nil {
		t.Fatal("без журнала паника не превратилась в ошибку")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal || st.Message() != "internal error" {
		t.Fatalf("без журнала звено дало %s/%q, ожидалось %s/%q",
			st.Code(), st.Message(), codes.Internal, "internal error")
	}
	if err := cc.Invoke(probeCtx(t), probeFineMethod, &emptypb.Empty{}, &emptypb.Empty{}); err != nil {
		t.Fatalf("без журнала сервер после паники не обслуживает: %v", err)
	}
}

// TestHandlerPanicWithRecoveryLinkKeepsTheProcessServing — положительный
// контроль к TestHandlerPanicWithoutRecoveryLinkKillsTheProcess: ТОТ ЖЕ
// харнесс, тот же запрос, отличие ровно одно — провязанное звено.
func TestHandlerPanicWithRecoveryLinkKeepsTheProcessServing(t *testing.T) {
	out, err := spawnChild(t, "with")
	if err != nil {
		t.Fatalf("со звеном процесс всё равно завершился (%v).\n%s", err, out)
	}
	if !strings.Contains(out, `ВЫЖИЛ code=Internal msg="internal error"`) {
		t.Fatalf("со звеном клиент получил не фиксированный INTERNAL.\n%s", out)
	}
	if !strings.Contains(out, "ОБСЛУЖИВАЕТ ДАЛЬШЕ") {
		t.Fatalf("со звеном сервер не пережил панику.\n%s", out)
	}
	t.Logf("со звеном: процесс жив, клиент получил фиксированный INTERNAL")
}
