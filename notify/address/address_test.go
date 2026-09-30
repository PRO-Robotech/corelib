// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package address_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/address"
)

// NTF1-B27: два написания одного ящика — один ключ; локальная часть без
// изменений, домен — ASCII-форма IDNA профиля пакета.
func TestNormalizeGivesOneKeyPerMailbox(t *testing.T) {
	cases := []struct{ in, want string }{
		{"User@Example.Invalid", "User@example.invalid"},
		{"User@example.invalid", "User@example.invalid"},
		{"user@example.invalid", "user@example.invalid"},
		{"user@bücher.example.invalid", "user@xn--bcher-kva.example.invalid"},
		{"user@xn--bcher-kva.example.invalid", "user@xn--bcher-kva.example.invalid"},
	}
	for _, c := range cases {
		n, err := address.Normalize(c.in)
		require.NoError(t, err, c.in)
		got, err := n.Value()
		require.NoError(t, err)
		require.Equal(t, c.want, got, c.in)
	}
	// близнец: локальная часть различается — два ящика (Р15 NTF-4)
	a, err := address.Normalize("user@example.invalid")
	require.NoError(t, err)
	b, err := address.Normalize("User@example.invalid")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

// NTF1-B27: адрес, который Normalize не разбирает, — ErrMalformed; ошибка
// значения не несёт.
func TestNormalizeRejectsMalformed(t *testing.T) {
	for _, in := range []string{
		"",
		"user",                       // нет «@»
		"user@",                      // пустой домен
		"@example.invalid",           // пустая локальная часть
		"us\r\ner@example.invalid",   // CR LF в локальной части
		"us\x00er@example.invalid",   // C0
		"us\x7fer@example.invalid",   // DEL
		"us\u0085er@example.invalid", // C1
		"user@exa\r\nmple.invalid",   // CR LF в домене
		"user@exa mple.invalid",      // домен вне профиля
		"user@-example.invalid",      // метка с дефисом в начале
		"us\xffer@example.invalid",   // не UTF-8
	} {
		n, err := address.Normalize(in)
		require.ErrorIs(t, err, address.ErrMalformed, "%q", in)
		require.Equal(t, address.Normalized{}, n, "%q: при ошибке — нулевое значение", in)
		if in != "" {
			require.NotContains(t, err.Error(), in, "ошибка не несёт значения")
		}
	}
}

// NTF1-B27 (нуль): Value() нулевых значений — ErrUnset и пустая строка;
// близнец — значение из Normalize.
func TestZeroValuesAnswerErrUnset(t *testing.T) {
	s, err := address.Normalized{}.Value()
	require.ErrorIs(t, err, address.ErrUnset)
	require.Empty(t, s)
	s, err = address.Domain{}.Value()
	require.ErrorIs(t, err, address.ErrUnset)
	require.Empty(t, s)

	n, err := address.Normalize("user@example.invalid")
	require.NoError(t, err)
	s, err = n.Value()
	require.NoError(t, err)
	require.Equal(t, "user@example.invalid", s)

	d, err := address.NormalizeDomain("Example.Invalid")
	require.NoError(t, err)
	s, err = d.Value()
	require.NoError(t, err)
	require.Equal(t, "example.invalid", s)
}

// NormalizeDomain — доменная часть Normalize: одна функция на оба пути.
func TestNormalizeTakesDomainFromNormalizeDomain(t *testing.T) {
	for _, dom := range []string{"Example.Invalid", "bücher.example.invalid", "straße.example.invalid"} {
		d, err := address.NormalizeDomain(dom)
		require.NoError(t, err)
		dv, err := d.Value()
		require.NoError(t, err)
		n, err := address.Normalize("x@" + dom)
		require.NoError(t, err)
		nv, err := n.Value()
		require.NoError(t, err)
		require.Equal(t, "x@"+dv, nv)
	}
	_, err := address.NormalizeDomain("")
	require.ErrorIs(t, err, address.ErrMalformed)
}

// З1: address не импортирует прочие notify/*.
func TestAddressDependsOnNoOtherNotifyPackage(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err)
	deps := strings.Fields(string(out))
	t.Logf("зависимостей notify/address: %d", len(deps))
	require.Contains(t, deps, "github.com/PRO-Robotech/corelib/notify/address", "перепись зависимостей пуста — это не зелёный")
	for _, d := range deps {
		if strings.HasPrefix(d, "github.com/PRO-Robotech/corelib/notify/") && d != "github.com/PRO-Robotech/corelib/notify/address" {
			t.Errorf("notify/address тянет %s", d)
		}
	}
	for _, banned := range []string{"net/smtp", "mime/multipart", "html/template", "text/template"} {
		require.NotContains(t, deps, banned)
	}
}
