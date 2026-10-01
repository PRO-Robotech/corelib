// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed"
)

func key(id byte, fill byte) feed.Key {
	return feed.Key{ID: id, Secret: bytes.Repeat([]byte{fill}, 32)}
}

func ring(t *testing.T, active feed.Key, previous ...feed.Key) *feed.Keyring {
	t.Helper()
	r, err := feed.NewKeyring(active, previous...)
	require.NoError(t, err)
	return r
}

// З11: шифротекст несёт идентификатор ключа и не несёт открытого текста;
// открывается тем же кольцом на том же (служба, id, шаблон).
func TestZ11_SealCarriesTheKeyIDAndOpensBack(t *testing.T) {
	k1 := ring(t, key(1, 0xA1))
	ct, err := k1.Seal("probe", "ntf-a", "probe-all", []byte(`{"code":"s3cr3t-B03"}`))
	require.NoError(t, err)
	require.Equal(t, byte(1), ct[0], "первый байт — идентификатор ключа")
	require.False(t, bytes.Contains(ct, []byte("s3cr3t-B03")))
	pt, err := k1.Open("probe", "ntf-a", "probe-all", ct)
	require.NoError(t, err)
	require.Equal(t, `{"code":"s3cr3t-B03"}`, string(pt))
}

// УК19, CX1-19: AAD — (служба, id, шаблон) с длиной каждой части. Перенос в
// другую строку и смена шаблона при прежнем шифротексте — sealed_mismatch;
// раскладка, дающая ту же склейку частей без длин, тоже не открывается.
func TestUK19_AADBindsRowTemplateAndTable(t *testing.T) {
	r := ring(t, key(1, 0xA1))
	ct, err := r.Seal("probe", "ntf-a", "probe-all", []byte("x"))
	require.NoError(t, err)

	for name, args := range map[string][3]string{
		"другая строка (B04)":           {"probe", "ntf-b", "probe-all"},
		"другой шаблон (УК19)":          {"probe", "ntf-a", "probe-hello"},
		"другая таблица":                {"probf", "ntf-a", "probe-all"},
		"та же склейка, другие границы": {"probe", "ntf-ap", "robe-all"},
	} {
		_, err := r.Open(args[0], args[1], args[2], ct)
		require.ErrorIs(t, err, feed.ErrSealedMismatch, name)
		require.NotErrorIs(t, err, feed.ErrKeyUnavailable, name)
	}
	// Близнец — те же части: открывается.
	_, err = r.Open("probe", "ntf-a", "probe-all", ct)
	require.NoError(t, err)
}

// NTF1-B06, B07: ротация — строка прежнего ключа открывается кольцом
// «активный K2, прежний K1», новые строки запечатаны K2; кольцо «только K2» —
// key_unavailable, перебора ключей нет.
func TestNTF1B06B07_RotationOpensPreviousAndRefusesRemoved(t *testing.T) {
	ct1, err := ring(t, key(1, 0xA1)).Seal("probe", "ntf-a", "t", []byte("x"))
	require.NoError(t, err)

	rotated := ring(t, key(2, 0xB2), key(1, 0xA1))
	pt, err := rotated.Open("probe", "ntf-a", "t", ct1)
	require.NoError(t, err)
	require.Equal(t, "x", string(pt))
	ct2, err := rotated.Seal("probe", "ntf-b", "t", []byte("y"))
	require.NoError(t, err)
	require.Equal(t, byte(2), ct2[0])

	only2 := ring(t, key(2, 0xB2))
	_, err = only2.Open("probe", "ntf-a", "t", ct1)
	require.ErrorIs(t, err, feed.ErrKeyUnavailable)
	require.NotErrorIs(t, err, feed.ErrSealedMismatch)

	// Ключ с тем же идентификатором, но другим материалом — не перебор, а
	// sealed_mismatch: идентификатор назвал ключ, и он не открыл.
	impostor := ring(t, key(1, 0xFF))
	_, err = impostor.Open("probe", "ntf-a", "t", ct1)
	require.ErrorIs(t, err, feed.ErrSealedMismatch)

	_, err = rotated.Open("probe", "ntf-a", "t", []byte{2})
	require.ErrorIs(t, err, feed.ErrSealedMismatch, "усечённый шифротекст")
}

func TestKeyringRefusesMalformedRings(t *testing.T) {
	for name, build := range map[string]func() error{
		"ключ короче 32 байт": func() error {
			_, err := feed.NewKeyring(feed.Key{ID: 1, Secret: make([]byte, 16)})
			return err
		},
		"прежний с идентификатором активного": func() error {
			_, err := feed.NewKeyring(key(1, 1), key(1, 2))
			return err
		},
		"два прежних": func() error {
			_, err := feed.NewKeyring(key(1, 1), key(2, 2), key(3, 3))
			return err
		},
	} {
		require.Error(t, build(), name)
	}
}

func keyringFile(active, previous string) string {
	enc := func(fill byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)) }
	s := fmt.Sprintf(`{"active":{"id":%s,"key":%q}`, active, enc(0xA1))
	if previous != "" {
		s += fmt.Sprintf(`,"previous":{"id":%s,"key":%q}`, previous, enc(0xB2))
	}
	return s + "}"
}

// NTF1-B05: ручка ключа — путь к секрету. Не задана, пуста, файл не читается
// или не по форме — отказ с именем ручки; задана — кольцо и строка самоотчёта
// с осью ключа.
func TestNTF1B05_KeyringKnobRefusesWithItsName(t *testing.T) {
	const knob = "KACHO_PROBE_NOTIFICATIONS_FEED_KEYRING"
	files := map[string]string{
		"/run/ok":      keyringFile("2", "1"),
		"/run/garbage": `{"active":{"id":1}}`,
		"/run/extra":   `{"active":{"id":1,"key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `"},"next":{}}`,
	}
	read := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("no such file")
	}
	for name, env := range map[string]map[string]string{
		"не задана":   {},
		"пустая":      {knob: ""},
		"нет файла":   {knob: "/run/none"},
		"не по форме": {knob: "/run/garbage"},
		"лишнее поле": {knob: "/run/extra"},
	} {
		lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
		_, err := feed.LoadKeyring(knob, lookup, read)
		require.Error(t, err, name)
		require.Contains(t, err.Error(), knob, name)
		require.NotContains(t, err.Error(), base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xA1}, 32)), "ключ в тексте ошибки")
	}

	r, err := feed.LoadKeyring(knob, func(k string) (string, bool) { return "/run/ok", k == knob }, read)
	require.NoError(t, err)
	report := r.Report()
	require.True(t, strings.HasPrefix(report, "feed-keyring "), report)
	require.Contains(t, report, "active=2")
	require.Contains(t, report, "previous=1")
	require.NotContains(t, report, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xA1}, 32)))
}
