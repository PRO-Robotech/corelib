// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec_test

import (
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

func fingerprintOf(t *testing.T, fsys fstest.MapFS) string {
	t.Helper()
	cat, _, fs := load(t, fsys)
	require.Empty(t, fs)
	require.Len(t, cat.Templates, 1)
	fp := spec.SetFingerprint(spec.SetOf(cat.Templates[0]))

	narrow, err := spec.ReadSetOnly(fsys, "invite")
	require.NoError(t, err)
	require.Equal(t, fp, spec.SetFingerprint(narrow), "узкое чтение даёт тот же набор, что валидатор")
	return fp
}

// CX1-45 (б): форма строки — v1:sha256:<hex>.
func TestFingerprintForm(t *testing.T) {
	fp := fingerprintOf(t, a01(t))
	require.Regexp(t, regexp.MustCompile(`^v1:sha256:[0-9a-f]{64}$`), fp)
}

// УК52 (а), УК56, CX1-45 (а): перестановка атрибутов и ключей, кавычки,
// комментарии и пробелы — отпечаток прежний; как и правка вне набора (текст,
// ttl, limits).
func TestFingerprintIgnoresEverythingOutsideTheSet(t *testing.T) {
	base := fingerprintOf(t, a01(t))

	permuted := a01(t)
	permuted["invite/notification.yaml"] = &fstest.MapFile{Data: []byte(`# та же форма другими словами
subject: {ru: 'Приглашение в облако'}
attributes:
  token:        {presence: "required", type: "token"}   # переставлено
  inviter_name:
    presence: required
    type: text
limits:
  - {max: 3, window: 24h, scope: recipient}
  - {scope: initiator, window: 1h, max: 20}
ttl: "168h"
class: "security"
name: 'invite'
`)}
	require.Equal(t, base, fingerprintOf(t, permuted))

	outside := a01(t)
	edit(t, outside, "invite/notification.yaml", "ttl: 168h", "ttl: 1h")
	edit(t, outside, "invite/notification.yaml", "max: 3", "max: 5")
	edit(t, outside, "invite/body.ru.yaml", "приглашает вас в облако.", "зовёт вас.")
	edit(t, outside, "invite/notification.yaml", `ru: "Приглашение в облако"`, `ru: "Вас зовут в облако"`)
	require.Equal(t, base, fingerprintOf(t, outside), "текст, ttl и limits в набор не входят")
}

// Каждое поле набора меняет отпечаток: имя, вид, обязательность, вхождение в
// тему атрибута и класс шаблона.
func TestFingerprintChangesWithEachFieldOfTheSet(t *testing.T) {
	base := fingerprintOf(t, a01(t))
	seen := map[string]string{"base": base}
	variants := map[string]func(fsys fstest.MapFS){
		"имя атрибута": func(f fstest.MapFS) {
			edit(t, f, "invite/notification.yaml", "inviter_name: {", "inviter: {")
			edit(t, f, "invite/body.ru.yaml", "{{ inviter_name }}", "{{ inviter }}")
		},
		"вид атрибута": func(f fstest.MapFS) {
			edit(t, f, "invite/notification.yaml", "inviter_name: {type: text", "inviter_name: {type: secret")
			edit(t, f, "invite/body.ru.yaml", `  - p: "{{ inviter_name }} приглашает вас в облако."`, `  - code: "{{ inviter_name }}"`)
		},
		"обязательность": func(f fstest.MapFS) {
			edit(t, f, "invite/notification.yaml", "inviter_name: {type: text, presence: required}", "inviter_name: {type: text, presence: optional}")
			edit(t, f, "invite/body.ru.yaml", `приглашает вас в облако."`, "приглашает вас в облако.\"\n    when: inviter_name")
		},
		"вхождение в тему": func(f fstest.MapFS) {
			edit(t, f, "invite/notification.yaml", `ru: "Приглашение в облако"`, `ru: "Приглашение от {{ inviter_name }}"`)
		},
		"класс": func(f fstest.MapFS) {
			edit(t, f, "invite/notification.yaml", "class: security", "class: notice")
		},
	}
	for name, mutate := range variants {
		fsys := a01(t)
		mutate(fsys)
		fp := fingerprintOf(t, fsys)
		for prev, v := range seen {
			require.NotEqual(t, v, fp, "%s: отпечаток совпал с %s", name, prev)
		}
		seen[name] = fp
	}
}

// УК52 (б), УК56: «набор тот же» решается пересчётом. База, чей revision.yaml
// записан прежней версией алгоритма, при том же наборе — тот же набор;
// записанная строка отпечатка в решении не участвует.
func TestSameSetIsDecidedByRecomputationNotByTheWrittenString(t *testing.T) {
	cur := a01(t)
	curCat, _, fs := load(t, cur)
	require.Empty(t, fs)
	require.Len(t, curCat.Templates, 1)

	base := a01(t)
	edit(t, base, "invite/revision.yaml", "fingerprint: v1:sha256:", "fingerprint: v0:sha256:")
	baseRev, err := spec.ReadRevision(base["invite/revision.yaml"].Data)
	require.NoError(t, err, "revision.yaml прежней версии алгоритма читается")
	require.NotEqual(t, spec.SetFingerprint(spec.SetOf(curCat.Templates[0])), baseRev.Fingerprint)

	baseSet, err := spec.ReadSetOnly(base, "invite")
	require.NoError(t, err)
	require.True(t, spec.SameSet(baseSet, spec.SetOf(curCat.Templates[0])), "набор тот же")

	// близнец: иной набор — не тот же
	edit(t, base, "invite/notification.yaml", "class: security", "class: notice")
	baseSet, err = spec.ReadSetOnly(base, "invite")
	require.NoError(t, err)
	require.False(t, spec.SameSet(baseSet, spec.SetOf(curCat.Templates[0])))
}

// УК52 (в), УК56: база прежнего формата читается узким чтением — ключи и
// правила, которых текущий формат не знает или которые ужесточил, базу
// «ненайденной» не делают.
func TestReadSetOnlyReadsABaseOfAnEarlierFormat(t *testing.T) {
	base := fstest.MapFS{
		"invite/notification.yaml": {Data: []byte(`name: invite
class: security
legacy_hint: ключ, которого текущий формат не знает
ttl: 900h
attributes:
  inviter_name: {type: text, presence: required, note: прежнее поле}
  token: {type: token, presence: required}
subject:
  ru: "Приглашение в облако"
`)},
	}
	_, _, err := spec.LoadFS(base, ".")
	require.Error(t, err, "текущий валидатор такую базу отвергает")

	set, err := spec.ReadSetOnly(base, "invite")
	require.NoError(t, err)
	require.Equal(t, spec.ClassSecurity, set.Class)
	require.Len(t, set.Attrs, 2)
	require.Equal(t, fingerprintOf(t, a01(t)), spec.SetFingerprint(set))

	// без набора базу прочесть нельзя: класс и атрибуты — её содержимое
	_, err = spec.ReadSetOnly(fstest.MapFS{"invite/notification.yaml": {Data: []byte("name: invite\n")}}, "invite")
	require.Error(t, err)
	_, err = spec.ReadSetOnly(fstest.MapFS{}, "invite")
	require.Error(t, err)
}

// УК52 (г): revision.yaml пишет и читает одна реализация; при том же
// содержимом — побайтно тот же файл.
func TestRevisionRoundTripIsByteStable(t *testing.T) {
	r := spec.Revision{Number: 3, Fingerprint: fingerprintOf(t, a01(t))}
	b1, err := spec.WriteRevision(r)
	require.NoError(t, err)
	b2, err := spec.WriteRevision(r)
	require.NoError(t, err)
	require.Equal(t, b1, b2)
	back, err := spec.ReadRevision(b1)
	require.NoError(t, err)
	require.Equal(t, r, back)

	for _, bad := range []spec.Revision{{Number: 0, Fingerprint: r.Fingerprint}, {Number: 1, Fingerprint: "sha256:x"}} {
		_, err := spec.WriteRevision(bad)
		require.Error(t, err)
	}
	for _, bad := range []string{
		"revision: 0\nfingerprint: " + r.Fingerprint + "\n",
		"revision: 1.5\nfingerprint: " + r.Fingerprint + "\n",
		"revision: \"1\"\nfingerprint: " + r.Fingerprint + "\n",
		"revision: 1\n",
		"revision: 1\nfingerprint: v1:md5:00\n",
		"revision: 1\nfingerprint: " + r.Fingerprint + "\nextra: x\n",
		"- 1\n",
	} {
		_, err := spec.ReadRevision([]byte(bad))
		require.Error(t, err, "%q", bad)
	}
}
