// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package form_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/form"
)

// NTF1-B28 (нуль): Value() нулевого значения каждого из трёх типов — ErrUnset;
// близнец — значение из функции пакета возвращает строку и nil.
func TestZeroValueOfEachOpaqueTypeIsUnset(t *testing.T) {
	zeros := map[string]func() (string, error){
		"form.Path":       form.Path{}.Value,
		"form.Token":      form.Token{}.Value,
		"form.HeaderText": form.HeaderText{}.Value,
	}
	for name, value := range zeros {
		s, err := value()
		require.ErrorIs(t, err, form.ErrUnset, "%s{}.Value()", name)
		require.Empty(t, s, "%s{}.Value() не выдаёт строки", name)
	}

	p, err := form.ParsePath("/a")
	require.NoError(t, err)
	s, err := p.Value()
	require.NoError(t, err)
	require.Equal(t, "/a", s)
}

// NTF1-B29 (тема): пустая тема — ErrEmpty, значения нет; близнец — "probe".
func TestHeaderTextEmptyIsRefused(t *testing.T) {
	h, err := form.ParseHeaderText("")
	require.ErrorIs(t, err, form.ErrEmpty)
	_, verr := h.Value()
	require.ErrorIs(t, verr, form.ErrUnset, "отвергнутое значение — нулевое")

	h, err = form.ParseHeaderText("probe")
	require.NoError(t, err)
	s, err := h.Value()
	require.NoError(t, err)
	require.Equal(t, "probe", s)
}

// Р7: значение темы — без CR, LF и иных управляющих символов C0 и DEL.
func TestHeaderTextRefusesControlCharacters(t *testing.T) {
	for _, bad := range []string{"a\rb", "a\nb", "a\tb", "a\x00b", "a\x1fb", "a\x7fb", "\xff"} {
		_, err := form.ParseHeaderText(bad)
		require.ErrorIs(t, err, form.ErrMalformed, "%q", bad)
		require.NotContains(t, err.Error(), bad, "ошибка значения не несёт")
	}
	for _, good := range []string{"probe", "Код восстановления доступа", "a b ~ !"} {
		_, err := form.ParseHeaderText(good)
		require.NoError(t, err, "%q", good)
	}
}

// Р7: форма пути — три границы и запрещённые знаки.
func TestPathFormBoundaries(t *testing.T) {
	accepted := []string{"/", "/a", "/a/b", "/iam/invitations/accept", "/A-z_0.9~", "/a.b", "/..a", "/" + strings.Repeat("a", 1023)}
	for _, s := range accepted {
		p, err := form.ParsePath(s)
		require.NoError(t, err, "%q", s)
		v, err := p.Value()
		require.NoError(t, err)
		require.Equal(t, s, v)
	}
	refused := []string{
		"/a/", "//a", "/a//b", "a", "/.", "/..", "/a/./b", "/a/../b",
		"https://example.invalid/x", "//example.invalid/x", "/a?b", "/a#b", "/a%2f", `/a\b`, "/a b", "/a\nb",
		"/" + strings.Repeat("a", 1024),
	}
	for _, s := range refused {
		_, err := form.ParsePath(s)
		require.ErrorIs(t, err, form.ErrMalformed, "%q", s)
	}
	_, err := form.ParsePath("")
	require.ErrorIs(t, err, form.ErrEmpty)
}

// Р7: токен — алфавит [A-Za-z0-9_-], длина [16..512].
func TestTokenFormBoundaries(t *testing.T) {
	for _, s := range []string{strings.Repeat("a", 16), strings.Repeat("Z", 512), "0123456789abcdefghijklmnopqrstuv", "ab-_cdEF01234567"} {
		_, err := form.ParseToken(s)
		require.NoError(t, err, "%q", s)
	}
	for _, s := range []string{strings.Repeat("a", 15), strings.Repeat("a", 513), "0123456789abcde=", "0123456789abcde.", "0123456789abcde ", "0123456789abcdeё"} {
		_, err := form.ParseToken(s)
		require.ErrorIs(t, err, form.ErrMalformed, "%q", s)
	}
	_, err := form.ParseToken("")
	require.ErrorIs(t, err, form.ErrEmpty)
}

