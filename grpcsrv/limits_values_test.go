// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

// limits_values_test.go — ЗНАЧЕНИЯ пределов, а не факт их выставления.
//
// Соседний файл утверждает наблюдаемое на проводе: сервер объявляет предел и
// отвергает переросшее сообщение. Этого мало ровно в одном: провод не различает
// «250» и «любое другое конечное число», а таблица §8.6 — авторитет по числам, и
// расхождение константы с ней объявлено дефектом, а не разночтением. Поэтому числа
// закрепляются отдельно и здесь.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultServerLimitsCarryTheDecidedNumbers — числа §8.6 дословно.
func TestDefaultServerLimitsCarryTheDecidedNumbers(t *testing.T) {
	l := DefaultServerLimits()

	require.Equal(t, uint32(250), l.ConcurrentStreamsPerConnection,
		"одновременных вызовов на соединение — 250 (§8.6)")
	require.Equal(t, 16*1024*1024, l.SendMsgBytes,
		"предельный размер ответа — 16 МиБ (§8.6): законная страница из 1000 объектов ≈2 МиБ")
	require.Equal(t, 4*1024*1024, l.RecvMsgBytes,
		"предельный размер запроса — 4 МиБ (§8.6): решение ОСТАВИТЬ умолчание библиотеки, "+
			"записанное явно, чтобы молчание о нём не читалось как забывчивость")

	require.NoError(t, l.Validate())
	require.Len(t, l.ServerOptions(), 3,
		"все три предела обязаны доезжать до сервера: молчаливо пропущенный остаётся умолчанием библиотеки")
}

// TestServerLimitsRejectTheSilentlyInvertedZero — ноль одновременных вызовов
// отвергается.
//
// Это не педантизм: `grpc.MaxConcurrentStreams(0)` библиотека молча заменяет на
// `math.MaxUint32` (server.go), то есть ноль означает «не ограничено» — ровно
// противоположное тому, что читается. Настройка, которую переворачивают молча,
// хуже отсутствующей, поэтому отказ.
func TestServerLimitsRejectTheSilentlyInvertedZero(t *testing.T) {
	l := DefaultServerLimits()
	l.ConcurrentStreamsPerConnection = 0

	require.Error(t, l.Validate())
}

// TestServerLimitsRejectNonPositiveSizes — отрицание по обоим размерам.
func TestServerLimitsRejectNonPositiveSizes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mutet func(*ServerLimits)
	}{
		{"нулевой предел ответа", func(l *ServerLimits) { l.SendMsgBytes = 0 }},
		{"отрицательный предел запроса", func(l *ServerLimits) { l.RecvMsgBytes = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := DefaultServerLimits()
			tc.mutet(&l)
			require.Error(t, l.Validate())
		})
	}
}

// TestDefaultServerLimitsAreUsable — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к двум отрицаниям
// выше: набор по умолчанию проходит собственную проверку.
//
// Без него отрицания зеленели бы и на проверке, отвергающей всё.
func TestDefaultServerLimitsAreUsable(t *testing.T) {
	require.NoError(t, DefaultServerLimits().Validate())
}
