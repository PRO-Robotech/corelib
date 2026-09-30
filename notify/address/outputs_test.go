// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package address_test

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/address"
)

type opaque struct {
	name  string
	value any
	raw   []string // написания, которых в выходах быть не должно
	read  func() (string, error)
	want  string
}

func opaqueValues(t *testing.T) []opaque {
	t.Helper()
	n, err := address.Normalize("mailbox-marker@domain-marker.invalid")
	require.NoError(t, err)
	d, err := address.NormalizeDomain("domain-marker.invalid")
	require.NoError(t, err)
	return []opaque{
		{"address.Normalized", n, []string{"mailbox-marker", "domain-marker"}, n.Value, "mailbox-marker@domain-marker.invalid"},
		{"address.Domain", d, []string{"domain-marker"}, d.Value, "domain-marker.invalid"},
	}
}

// УК47, CX1-42: единственный выход значения — Value(). Четыре пути наружу —
// fmt, slog, json, html/template — адреса не несут, json отвечает ошибкой.
// Близнец — Value() возвращает строку.
func TestOpaqueTypesLeakThroughNoExit(t *testing.T) {
	for _, o := range opaqueValues(t) {
		t.Run(o.name, func(t *testing.T) {
			s, err := o.read()
			require.NoError(t, err)
			require.Equal(t, o.want, s)

			notLeaked := func(out, where string) {
				t.Helper()
				for _, r := range o.raw {
					require.NotContains(t, out, r, where)
				}
			}

			type holder struct{ Field any }
			for _, verb := range []string{"%v", "%s", "%+v", "%#v", "%q", "%x"} {
				for _, arg := range []any{o.value, &o.value, holder{o.value}} {
					notLeaked(fmt.Sprintf(verb, arg), "fmt "+verb)
				}
			}
			require.Contains(t, fmt.Sprint(o.value), o.name, "нейтральная форма называет тип")
			require.Contains(t, fmt.Sprint(o.value), "set", "нейтральная форма называет признак «задано»")

			for _, mk := range []func(*bytes.Buffer) slog.Handler{
				func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
				func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
			} {
				var buf bytes.Buffer
				slog.New(mk(&buf)).Info("probe", "attr", o.value, slog.Group("g", "nested", o.value))
				notLeaked(buf.String(), "slog")
				require.Contains(t, buf.String(), o.name)
			}

			out, err := json.Marshal(o.value)
			require.ErrorIs(t, err, address.ErrNotSerializable)
			notLeaked(string(out), "json")
			_, err = json.Marshal(map[string]any{"k": o.value})
			require.ErrorIs(t, err, address.ErrNotSerializable)
			m, ok := o.value.(encoding.TextMarshaler)
			require.True(t, ok, "%s не объявляет MarshalText — выход не закрыт явно", o.name)
			_, err = m.MarshalText()
			require.ErrorIs(t, err, address.ErrNotSerializable)
			a, ok := o.value.(encoding.TextAppender)
			require.True(t, ok, "%s не объявляет AppendText — выход не закрыт явно", o.name)
			_, err = a.AppendText(nil)
			require.ErrorIs(t, err, address.ErrNotSerializable)

			tpl := template.Must(template.New("x").Parse(`<p>{{.}}</p><a title="{{.}}">x</a>`))
			var buf bytes.Buffer
			require.NoError(t, tpl.Execute(&buf, o.value))
			notLeaked(buf.String(), "html/template")
			js := template.Must(template.New("js").Parse(`<script>var x = {{.}};</script>`))
			buf.Reset()
			_ = js.Execute(&buf, o.value)
			notLeaked(buf.String(), "html/template script")
		})
	}
}

// Нулевое значение в выходах — нейтральная форма «не задано».
func TestZeroOpaqueTypesPrintUnset(t *testing.T) {
	for _, v := range []any{address.Normalized{}, address.Domain{}} {
		require.Contains(t, fmt.Sprint(v), "unset")
	}
}
