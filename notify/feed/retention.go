// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"fmt"
	"time"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/retention"
)

// Пороги уборки ленты (З10). Величины петли — retention.DefaultConfig().
const (
	// ClosedRetention — сколько закрытая строка живёт после отметки закрытия.
	// Читатели: оператор, разбирающий исход письма (та же причина, что у
	// outbox.DeliveredRetention), и предикат LimitRefundedPredicate у NTF-2,
	// который удалённую строку толкует как «лимит не возвращён, момент
	// считается». Поэтому ClosedRetention НЕ КОРОЧЕ самого длинного окна, которое
	// читатель судит через предикат (УК81); соотношение держит проба читателя
	// по этой константе.
	ClosedRetention = 7 * 24 * time.Hour
	// WindowRetention — сколько строка окна живёт после конца окна: запас на
	// чтение счётчика оператором. Put пишет только в текущее окно.
	WindowRetention = 24 * time.Hour
)

// closedSweepSQL — уборка закрытых строк (УК76, УК82): партия в порядке
// частичного индекса (outcome_at) WHERE state <> 'pending'; pending не
// снимается ни при каком возрасте — у неё нет отметки закрытия. Вклад уходит
// каскадом.
func closedSweepSQL(svc string) string {
	return fmt.Sprintf(`DELETE FROM %[1]s WHERE id IN (
  SELECT id FROM %[1]s
   WHERE state <> 'pending' AND outcome_at < now() - make_interval(secs => $1)
   ORDER BY outcome_at, id
   LIMIT $2)`, tablename.Of(svc, tablename.Outbox))
}

// RetentionSubjects — два предмета петли corelib/retention для ленты службы
// svc (З10, УК93, УК94): закрытые строки и прошедшие окна. Имя предмета — имя
// таблицы по договору поля Subject.Name. Уборка прошедших окон берёт строки
// SKIP LOCKED и ни на ком не ждёт (CX1-63); занятую строку снимает следующий
// проход.
func RetentionSubjects(db DB, svc string) []retention.Subject {
	return []retention.Subject{
		{
			Name:  tablename.Of(svc, tablename.Outbox),
			Grace: ClosedRetention,
			Sweep: func(ctx context.Context, grace time.Duration, batch int) (int64, bool, error) {
				tag, err := db.Exec(ctx, closedSweepSQL(svc), grace.Seconds(), batch)
				if err != nil {
					return 0, false, err
				}
				return tag.RowsAffected(), tag.RowsAffected() == int64(batch), nil
			},
		},
		{
			Name:  tablename.Of(svc, tablename.Window),
			Grace: WindowRetention,
			Sweep: func(ctx context.Context, grace time.Duration, batch int) (int64, bool, error) {
				tag, err := db.Exec(ctx, fmt.Sprintf(`DELETE FROM %[1]s
 WHERE (template, scope, window_seconds, key, window_start) IN (
  SELECT template, scope, window_seconds, key, window_start FROM %[1]s
   WHERE window_start + make_interval(secs => window_seconds) < now() - make_interval(secs => $1)
   ORDER BY window_start
   LIMIT $2
     FOR UPDATE SKIP LOCKED)`, tablename.Of(svc, tablename.Window)), grace.Seconds(), batch)
				if err != nil {
					return 0, false, err
				}
				return tag.RowsAffected(), tag.RowsAffected() == int64(batch), nil
			},
		},
	}
}
