// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package form

import (
	"fmt"
	"time"
)

// timestampLayout — одно написание отметки времени в ленте: RFC 3339, UTC,
// секунды (2026-09-30T00:00:00Z).
const timestampLayout = "2006-01-02T15:04:05Z"

// FormatTimestamp — единственный писатель написания ленты: приводит к UTC и
// усекает до секунды. Год вне [0000..9999] даёт строку, которую ParseTimestamp
// не принимает, — так диапазон отвергается без отдельной проверки.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(timestampLayout)
}

// ParseTimestamp — единственный читатель написания ленты. Принимает ровно одно
// написание: второе написание той же отметки (смещение, доли секунды, строчные
// буквы) — ErrMalformed; пустая строка — ErrEmpty.
func ParseTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, ErrEmpty
	}
	at, err := time.Parse(timestampLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: отметка времени не в написании ленты (RFC 3339, UTC, секунды)", ErrMalformed)
	}
	if at.Format(timestampLayout) != s {
		return time.Time{}, fmt.Errorf("%w: второе написание отметки времени", ErrMalformed)
	}
	return at.UTC(), nil
}
