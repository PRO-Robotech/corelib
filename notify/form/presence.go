// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package form

import (
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"
)

// Present — решение Presence: задано ли значение.
type Present bool

// Исходы Presence.
const (
	// Given — значение задано и проверено по форме.
	Given Present = true
	// Absent — значение не задано: нуль своего вида после нормализации.
	Absent Present = false
)

// Value — значение атрибута в написании ленты, проверенное по форме своего
// вида. Строит его только Presence (и Require); нулевое значение — «не
// задано». Выходы закрыты так же, как у Path: значение секрета не уходит ни в
// fmt, ни в slog, ни в json.
type Value struct {
	kind Kind
	v    string
	set  bool
}

// Kind — вид значения.
func (v Value) Kind() Kind { return v.kind }

// Value — написание значения в ленте; единственный выход. Нулевое значение —
// ErrUnset.
func (v Value) Value() (string, error) { return valueOf(v.v, v.set) }

// Format закрывает выход через fmt при любом глаголе и флаге.
func (v Value) Format(f fmt.State, _ rune) { writeNeutral(f, "form.Value", v.set) }

// LogValue закрывает выход через slog.
func (v Value) LogValue() slog.Value { return slog.StringValue(neutral("form.Value", v.set)) }

// MarshalJSON закрывает выход через encoding/json.
func (Value) MarshalJSON() ([]byte, error) { return nil, ErrNotSerializable }

// MarshalText закрывает выход через encoding.TextMarshaler.
func (Value) MarshalText() ([]byte, error) { return nil, ErrNotSerializable }

// AppendText закрывает выход через encoding.TextAppender.
func (Value) AppendText(b []byte) ([]byte, error) { return b, ErrNotSerializable }

// Presence — ЕДИНСТВЕННАЯ функция, решающая «нуль ли это» (З2, CX1-44,
// CX1-50). Порядок несущий: нормализовать → решить → проверить. Решение
// принимается по нормализованному значению, поэтому time.Time{} + 0,5 с — нуль,
// а не ключ 0001-01-01T00:00:00Z.
//
// raw по виду:
//   - text, secret, path, token — string; пустая строка — Absent;
//   - timestamp — time.Time (источник: UTC, усечение до секунды, IsZero) либо
//     string в написании ленты (notify: ParseTimestamp, нулевая отметка —
//     Absent).
//
// Вид вне перечня — ErrUnknownType при любом raw; raw не того Go-типа —
// ErrMalformed. Absent возвращается без ошибки: судить нуль у required —
// дело вызывающего (Require либо сторож feed.Put).
func Presence(kind Kind, raw any) (Value, Present, error) {
	switch kind {
	case KindText, KindSecret:
		s, err := rawString(kind, raw)
		if err != nil || s == "" {
			return Value{}, Absent, err
		}
		if !utf8.ValidString(s) {
			return Value{}, Absent, fmt.Errorf("%w: значение вида %s — не UTF-8", ErrMalformed, kind)
		}
		return Value{kind: kind, v: s, set: true}, Given, nil
	case KindPath:
		s, err := rawString(kind, raw)
		if err != nil || s == "" {
			return Value{}, Absent, err
		}
		if err := checkPath(s); err != nil {
			return Value{}, Absent, err
		}
		return Value{kind: kind, v: s, set: true}, Given, nil
	case KindToken:
		s, err := rawString(kind, raw)
		if err != nil || s == "" {
			return Value{}, Absent, err
		}
		if err := checkToken(s); err != nil {
			return Value{}, Absent, err
		}
		return Value{kind: kind, v: s, set: true}, Given, nil
	case KindTimestamp:
		return timestampPresence(raw)
	}
	return Value{}, Absent, fmt.Errorf("%w: %q", ErrUnknownType, string(kind))
}

// Require — Presence для атрибута, у которого нуль не значение: Absent —
// ErrEmpty. Решение «нуль ли это» принимает Presence, второго нет.
func Require(kind Kind, raw any) (Value, error) {
	v, present, err := Presence(kind, raw)
	if err != nil {
		return Value{}, err
	}
	if present == Absent {
		return Value{}, ErrEmpty
	}
	return v, nil
}

func rawString(kind Kind, raw any) (string, error) {
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w: значение вида %s — не строка (%T)", ErrMalformed, kind, raw)
	}
	return s, nil
}

func timestampPresence(raw any) (Value, Present, error) {
	var at time.Time
	switch r := raw.(type) {
	case time.Time:
		at = r.UTC().Truncate(time.Second)
	case string:
		if r == "" {
			return Value{}, Absent, nil
		}
		parsed, err := ParseTimestamp(r)
		if err != nil {
			return Value{}, Absent, err
		}
		at = parsed
	default:
		return Value{}, Absent, fmt.Errorf("%w: значение вида timestamp — не time.Time и не строка (%T)", ErrMalformed, raw)
	}
	if at.IsZero() {
		return Value{}, Absent, nil
	}
	// Проверка — через написание ленты той же функцией, что читает notify:
	// год вне [0000..9999] отвергается здесь, без отдельной проверки диапазона.
	written := FormatTimestamp(at)
	back, err := ParseTimestamp(written)
	if err != nil {
		return Value{}, Absent, err
	}
	if !back.Equal(at) {
		return Value{}, Absent, fmt.Errorf("%w: отметка времени не переживает написание ленты", ErrMalformed)
	}
	return Value{kind: KindTimestamp, v: written, set: true}, Given, nil
}
