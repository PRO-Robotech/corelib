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

// takeWindows — шаг 5 Put: для каждого лимита в порядке описания (строгое
// возрастание по LimitLess проверено шагом 2) — один условный оператор окна.
// Ноль строк любого лимита — ErrLimitExhausted: вызывающий Put откатывает
// точку сохранения, снимая вклады прошедших лимитов этой постановки (CX1-10,
// CX1-61). Чтения счётчика с последующей записью нет — лимит держит база
// (NTF1-B09).
//
// Порядок замков строк окна — сужение полного порядка ключа окна (CX1-63): в
// одной постановке шаблон один, на каждую пару (scope, window_seconds) — одна
// строка текущего окна, и пары идут по возрастанию.
//
// Окно — выровненный интервал date_bin(window_seconds, now(), epoch) часами
// базы.
func takeWindows(ctx context.Context, sp pgx.Tx, svc string, desc TemplateDesc, recipient, initiator string) ([]window, error) {
	out := make([]window, 0, len(desc.Limits))
	for _, l := range desc.Limits {
		key := recipient
		if l.Scope == ScopeInitiator {
			key = initiator
		}
		var start time.Time
		err := sp.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %s AS w
			(template, scope, window_seconds, key, window_start, count)
			VALUES ($1, $2, $3::integer, $4, date_bin(make_interval(secs => $3::integer), now(), 'epoch'::timestamptz), 1)
			ON CONFLICT (template, scope, window_seconds, key, window_start)
			DO UPDATE SET count = w.count + 1 WHERE w.count < $5
			RETURNING window_start`, tablename.Of(svc, tablename.Window)),
			desc.Name, string(l.Scope), l.WindowSeconds, key, l.Max).Scan(&start)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: шаблон %s: окно %s/%dс", ErrLimitExhausted, desc.Name, l.Scope, l.WindowSeconds)
		}
		if err != nil {
			return nil, fmt.Errorf("feed: шаблон %s: окно %s/%dс: %w", desc.Name, l.Scope, l.WindowSeconds, err)
		}
		out = append(out, window{limit: l, key: key, start: start})
	}
	return out, nil
}
