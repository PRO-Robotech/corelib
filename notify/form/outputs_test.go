// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package form_test

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/form"
)

// opaque — значение непрозрачного типа и исходная строка, которой в выходах
// быть не должно.
type opaque struct {
	name  string
	value any
	raw   string
	read  func() (string, error)
}

func opaqueValues(t *testing.T) []opaque {
	t.Helper()
	p, err := form.ParsePath("/secret-path-marker")
	require.NoError(t, err)
	tok, err := form.ParseToken("tokenmarker0123456789")
	require.NoError(t, err)
	h, err := form.ParseHeaderText("header-marker")
	require.NoError(t, err)
	v, err := form.Require(form.KindSecret, "value-marker")
	require.NoError(t, err)
	return []opaque{
		{"form.Path", p, "/secret-path-marker", p.Value},
		{"form.Token", tok, "tokenmarker0123456789", tok.Value},
		{"form.HeaderText", h, "header-marker", h.Value},
		{"form.Value", v, "value-marker", v.Value},
	}
}

// УК47, CX1-42: единственный выход значения — Value(). Четыре пути наружу —
// fmt, slog, json, html/template — строки не несут, json отвечает ошибкой.
// Близнец — Value() возвращает строку.
func TestOpaqueTypesLeakThroughNoExit(t *testing.T) {
	for _, o := range opaqueValues(t) {
		t.Run(o.name, func(t *testing.T) {
			// близнец
			s, err := o.read()
			require.NoError(t, err)
			require.Equal(t, o.raw, s)

			// (1) fmt — прямо, по указателю и полем экспортируемой структуры
			type holder struct{ Field any }
			for _, verb := range []string{"%v", "%s", "%+v", "%#v", "%q", "%x"} {
				for _, arg := range []any{o.value, &o.value, holder{o.value}} {
					out := fmt.Sprintf(verb, arg)
					require.NotContains(t, out, o.raw, "fmt %s", verb)
				}
			}
			require.Contains(t, fmt.Sprint(o.value), o.name, "нейтральная форма называет тип")
			require.Contains(t, fmt.Sprint(o.value), "set", "нейтральная форма называет признак «задано»")

			// (2) slog — текстовый и JSON обработчики
			for _, mk := range []func(*bytes.Buffer) slog.Handler{
				func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
				func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
			} {
				var buf bytes.Buffer
				slog.New(mk(&buf)).Info("probe", "attr", o.value, slog.Group("g", "nested", o.value))
				require.NotContains(t, buf.String(), o.raw)
				require.Contains(t, buf.String(), o.name)
			}

			// (3) json.Marshal — ошибка, значения нет
			out, err := json.Marshal(o.value)
			require.Error(t, err)
			require.ErrorIs(t, err, form.ErrNotSerializable)
			require.NotContains(t, string(out), o.raw)
			_, err = json.Marshal(map[string]any{"k": o.value})
			require.ErrorIs(t, err, form.ErrNotSerializable)
			if m, ok := o.value.(encoding.TextMarshaler); ok {
				_, err = m.MarshalText()
				require.ErrorIs(t, err, form.ErrNotSerializable)
			} else {
				t.Fatalf("%s не объявляет MarshalText — выход не закрыт явно", o.name)
			}
			if a, ok := o.value.(encoding.TextAppender); ok {
				_, err = a.AppendText(nil)
				require.ErrorIs(t, err, form.ErrNotSerializable)
			} else {
				t.Fatalf("%s не объявляет AppendText — выход не закрыт явно", o.name)
			}

			// (4) подстановка в html/template — в тексте, атрибуте и скрипте
			tpl := template.Must(template.New("x").Parse(`<p>{{.}}</p><a title="{{.}}">x</a>`))
			var buf bytes.Buffer
			require.NoError(t, tpl.Execute(&buf, o.value))
			require.NotContains(t, buf.String(), o.raw)
			js := template.Must(template.New("js").Parse(`<script>var x = {{.}};</script>`))
			buf.Reset()
			err = js.Execute(&buf, o.value)
			require.NotContains(t, buf.String(), o.raw)
			if err == nil {
				t.Logf("%s в контексте скрипта: %s", o.name, buf.String())
			}
		})
	}
}

// Нулевое значение в выходах — нейтральная форма «не задано».
func TestZeroOpaqueTypesPrintUnset(t *testing.T) {
	for _, v := range []any{form.Path{}, form.Token{}, form.HeaderText{}} {
		require.Contains(t, fmt.Sprint(v), "unset")
	}
}
