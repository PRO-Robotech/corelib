// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
)

// window — строка окна, в которую постановка внесла вклад.
type window struct {
	limit Limit
	key   string
	start time.Time
}

// windowKeys — ключи окон постановки по области: адресат (значение колонки
// адресата формы описания), инициатор и проект из Values.
type windowKeys struct {
	recipient, initiator, project string
}

func (k windowKeys) of(s Scope) string {
	switch s {
	case ScopeInitiator:
		return k.initiator
	case ScopeProject:
		return k.project
	case ScopeRecipient:
	}
	return k.recipient
}

// exhaustedError — ErrLimitExhausted с областью подавления: её метку несёт
// notify_suppressed_total (NTF-3 Р14). Текст называет шаблон и первое
// исчерпанное окно в порядке описания.
type exhaustedError struct {
	template string
	first    Limit
	scope    Scope
}

func (e *exhaustedError) Error() string {
	return fmt.Sprintf("%s: шаблон %s: окно %s/%dс", ErrLimitExhausted, e.template, e.first.Scope, e.first.WindowSeconds)
}

func (e *exhaustedError) Unwrap() error { return ErrLimitExhausted }

// suppressionScope — область метки подавления по множеству исчерпанных окон:
// исчерпано окно адресата — recipient (в том числе вместе с окном проекта,
// NTF-3 Р14); иначе project; иначе initiator.
func suppressionScope(exhausted map[Scope]bool) Scope {
	for _, s := range []Scope{ScopeRecipient, ScopeProject, ScopeInitiator} {
		if exhausted[s] {
			return s
		}
	}
	return ScopeRecipient
}

// takeWindows — шаг 5 Put: для каждого лимита в порядке описания (строгое
// возрастание по LimitLess проверено шагом 2) — один условный оператор окна.
// Ноль строк любого лимита — ErrLimitExhausted (*exhaustedError): вызывающий
// Put откатывает точку сохранения, снимая вклады всех лимитов этой постановки
// (CX1-10, CX1-61). После первого исчерпанного окна операторы оставшихся
// лимитов всё равно исполняются — тем же порядком и под той же точкой
// сохранения, — чтобы область подавления назвала каждое исчерпанное окно
// (исчерпаны оба — recipient); их вклад снимает тот же откат. Чтения счётчика
// с последующей записью нет — лимит держит база (NTF1-B09).
//
// Порядок замков строк окна — сужение полного порядка ключа окна (CX1-63): в
// одной постановке шаблон один, на каждую пару (scope, window_seconds) — одна
// строка текущего окна, и пары идут по возрастанию.
//
// Окно — выровненный интервал date_bin(window_seconds, now(), epoch) часами
// базы.
func takeWindows(ctx context.Context, sp pgx.Tx, svc string, desc TemplateDesc, keys windowKeys) ([]window, error) {
	out := make([]window, 0, len(desc.Limits))
	var ex *exhaustedError
	exhausted := map[Scope]bool{}
	for _, l := range desc.Limits {
		key := keys.of(l.Scope)
		var start time.Time
		err := sp.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %s AS w
			(template, scope, window_seconds, key, window_start, count)
			VALUES ($1, $2, $3::integer, $4, date_bin(make_interval(secs => $3::integer), now(), 'epoch'::timestamptz), 1)
			ON CONFLICT (template, scope, window_seconds, key, window_start)
			DO UPDATE SET count = w.count + 1 WHERE w.count < $5
			RETURNING window_start`, tablename.Of(svc, tablename.Window)),
			desc.Name, string(l.Scope), l.WindowSeconds, key, l.Max).Scan(&start)
		if errors.Is(err, pgx.ErrNoRows) {
			if ex == nil {
				ex = &exhaustedError{template: desc.Name, first: l}
			}
			exhausted[l.Scope] = true
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("feed: шаблон %s: окно %s/%dс: %w", desc.Name, l.Scope, l.WindowSeconds, err)
		}
		out = append(out, window{limit: l, key: key, start: start})
	}
	if ex != nil {
		ex.scope = suppressionScope(exhausted)
		return nil, ex
	}
	return out, nil
}