// З2, CX1-44: одно написание отметки времени — RFC 3339, UTC, секунды.
func TestTimestampHasOneWriting(t *testing.T) {
	at := time.Date(2026, 9, 30, 3, 0, 0, 500_000_000, time.FixedZone("msk", 3*3600))
	require.Equal(t, "2026-09-30T00:00:00Z", form.FormatTimestamp(at))

	got, err := form.ParseTimestamp("2026-09-30T00:00:00Z")
	require.NoError(t, err)
	require.True(t, got.Equal(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, time.UTC, got.Location())

	for _, second := range []string{
		"2026-09-30T03:00:00+03:00", // второе написание той же отметки (NTF1-G25)
		"2026-09-30T00:00:00.5Z",
		"2026-09-30T00:00:00.000Z",
		"2026-09-30 00:00:00Z",
		"2026-09-30t00:00:00z",
		"10000-01-01T00:00:00Z",
		"-0001-01-01T00:00:00Z",
		"not-a-time",
	} {
		_, err := form.ParseTimestamp(second)
		require.ErrorIs(t, err, form.ErrMalformed, "%q", second)
	}
	_, err = form.ParseTimestamp("")
	require.ErrorIs(t, err, form.ErrEmpty)
}

// УК51, УК56, CX1-44 (в): год вне [0000..9999] отвергает та же функция
// написания, без отдельной проверки диапазона.
func TestTimestampYearOutsideTheWritingIsRefused(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		_, present, err := form.Presence(form.KindTimestamp, at)
		require.ErrorIs(t, err, form.ErrMalformed, "%v", at)
		require.Equal(t, form.Absent, present)
	}
	// близнец: граница 0000 и 9999 — внутри написания
	for _, at := range []time.Time{
		time.Date(0, 1, 1, 0, 0, 1, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC),
	} {
		v, present, err := form.Presence(form.KindTimestamp, at)
		require.NoError(t, err, "%v", at)
		require.Equal(t, form.Given, present)
		s, err := v.Value()
		require.NoError(t, err)
		require.Equal(t, form.FormatTimestamp(at), s)
	}
}

// УК58, УК66, CX1-44 (а), CX1-50: «нормализовать → решить → проверить».
// time.Time{} + 0,5 с после нормализации — нуль, то есть «не задано», а не
// ключ 0001-01-01T00:00:00Z; близнец — ненулевая отметка с долей усекается и
// остаётся заданной.
func TestTimestampZeroPlusFractionIsAbsent(t *testing.T) {
	v, present, err := form.Presence(form.KindTimestamp, time.Time{}.Add(500*time.Millisecond))
	require.NoError(t, err)
	require.Equal(t, form.Absent, present)
	_, verr := v.Value()
	require.ErrorIs(t, verr, form.ErrUnset)

	_, err = form.Require(form.KindTimestamp, time.Time{}.Add(500*time.Millisecond))
	require.ErrorIs(t, err, form.ErrEmpty)

	at := time.Date(2026, 9, 30, 0, 0, 0, 999_000_000, time.UTC)
	v, present, err = form.Presence(form.KindTimestamp, at)
	require.NoError(t, err)
	require.Equal(t, form.Given, present)
	s, err := v.Value()
	require.NoError(t, err)
	require.Equal(t, "2026-09-30T00:00:00Z", s)

	// в notify та же функция читает строку ленты: нулевое написание — нуль.
	_, present, err = form.Presence(form.KindTimestamp, "0001-01-01T00:00:00Z")
	require.NoError(t, err)
	require.Equal(t, form.Absent, present)
	_, _, err = form.Presence(form.KindTimestamp, "2026-09-30T03:00:00+03:00")
	require.ErrorIs(t, err, form.ErrMalformed)
}

// Сырое значение не того Go-типа для вида — вне формы, а не «принято».
func TestPresenceRefusesARawOfTheWrongGoType(t *testing.T) {
	for kind, raw := range map[form.Kind]any{
		form.KindText:      42,
		form.KindSecret:    []byte("x"),
		form.KindPath:      time.Now(),
		form.KindToken:     nil,
		form.KindTimestamp: 1.5,
	} {
		_, _, err := form.Presence(kind, raw)
		require.ErrorIs(t, err, form.ErrMalformed, "%s", kind)
	}
}

// Ошибка формы не несёт значения: вызывающий называет атрибут, а значение —
// нет (NTF1-B28).
func TestFormErrorsCarryNoValue(t *testing.T) {
	const secretish = "/leak//me"
	_, _, err := form.Presence(form.KindPath, secretish)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "leak")

	_, _, err = form.Presence(form.KindText, "\xffsecret")
	require.ErrorIs(t, err, form.ErrMalformed)
	require.NotContains(t, err.Error(), "secret")
	require.True(t, errors.Is(err, form.ErrMalformed))
}
