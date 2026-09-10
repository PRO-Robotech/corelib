// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcclient

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// dial.go — сборка клиентского gRPC-соединения к соседнему сервису: ОДНА на
// всё дерево.
//
// # Откуда взялось
//
// До 2026-09-04 соединения собирал сторонний строитель, чей модуль не нёс
// лицензии НИ ОДНИМ файлом (снят целиком). Отсутствие лицензии означает «все
// права защищены», и публичный репозиторий с таким пином распространял чужой
// код без разрешения. Здесь тот же набор параметров собран из штатных средств
// gRPC — и держится гейтом `internal/repohygiene`
// `TestEveryPinnedModuleCarriesALicense`.
//
// # Что сохранено дословно
//
//	отступ соединения  — BaseDelay=d/10, Multiplier=1.01, Jitter=0.1,
//	                     MaxDelay=d, MinConnectTimeout=d/10; d<1s поднимается до 1s
//	повтор вызова      — только на UNAVAILABLE, не более Retries сверх исходной попытки
//	разрешение имени   — схема `passthrough`, как у снятого строителя
//	keepalive          — Timeout = Time/3, PermitWithoutStream=false
//	пустые creds       — insecure
//
// # Что различается — названо, а не умолчано
//
//  1. ПОВТОР задаётся конфигурацией службы, а не перехватчиком. Конфигурация
//     требует ПОЛОЖИТЕЛЬНОГО отступа, поэтому между попытками появляется малая
//     задержка, тогда как снятый строитель повторял немедленно. Это отличие уже
//     жило в дереве: путь распределения нагрузки vpc собирался так же и нёс ту
//     же оговорку.
//  2. Снят перехватчик, дописывавший имя хоста в исходящие метаданные. Читателя
//     у этого заголовка в дереве НЕТ НИ ОДНОГО (предикат:
//     `git grep -n 'host-name' -- '*.go'` → пусто), поэтому переносить его
//     значило бы завести код, чей выход никто не читает.
//
// # Почему схема разрешения имени названа ЯВНО
//
// Снятый строитель звал `grpc.DialContext`, чья схема по умолчанию —
// `passthrough`. У `grpc.NewClient` умолчание ДРУГОЕ (`dns`), и молчаливый
// переход сменил бы разрешение имени у каждого соседа разом. Поэтому схема
// пишется в адрес, а не оставляется на умолчание: умолчание уже менялось однажды
// и может смениться снова.

const (
	// tcpScheme — схема, которую снятый строитель срезал сам. Оператор, задавший
	// её в профиле развёртывания, обязан продолжать работать.
	tcpScheme = "tcp://"
	// passthroughScheme — имя отдаётся сетевому набирателю как есть.
	passthroughScheme = "passthrough:///"
	// dnsScheme — резолвер, отдающий ВСЕ адреса имени. Нужен распределению:
	// passthrough отдаёт один адрес, и балансировать было бы нечего.
	dnsScheme = "dns:///"
	// minDialBackoff — пол срока набора. Ниже секунды отступ вырождается в сотые
	// доли, и повтор соединения превращается в занятое ожидание.
	minDialBackoff = time.Second
	// keepAliveAckDivisor — подтверждение ожидается за треть интервала опроса.
	keepAliveAckDivisor = 3
)

// PeerDialOptions — параметры соединения с соседним сервисом.
type PeerDialOptions struct {
	// Endpoint — адрес соседа: `host:port`, либо адрес, сам назвавший резолвер.
	Endpoint string
	// Creds — транспортные учётные данные. nil → insecure.
	Creds credentials.TransportCredentials
	// Retries — сколько раз повторить вызов сверх исходной попытки. 0 → без повтора.
	Retries uint
	// DialTimeout — цель отступа переподключения.
	DialTimeout time.Duration
	// KeepAliveTime — интервал опроса. 0 → без keepalive.
	KeepAliveTime time.Duration
	// UserAgent — представление клиента.
	UserAgent string
	// RoundRobin — распределять вызовы по ВСЕМ адресам имени. Требует резолвера
	// dns: passthrough отдаёт один адрес.
	RoundRobin bool
}

// PeerTarget — адрес в форме, которую понимает резолвер gRPC. Схема пишется
// явно; разбор разобран в шапке файла.
func PeerTarget(endpoint string, roundRobin bool) string {
	target := strings.TrimSpace(endpoint)
	if rest, ok := strings.CutPrefix(target, tcpScheme); ok {
		target = strings.TrimSpace(rest)
	}
	if strings.Contains(target, "://") {
		return target
	}
	if roundRobin {
		return dnsScheme + target
	}
	return passthroughScheme + target
}

// PeerConnectParams — отступ переподключения. Величины дословно те же, что у
// снятого строителя.
func PeerConnectParams(dialTimeout time.Duration) grpc.ConnectParams {
	d := dialTimeout
	if d < minDialBackoff {
		d = minDialBackoff
	}
	cfg := backoff.DefaultConfig
	cfg.BaseDelay = d / 10
	cfg.Multiplier = 1.01
	cfg.Jitter = 0.1
	cfg.MaxDelay = d
	return grpc.ConnectParams{Backoff: cfg, MinConnectTimeout: d / 10}
}

// PeerServiceConfigJSON — конфигурация службы: политика повтора и, по просьбе,
// распределение. Пустая строка означает «объявлять нечего» — а не «объявлено
// пустое»: повтор без попыток и балансировщик без адресов суть разные вещи.
func PeerServiceConfigJSON(retries uint, roundRobin bool) string {
	var parts []string
	if roundRobin {
		parts = append(parts, `"loadBalancingConfig":[{"round_robin":{}}]`)
	}
	if retries > 0 {
		// maxAttempts считает ИСХОДНУЮ попытку, поэтому retries+1.
		parts = append(parts, fmt.Sprintf(
			`"methodConfig":[{"name":[{}],"retryPolicy":{`+
				`"maxAttempts":%d,"initialBackoff":"0.1s","maxBackoff":"1s",`+
				`"backoffMultiplier":2.0,"retryableStatusCodes":["UNAVAILABLE"]}}]`,
			retries+1))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// DialPeer — соединение с соседом. Не набирает: `grpc.NewClient` откладывает
// соединение до первого вызова, поэтому отказ здесь означает негодные
// параметры, а не недоступность соседа.
func DialPeer(opts PeerDialOptions) (*grpc.ClientConn, error) {
	if strings.TrimSpace(opts.Endpoint) == "" {
		return nil, fmt.Errorf("grpcclient.DialPeer: адрес соседа пуст")
	}
	creds := opts.Creds
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithUserAgent(opts.UserAgent),
		grpc.WithConnectParams(PeerConnectParams(opts.DialTimeout)),
	}
	if cfg := PeerServiceConfigJSON(opts.Retries, opts.RoundRobin); cfg != "" {
		dialOpts = append(dialOpts, grpc.WithDefaultServiceConfig(cfg))
	}
	if opts.KeepAliveTime > 0 {
		dialOpts = append(dialOpts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                opts.KeepAliveTime,
			Timeout:             opts.KeepAliveTime / keepAliveAckDivisor,
			PermitWithoutStream: false,
		}))
	}
	cc, err := grpc.NewClient(PeerTarget(opts.Endpoint, opts.RoundRobin), dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("grpcclient.DialPeer %q: %w", opts.Endpoint, err)
	}
	return cc, nil
}
