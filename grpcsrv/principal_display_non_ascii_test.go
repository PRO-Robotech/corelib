// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// TestPrincipalDisplayName_NonASCII_SurvivesTheWire — отображаемое имя
// принципала, записанное не латиницей, обязано ДОЕЗЖАТЬ до обработчика.
//
// ─────────────────────────────────────────────────────────────────────────────
// ПРЕДМЕТ
//
// Продукт русскоязычный: арендатор регистрируется, называя себя по-русски, и
// это законный ввод. Значение обычного метаданного ключа gRPC ограничено
// печатаемой латиницей (0x20–0x7E); библиотека отвергает ВЕСЬ вызов, не дойдя
// до обработчика, — то есть отказ приходит на КАЖДЫЙ запрос к API, а не на тот
// один, где имя показывается.
//
// Дороже обычного дефекта именно это: вход при этом проходит, печенье сессии
// ставится, консоль открывается — и остаётся пустой. Со стороны неотличимо от
// «продукт не работает».
//
// ─────────────────────────────────────────────────────────────────────────────
// ПОЧЕМУ ПРОБА ЧЕРЕЗ НАСТОЯЩИЙ gRPC, А НЕ ЧЕРЕЗ ФУНКЦИЮ ИЗВЛЕЧЕНИЯ
//
// Ограничение на значение живёт в БИБЛИОТЕКЕ, а не в нашем коде. Проба,
// зовущая извлечение напрямую с уже собранными метаданными, о нём не узнает
// ничего и останется зелёной при неработающем продукте: она проверяет ответ
// нашей функции, а не то, доезжает ли вызов.
//
// Поэтому вызов идёт по настоящему соединению, через настоящий кодек.
func TestPrincipalDisplayName_NonASCII_SurvivesTheWire(t *testing.T) {
	const cyrillicName = "Демо Пользователь"

	cap := &captured{}
	dialer := serveBufconnNet(t, cap, grpc.Creds(insecure.NewCredentials()))

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	md := metadata.MD{}
	md.Set(grpcsrv.MDKeyPrincipalType, "user")
	md.Set(grpcsrv.MDKeyPrincipalID, "usr-cyrillic")
	grpcsrv.SetPrincipalDisplayMD(md, cyrillicName)

	_, err = healthpb.NewHealthClient(conn).Check(
		metadata.NewOutgoingContext(ctx, md), &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "вызов с не-латинским отображаемым именем обязан доехать "+
		"до обработчика: имя такого вида — законный ввод русскоязычного продукта")

	_, _, p, trusted := cap.get()
	require.True(t, trusted, "личность обязана быть принята")
	require.Equal(t, "usr-cyrillic", p.ID)
	require.Equal(t, cyrillicName, p.DisplayName,
		"имя обязано доехать БЕЗ ИСКАЖЕНИЙ — усечённое или подменённое имя "+
			"утверждает о вызывающем неправду в каждой операции, которую он создал")
}

// TestPrincipalDisplayName_ASCII_StillWorks — положительный контроль.
// Без него отрицание выше зеленело бы на реализации, которая ломает латиницу.
func TestPrincipalDisplayName_ASCII_StillWorks(t *testing.T) {
	const asciiName = "Demo User"

	cap := &captured{}
	dialer := serveBufconnNet(t, cap, grpc.Creds(insecure.NewCredentials()))

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	md := metadata.MD{}
	md.Set(grpcsrv.MDKeyPrincipalType, "user")
	md.Set(grpcsrv.MDKeyPrincipalID, "usr-ascii")
	grpcsrv.SetPrincipalDisplayMD(md, asciiName)

	_, err = healthpb.NewHealthClient(conn).Check(
		metadata.NewOutgoingContext(ctx, md), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	_, _, p, _ := cap.get()
	require.Equal(t, asciiName, p.DisplayName)
}

// TestPrincipalDisplayName_LegacyPlainKey_StillRead — совместимость на время
// выкатки. Край и сервисы катятся не одновременно, поэтому читатель обязан
// понимать и ПРЕЖНЮЮ форму: иначе в окне выкатки имя пропадает у всех, включая
// тех, у кого оно латиницей и работало.
func TestPrincipalDisplayName_LegacyPlainKey_StillRead(t *testing.T) {
	cap := &captured{}
	dialer := serveBufconnNet(t, cap, grpc.Creds(insecure.NewCredentials()))

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = healthpb.NewHealthClient(conn).Check(
		metadata.NewOutgoingContext(ctx, metadata.Pairs(
			grpcsrv.MDKeyPrincipalType, "user",
			grpcsrv.MDKeyPrincipalID, "usr-legacy",
			grpcsrv.MDKeyPrincipalDisplay, "Legacy Producer",
		)), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	_, _, p, _ := cap.get()
	require.Equal(t, "Legacy Producer", p.DisplayName,
		"прежняя форма обязана читаться, пока производители не перекатились")
}

var _ net.Conn // keep net import honest across build tags
