// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// Константы сервера ленты (З8, З9, З23; §8). Объявлены однажды, ручек нет.
const (
	// LeaseTTL — длительность аренды строки, выданной Claim.
	LeaseTTL = 5 * time.Minute
	// MaxClaim — потолок max в Claim.
	MaxClaim = 500
	// MinDefer, MaxDefer — границы defer_for в Ack DEFER.
	MinDefer = time.Second
	MaxDefer = 15 * time.Minute
)

// storeCallTimeout — свой срок каждого оператора хранилища сервера ленты
// (arch-per-call-deadline): срок вызывающего может не быть вовсе. Аренда — один
// оператор не больше MaxClaim строк с SKIP LOCKED, запись исхода — один
// условный оператор по первичному ключу; 10 с — запас на медленный, но живой
// источник, равный сроку вызова Claim у notify (sourceCallTimeout, §8), и
// много меньше LeaseTTL: оператор, переживший срок, аренду не продлевает.
const storeCallTimeout = 10 * time.Second

// FeedObjectType — тип объекта модели прав ленты: объект проверки Claim и Ack
// — notification_feed:<модуль>, к которому привязан сервер (З14).
const FeedObjectType servicecontract.ObjectType = "notification_feed"

// DB — хранилище сервера ленты и уборщика: пул источника. Каждый вызов —
// отдельный оператор в своей короткой транзакции; соединение не удерживается
// между операторами (NTF1-B18).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Elapsed — сколько прошло по монотонным часам процесса с момента Start.
type Elapsed func() time.Duration

// Clock — монотонные часы процесса: отдают длительности, а не моменты (УК80).
// Сервер ленты вычитает ими время между возвратом оператора аренды и ответом.
type Clock interface {
	Start() Elapsed
}

// monotonic — time.Now() без преобразований: разность двух его значений
// считается монотонной частью часов.
type monotonic struct{}

func (monotonic) Start() Elapsed {
	t0 := time.Now()
	return func() time.Duration { return time.Since(t0) }
}

// ServerConfig — то, что корень источника передаёт серверу ленты.
type ServerConfig struct {
	// Module — имя модуля: объект notification_feed:<Module>, метка метрик.
	Module string
	// Service — префикс таблиц ленты; тот же, что у Source.
	Service string
	// Enabled — флаг источника; сервер поднимается только при включённом
	// (Р9, NTF1-N07).
	Enabled Enabled
	// DB — пул источника.
	DB DB
	// Keyring — кольцо ключей секрета (З11).
	Keyring *Keyring
	// Clock — монотонные часы; nil — часы процесса.
	Clock Clock
	// Metrics — регистратор метрик ленты.
	Metrics prometheus.Registerer
	// Log — журнал; nil — молчащий.
	Log *slog.Logger
}

// Server — сервер ленты InternalNotificationFeedService модуля-источника.
// Строит только NewServer; регистрирует корень вызовом
// notifyv1.RegisterInternalNotificationFeedServiceServer на внутреннем
// слушателе.
type Server struct {
	notifyv1.UnimplementedInternalNotificationFeedServiceServer

	module  string
	service string
	db      DB
	ring    *Keyring
	clock   Clock
	metrics *metrics
	log     *slog.Logger
}

// NewServer судит конфигурацию и регистрирует метрики ленты. Выключенный или
// не разобранный флаг — отказ: сервер ленты при выключенной доставке не
// поднимается (уборщик — StartSweeper — поднимается при любом флаге).
func NewServer(cfg ServerConfig) (*Server, error) {
	if !moduleForm.MatchString(cfg.Module) {
		return nil, fmt.Errorf("feed: ServerConfig.Module %q не DNS-метка", cfg.Module)
	}
	if err := tablename.Valid(cfg.Service); err != nil {
		return nil, fmt.Errorf("feed: ServerConfig.Service модуля %s: %w", cfg.Module, err)
	}
	if !cfg.Enabled.Set() {
		return nil, fmt.Errorf("feed: ServerConfig.Enabled модуля %s не разобран ParseEnabled", cfg.Module)
	}
	if !cfg.Enabled.On() {
		return nil, fmt.Errorf("feed: сервер ленты модуля %s при выключенной доставке не поднимается", cfg.Module)
	}
	if cfg.DB == nil {
		return nil, fmt.Errorf("feed: ServerConfig.DB модуля %s не задан", cfg.Module)
	}
	if cfg.Keyring == nil {
		return nil, fmt.Errorf("feed: ServerConfig.Keyring модуля %s не задан", cfg.Module)
	}
	if cfg.Metrics == nil {
		return nil, fmt.Errorf("feed: ServerConfig.Metrics модуля %s не задан", cfg.Module)
	}
	m, err := newMetrics(cfg.Metrics, cfg.Module)
	if err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = monotonic{}
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		module: cfg.Module, service: cfg.Service, db: cfg.DB, ring: cfg.Keyring,
		clock: clock, metrics: m, log: log,
	}, nil
}

// Bound — привязка сервера к экземпляру модели прав для дескриптора
// (servicecontract.Spec.Bound): notification_feed:<модуль>.
func (s *Server) Bound() servicecontract.Bound {
	return servicecontract.Bound{Type: FeedObjectType, ID: s.module}
}

// fieldError — INVALID_ARGUMENT с текстом «<поле>: <правило>» и нарушением
// поля в BadRequest.
func fieldError(field, rule string) error {
	st := status.New(codes.InvalidArgument, field+": "+rule)
	withDetails, err := st.WithDetails(&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
		{Field: field, Description: rule},
	}})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// internalError — отказ хранилища: фиксированный текст наружу, причина — в
// журнал.
func (s *Server) internalError(ctx context.Context, op string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	s.log.ErrorContext(ctx, "notification feed storage failure",
		slog.String("module", s.module), slog.String("op", op), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "internal error")
}

// reasonError — FAILED_PRECONDITION с машинным признаком reason ленты.
func (s *Server) reasonError(reason, id, msg string) error {
	st := status.New(codes.FailedPrecondition, msg)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reason, Domain: s.module + ".kacho.cloud",
		Metadata: map[string]string{"resource_type": "notification", "resource_id": id},
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}
