// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

// DefaultKeepaliveEnforcement — server-side EnforcementPolicy, допускающая частые
// idle keepalive-пинги client'ов.
//
// gRPC-дефолт (MinTime: 5m, PermitWithoutStream: false) забанил бы клиента с
// PermitWithoutStream-пингами каждые 10s → GOAWAY too_many_pings. Чтобы idle-prone
// conn'ы (compute→iam-internal authz, iam subject-drainer→api-gateway) могли держать
// conn теплым, сервер обязан разрешать такие пинги: MinTime <= клиентский Time (10s)
// и PermitWithoutStream=true.
func DefaultKeepaliveEnforcement() keepalive.EnforcementPolicy {
	return keepalive.EnforcementPolicy{
		MinTime:             5 * time.Second,
		PermitWithoutStream: true,
	}
}

// NewServer создает gRPC-сервер с зарегистрированными Health-сервисом в состоянии SERVING
// и server-reflection (для grpcurl, debug, dev-tooling).
//
// DefaultKeepaliveEnforcement() и DefaultServerLimits() ставятся ПЕРВЫМИ в opts,
// чтобы caller-opts могли их переопределить.
//
// # Почему пределы стоят ЗДЕСЬ, а не у каждого вызывающего
//
// Умолчания библиотеки оставляют сервер без предела одновременных вызовов и без
// предела размера ответа (числа и координаты — в limits.go). Пока пределы
// выставлял бы каждый сервис у себя, «не выставил» было бы неотличимо от «выставил
// такие же»: опция не обязательна, её отсутствие ничего не печатает, и слушатель
// поднимается молча. Здесь у величин ОДИН источник, и ни один из слушателей
// платформы не может подняться без них — не потому, что это правило, а потому что
// другого конструктора сервера в дереве нет.
//
// Следствие для гейта: «сервер без объявленного предела одновременных вызовов»
// перестал быть находкой, которую надо искать по дереву, — такого состояния не
// существует по построению. Утверждается это не прочтением, а на проводе:
// [TestServerAdvertisesItsConcurrentStreamLimit] читает кадр настроек соединения.
func NewServer(opts ...grpc.ServerOption) *grpc.Server {
	base := append([]grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(DefaultKeepaliveEnforcement()),
	}, DefaultServerLimits().ServerOptions()...)
	s := grpc.NewServer(append(base, opts...)...)
	h := health.NewServer()
	h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, h)
	reflection.Register(s)
	return s
}
