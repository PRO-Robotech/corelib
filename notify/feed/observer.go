// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// AckedRow — строка ленты, исход которой только что записал recordOutcome:
// что получает наблюдатель исхода (замысел issue-2924 З21 п.1, З28 п.1).
// ThreadKey — ключ нити строки; nil — у строки ключа нет.
type AckedRow struct {
	ID        string
	Class     Class
	Template  string
	Outcome   Outcome
	ThreadKey *string
}

// OutcomeObserver — порт наблюдателя исхода (З28 п.3). recordOutcome зовёт
// его последним действием в ветке, изменившей строку, в той же транзакции tx:
// отказ наблюдателя откатывает и запись исхода, и Ack получает отказ
// хранилища. Повтор Ack, не изменивший строку, наблюдателя не зовёт (CX5-54).
type OutcomeObserver interface {
	ObserveOutcome(ctx context.Context, tx pgx.Tx, row AckedRow) error
}

// ErrNilObserver — конструктор точки входа ленты (NewServer, NewLocal) без
// наблюдателя: отсутствие наблюдателя пишется явным значением NopObserver,
// и «наблюдателя нет» не совпадает с «наблюдатель забыт» (З28 п.3).
var ErrNilObserver = errors.New("feed: outcome observer is nil — pass feed.NopObserver explicitly")

type nopObserver struct{}

func (nopObserver) ObserveOutcome(context.Context, pgx.Tx, AckedRow) error { return nil }

// NopObserver — наблюдатель, который ничего не делает: явное «наблюдателя
// нет» (серверы лент модулей).
var NopObserver OutcomeObserver = nopObserver{}

type composed []OutcomeObserver

func (c composed) ObserveOutcome(ctx context.Context, tx pgx.Tx, row AckedRow) error {
	for _, o := range c {
		if err := o.ObserveOutcome(ctx, tx, row); err != nil {
			return err
		}
	}
	return nil
}

// ComposeObservers — наблюдатель, зовущий каждого из obs по разу в порядке
// перечня; первый отказ прерывает обход и откатывает запись исхода.
// Пустой перечень либо nil среди obs — nil: конструктор точки входа отвергнет
// его ErrNilObserver до старта, а не первый Ack.
func ComposeObservers(obs ...OutcomeObserver) OutcomeObserver {
	if len(obs) == 0 {
		return nil
	}
	for _, o := range obs {
		if o == nil {
			return nil
		}
	}
	return composed(append([]OutcomeObserver(nil), obs...))
}
