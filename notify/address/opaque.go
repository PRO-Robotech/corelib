// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package address

import (
	"fmt"
	"log/slog"
)

// neutral — нейтральная форма непрозрачного значения: имя типа и признак
// «задано», без значения.
func neutral(name, v string) string {
	if v != "" {
		return name + "(set)"
	}
	return name + "(unset)"
}

func writeNeutral(f fmt.State, name, v string) {
	// Ошибку записи в fmt.State вернуть некуда: интерфейс Formatter её не несёт.
	_, _ = f.Write([]byte(neutral(name, v)))
}

// Format закрывает выход через fmt при любом глаголе и флаге.
func (n Normalized) Format(f fmt.State, _ rune) { writeNeutral(f, "address.Normalized", n.v) }

// Format закрывает выход через fmt при любом глаголе и флаге.
func (d Domain) Format(f fmt.State, _ rune) { writeNeutral(f, "address.Domain", d.v) }

// LogValue закрывает выход через slog.
func (n Normalized) LogValue() slog.Value {
	return slog.StringValue(neutral("address.Normalized", n.v))
}

// LogValue закрывает выход через slog.
func (d Domain) LogValue() slog.Value { return slog.StringValue(neutral("address.Domain", d.v)) }

// MarshalJSON закрывает выход через encoding/json.
func (Normalized) MarshalJSON() ([]byte, error) { return nil, ErrNotSerializable }

// MarshalText закрывает выход через encoding.TextMarshaler.
func (Normalized) MarshalText() ([]byte, error) { return nil, ErrNotSerializable }

// AppendText закрывает выход через encoding.TextAppender.
func (Normalized) AppendText(b []byte) ([]byte, error) { return b, ErrNotSerializable }

// MarshalJSON закрывает выход через encoding/json.
func (Domain) MarshalJSON() ([]byte, error) { return nil, ErrNotSerializable }

// MarshalText закрывает выход через encoding.TextMarshaler.
func (Domain) MarshalText() ([]byte, error) { return nil, ErrNotSerializable }

// AppendText закрывает выход через encoding.TextAppender.
func (Domain) AppendText(b []byte) ([]byte, error) { return b, ErrNotSerializable }
