// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package form

import (
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// Границы формы Р7.
const (
	pathMaxLen  = 1024
	tokenMinLen = 16
	tokenMaxLen = 512
)

// Path — путь ссылки внутри консоли установки (Р7): либо ровно «/», либо
// последовательность сегментов «/» + непустая строка из [A-Za-z0-9._~-], не
// равная «.» и «..»; длина [1..1024]. Строит его только ParsePath; нулевое
// значение — «не задано», Value() на нём отвечает ErrUnset.
type Path struct {
	v   string
	set bool
}

// Token — токен ссылки (Р7): алфавит [A-Za-z0-9_-], длина [16..512].
type Token struct {
	v   string
	set bool
}

// HeaderText — значение, входящее в заголовок письма (тему): непустое, без
// CR, LF и иных управляющих символов C0 и DEL, корректный UTF-8.
type HeaderText struct {
	v   string
	set bool
}

// ParsePath проверяет форму пути. Пустая строка — ErrEmpty, вне формы —
// ErrMalformed с названием правила.
func ParsePath(s string) (Path, error) {
	if err := checkPath(s); err != nil {
		return Path{}, err
	}
	return Path{v: s, set: true}, nil
}

// ParseToken проверяет форму токена. Пустая строка — ErrEmpty.
func ParseToken(s string) (Token, error) {
	if err := checkToken(s); err != nil {
		return Token{}, err
	}
	return Token{v: s, set: true}, nil
}

// ParseHeaderText проверяет значение заголовка. Пустое — ErrEmpty: пустой
// темы нет ни у источника, ни в notify (NTF1-B29 (тема)).
func ParseHeaderText(s string) (HeaderText, error) {
	if s == "" {
		return HeaderText{}, ErrEmpty
	}
	if !utf8.ValidString(s) {
		return HeaderText{}, fmt.Errorf("%w: заголовок — не UTF-8", ErrMalformed)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return HeaderText{}, fmt.Errorf("%w: заголовок несёт управляющий символ (C0 или DEL)", ErrMalformed)
		}
	}
	return HeaderText{v: s, set: true}, nil
}

// Value — единственный выход значения. Нулевое значение — ErrUnset.
func (p Path) Value() (string, error) { return valueOf(p.v, p.set) }

// Value — единственный выход значения. Нулевое значение — ErrUnset.
func (t Token) Value() (string, error) { return valueOf(t.v, t.set) }

// Value — единственный выход значения. Нулевое значение — ErrUnset.
func (h HeaderText) Value() (string, error) { return valueOf(h.v, h.set) }

func valueOf(v string, set bool) (string, error) {
	if !set {
		return "", ErrUnset
	}
	return v, nil
}

func checkPath(s string) error {
	switch {
	case s == "":
		return ErrEmpty
	case len(s) > pathMaxLen:
		return fmt.Errorf("%w: путь длиннее %d байт", ErrMalformed, pathMaxLen)
	case s[0] != '/':
		return fmt.Errorf("%w: путь начинается с «/»; схемы и узла нет — origin задаёт установка", ErrMalformed)
	case s == "/":
		return nil
	}
	for _, seg := range strings.Split(s[1:], "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: пустой сегмент пути (хвостовая «/» или «//»)", ErrMalformed)
		case ".", "..":
			return fmt.Errorf("%w: сегмент пути «.» или «..»", ErrMalformed)
		}
		for i := 0; i < len(seg); i++ {
			if !isUnreserved(seg[i]) {
				return fmt.Errorf("%w: знак вне [A-Za-z0-9._~-] в сегменте пути", ErrMalformed)
			}
		}
	}
	return nil
}

func checkToken(s string) error {
	switch {
	case s == "":
		return ErrEmpty
	case len(s) < tokenMinLen || len(s) > tokenMaxLen:
		return fmt.Errorf("%w: длина токена вне [%d..%d]", ErrMalformed, tokenMinLen, tokenMaxLen)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isAlnum(c) && c != '_' && c != '-' {
			return fmt.Errorf("%w: знак токена вне [A-Za-z0-9_-]", ErrMalformed)
		}
	}
	return nil
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

func isUnreserved(c byte) bool {
	return isAlnum(c) || c == '.' || c == '_' || c == '~' || c == '-'
}

// neutral — нейтральная форма непрозрачного значения: имя типа и признак
// «задано», без значения.
func neutral(name string, set bool) string {
	if set {
		return name + "(set)"
	}
	return name + "(unset)"
}

// Format закрывает выход через fmt при любом глаголе и флаге.
func (p Path) Format(f fmt.State, _ rune) { writeNeutral(f, "form.Path", p.set) }

// Format закрывает выход через fmt при любом глаголе и флаге.
func (t Token) Format(f fmt.State, _ rune) { writeNeutral(f, "form.Token", t.set) }

// Format закрывает выход через fmt при любом глаголе и флаге.
func (h HeaderText) Format(f fmt.State, _ rune) { writeNeutral(f, "form.HeaderText", h.set) }

func writeNeutral(f fmt.State, name string, set bool) {
	// Ошибку записи в fmt.State вернуть некуда: интерфейс Formatter её не несёт.
	_, _ = f.Write([]byte(neutral(name, set)))
}

// LogValue закрывает выход через slog.
func (p Path) LogValue() slog.Value { return slog.StringValue(neutral("form.Path", p.set)) }

// LogValue закрывает выход через slog.
func (t Token) LogValue() slog.Value { return slog.StringValue(neutral("form.Token", t.set)) }

// LogValue закрывает выход через slog.
func (h HeaderText) LogValue() slog.Value {
	return slog.StringValue(neutral("form.HeaderText", h.set))
}

// MarshalJSON закрывает выход через encoding/json.
func (Path) MarshalJSON() ([]byte, error) { return nil, ErrNotSerializable }

// MarshalText закрывает выход через encoding.TextMarshaler.
func (Path) MarshalText() ([]byte, error) { return nil, ErrNotSerializable }

// AppendText закрывает выход через encoding.TextAppender.
func (Path) AppendText(b []byte) ([]byte, error) { return b, ErrNotSerializable }

// MarshalJSON закрывает выход через encoding/json.
func (Token) MarshalJSON() ([]byte, error) { return nil, ErrNotSerializable }

// MarshalText закрывает выход через encoding.TextMarshaler.
func (Token) MarshalText() ([]byte, error) { return nil, ErrNotSerializable }

// AppendText закрывает выход через encoding.TextAppender.
func (Token) AppendText(b []byte) ([]byte, error) { return b, ErrNotSerializable }

// MarshalJSON закрывает выход через encoding/json.
func (HeaderText) MarshalJSON() ([]byte, error) { return nil, ErrNotSerializable }

// MarshalText закрывает выход через encoding.TextMarshaler.
func (HeaderText) MarshalText() ([]byte, error) { return nil, ErrNotSerializable }

// AppendText закрывает выход через encoding.TextAppender.
func (HeaderText) AppendText(b []byte) ([]byte, error) { return b, ErrNotSerializable }
