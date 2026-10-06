// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
)

// Отказы Local (взятие и запись исхода в процессе). Признаки повтора — те же,
// что у сервера ленты (ReasonLeaseLost, ReasonOutcomeAlreadyRecorded), но
// значениями ошибки, а не статусом gRPC.
var (
	// ErrLeaseLost — аренда утрачена: токен не тот, аренда истекла, исход
	// записан другим токеном, уборщиком или Supersede.
	ErrLeaseLost = errors.New("feed: notification lease is lost")
	// ErrOutcomeAlreadyRecorded — тем же токеном записан другой исход.
	ErrOutcomeAlreadyRecorded = errors.New("feed: notification outcome is already recorded")
	// ErrNotificationNotFound — строки с таким id в ленте нет.
	ErrNotificationNotFound = errors.New("feed: notification not found")
	// ErrAckInvalid — запись исхода вне границ (validateAck): поле и правило
	// — в тексте обёртки.
	ErrAckInvalid = errors.New("feed: acknowledgement is invalid")
	// ErrClaimInvalid — взятие вне границ: max вне [1..MaxClaim], набор
	// классов пуст или вне Classes.
	ErrClaimInvalid = errors.New("feed: claim is invalid")
)

// LocalConfig — то, что корень службы передаёт точке входа в процессе.
type LocalConfig struct {
	// Module — имя модуля: метка метрик.
	Module string
	// Service — префикс таблиц ленты (не имя таблицы, замысел issue-2924 З28
	// п.7).
	Service string
	// DB — пул службы.
	DB TxDB
	// Keyring — кольцо ключей секрета (З11).
	Keyring *Keyring
	// Observer — наблюдатель исхода; nil — ErrNilObserver.
	Observer OutcomeObserver
	// Metrics — регистратор метрик ленты.
	Metrics prometheus.Registerer
	// Log — журнал; nil — молчащий.
	Log *slog.Logger
}

// Local — взятие и запись исхода в процессе для собственной ленты службы
// (NTF-3 Р19; замысел issue-2924 З28 п.2) — вторая точка входа к тем же
// строкам, что сервер ленты, над тем же ядром (claimTx, recordOutcome,
// validateAck, finishClaim). Своего SQL к таблице ленты у неё нет. Строит
// только NewLocal.
type Local struct {
	e entry
}

// NewLocal судит конфигурацию и регистрирует метрики ленты. Флага здесь нет:
// собственная лента службы поднимается её корнем.
func NewLocal(cfg LocalConfig) (*Local, error) {
	if !moduleForm.MatchString(cfg.Module) {
		return nil, fmt.Errorf("feed: LocalConfig.Module %q не DNS-метка", cfg.Module)
	}
	if err := tablename.Valid(cfg.Service); err != nil {
		return nil, fmt.Errorf("feed: LocalConfig.Service модуля %s: %w", cfg.Module, err)
	}
	if cfg.DB == nil {
		return nil, fmt.Errorf("feed: LocalConfig.DB модуля %s не задан", cfg.Module)
	}
	if cfg.Keyring == nil {
		return nil, fmt.Errorf("feed: LocalConfig.Keyring модуля %s не задан", cfg.Module)
	}
	if cfg.Observer == nil {
		return nil, fmt.Errorf("feed: LocalConfig.Observer модуля %s: %w", cfg.Module, ErrNilObserver)
	}
	if cfg.Metrics == nil {
		return nil, fmt.Errorf("feed: LocalConfig.Metrics модуля %s не задан", cfg.Module)
	}
	m, err := newMetrics(cfg.Metrics, cfg.Module)
	if err != nil {
		return nil, err
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Local{e: entry{
		module: cfg.Module, service: cfg.Service, db: cfg.DB, ring: cfg.Keyring,
		obs: cfg.Observer, metrics: m, log: log,
	}}, nil
}

// Claimed — строка, взятая Local.Claim: id, токен аренды, шаблон, класс,
// ключ нити (nil — ключа нет) и значения атрибутов, секретные — открытые.
type Claimed struct {
	ID, LeaseToken, Template string
	Class                    Class
	ThreadKey                *string
	Attrs                    map[string]string
}

// Claim арендует не больше limit строк классов classes (Classes, в том числе
// LocalOnlyClasses) — тот же оператор взятия, что у сервера ленты, с тем же
// условием срока и головы нити. Строки — в порядке взятия.
func (l *Local) Claim(ctx context.Context, classes []Class, limit int) ([]Claimed, error) {
	if limit < 1 || limit > MaxClaim {
		return nil, fmt.Errorf("%w: max %d вне [1..%d]", ErrClaimInvalid, limit, MaxClaim)
	}
	if len(classes) == 0 {
		return nil, fmt.Errorf("%w: набор классов пуст", ErrClaimInvalid)
	}
	words := make([]string, 0, len(classes))
	for _, c := range classes {
		if !slices.Contains(Classes(), c) {
			return nil, fmt.Errorf("%w: класс %q вне перечня", ErrClaimInvalid, string(c))
		}
		if !slices.Contains(words, string(c)) {
			words = append(words, string(c))
		}
	}
	got, err := l.e.claim(ctx, words, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("feed: взятие: %w", err)
	}
	kept, err := l.e.finishClaim(ctx, got)
	if err != nil {
		return nil, fmt.Errorf("feed: взятие: %w", err)
	}
	out := make([]Claimed, 0, len(kept))
	for _, r := range kept {
		out = append(out, Claimed{
			ID: r.id, LeaseToken: r.token, Template: r.template, Class: r.class,
			ThreadKey: r.threadKey, Attrs: r.attrs,
		})
	}
	return out, nil
}

// Ack — запись исхода в процессе: что передаёт Local.Ack. DeferFor — только
// при виде DEFER, в [MinDefer, MaxDefer]; при прочих видах — нуль.
type Ack struct {
	ID, LeaseToken string
	Outcome        Outcome
	DeferFor       time.Duration
}

// Ack записывает исход строки, взятой Local.Claim, — то же ядро, что Ack
// сервера ленты: границы validateAck, запись recordOutcome с наблюдателем.
// Повтор тем же токеном и исходом — nil без изменения строки; иначе
// ErrOutcomeAlreadyRecorded, ErrLeaseLost, ErrNotificationNotFound,
// ErrAckInvalid либо ошибка хранилища.
func (l *Local) Ack(ctx context.Context, a Ack) error {
	in := ackInput{id: a.ID, token: a.LeaseToken, outcome: a.Outcome, deferSet: a.DeferFor != 0, deferFor: a.DeferFor}
	if v := validateAck(in); v != nil {
		return fmt.Errorf("%w: %s", ErrAckInvalid, v.Error())
	}
	res, err := l.e.ack(ctx, in)
	if err != nil {
		return fmt.Errorf("feed: запись исхода строки %s: %w", a.ID, err)
	}
	switch res.verdict {
	case ackRecorded, ackRepeated:
		return nil
	case ackNotFound:
		return fmt.Errorf("%w: %s", ErrNotificationNotFound, a.ID)
	case ackAlreadyRecorded:
		return fmt.Errorf("%w: %s", ErrOutcomeAlreadyRecorded, a.ID)
	case ackLeaseLost:
	}
	return fmt.Errorf("%w: %s", ErrLeaseLost, a.ID)
}
