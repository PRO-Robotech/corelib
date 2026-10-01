// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/gitenv"
)

const (
	notifPath = "svc/notifications/invite/notification.yaml"
	bodyPath  = "svc/notifications/invite/body.ru.yaml"
	revPath   = "svc/notifications/invite/revision.yaml"
)

func addAttr(tr *tree, name string) {
	tr.edit(notifPath, "  token: {type: token, presence: required}\n",
		"  token: {type: token, presence: required}\n  "+name+": {type: text, presence: required}\n")
	tr.edit(bodyPath, "  - heading: \"Вас пригласили\"\n", "  - heading: \"Вас пригласили\"\n  - p: \"{{ "+name+" }}\"\n")
}

func dropAttr(tr *tree, name string) {
	tr.edit(notifPath, "  "+name+": {type: text, presence: required}\n", "")
	tr.edit(bodyPath, "  - p: \"{{ "+name+" }}\"\n", "")
}

// trunk — ствол: шаблон invite с ревизией 2 и набором S (атрибут extra
// добавлен одной правкой после ревизии 1).
func trunk(t *testing.T) (*tree, string) {
	t.Helper()
	tr := newTree(t)
	tr.initGit()
	tr.generate()
	tr.commit("ревизия 1")
	addAttr(tr, "extra")
	tr.generate()
	require.Contains(t, tr.read(revPath), "revision: 2\n")
	return tr, tr.commit("ревизия 2, набор S")
}

// NTF1-D07 (-check -base): каждая находка называет шаблон; близнецы зелёные.
func TestNTF1D07_CheckAgainstBase(t *testing.T) {
	t.Run("(а) набор изменён, ревизия не поднята", func(t *testing.T) {
		tr, base := trunk(t)
		rev2 := tr.read(revPath)
		addAttr(tr, "more")
		tr.generate()
		// руками: отпечаток нового набора при ревизии 2
		newFP := tr.read(revPath)
		tr.write(revPath, strings.Replace(newFP, "revision: 3", "revision: 2", 1))
		_ = rev2
		r := tr.run("-check", "-base", base)
		require.NotEqual(t, 0, r.code)
		require.Contains(t, r.stderr, "invite: набор изменён, ревизия не поднята")
		require.Contains(t, r.stdout, "база "+base)
		// исправление — генерация от базы: ревизия 3 с отпечатком нового набора
		tr.generate("-base", base)
		require.Equal(t, newFP, tr.read(revPath))
		require.Equal(t, 0, tr.run("-check", "-base", base).code)
	})
	t.Run("(б) откат шаблона — ревизия ниже стволовой", func(t *testing.T) {
		tr, base := trunk(t)
		tr.git("checkout", "-q", "HEAD~1", "--", "svc")
		require.Contains(t, tr.read(revPath), "revision: 1\n")
		r := tr.run("-check", "-base", base)
		require.NotEqual(t, 0, r.code)
		require.Contains(t, r.stderr, "invite: ревизия ниже стволовой")
		tr.generate("-base", base)
		require.Contains(t, tr.read(revPath), "revision: 3\n")
		require.Equal(t, 0, tr.run("-check", "-base", base).code)
	})
	t.Run("(в) ревизия поднята без смены набора", func(t *testing.T) {
		tr, base := trunk(t)
		baseRev := tr.read(revPath)
		tr.write(revPath, strings.Replace(baseRev, "revision: 2", "revision: 3", 1))
		r := tr.run("-check", "-base", base)
		require.NotEqual(t, 0, r.code)
		require.Contains(t, r.stderr, "invite: ревизия поднята без смены набора")
		tr.generate("-base", base)
		require.Equal(t, baseRev, tr.read(revPath), "файл побайтово равен файлу базы")
		require.Equal(t, 0, tr.run("-check", "-base", base).code)
	})
	t.Run("(г) ревизия выше стволовой + 1", func(t *testing.T) {
		tr, base := trunk(t)
		addAttr(tr, "more")
		tr.generate()
		addAttr(tr, "most")
		tr.generate()
		require.Contains(t, tr.read(revPath), "revision: 4\n")
		r := tr.run("-check", "-base", base)
		require.NotEqual(t, 0, r.code)
		require.Contains(t, r.stderr, "invite: ревизия выше стволовой + 1")
		tr.generate("-base", base)
		require.Contains(t, tr.read(revPath), "revision: 3\n")
		require.Equal(t, 0, tr.run("-check", "-base", base).code)
	})
	t.Run("близнецы: ревизия 3 с новым набором, файл как в стволе", func(t *testing.T) {
		tr, base := trunk(t)
		require.Equal(t, 0, tr.run("-check", "-base", base).code, "файл как в стволе")
		addAttr(tr, "more")
		tr.generate()
		require.Contains(t, tr.read(revPath), "revision: 3\n")
		r := tr.run("-check", "-base", base)
		require.Equal(t, 0, r.code, r.stderr)
	})
	t.Run("A→B→A", func(t *testing.T) {
		tr, base := trunk(t)
		baseRev := tr.read(revPath)
		addAttr(tr, "more")
		tr.generate()
		dropAttr(tr, "more")
		tr.generate()
		require.Contains(t, tr.read(revPath), "revision: 4\n")
		require.Equal(t, 0, tr.run("-check").code, "без базы — зелёный")
		r := tr.run("-check", "-base", base)
		require.NotEqual(t, 0, r.code)
		require.Contains(t, r.stderr, "invite: ревизия поднята без смены набора")
		after := tr.read(revPath)
		tr.generate()
		require.Equal(t, after, tr.read(revPath), "повторная генерация файл не меняет")
		require.NotEqual(t, 0, tr.run("-check", "-base", base).code)
		tr.generate("-base", base)
		require.Equal(t, baseRev, tr.read(revPath))
		require.Equal(t, 0, tr.run("-check", "-base", base).code)
	})
	t.Run("-check -base исполняет и -check", func(t *testing.T) {
		tr, base := trunk(t)
		addAttr(tr, "more")
		r := tr.run("-check", "-base", base)
		require.NotEqual(t, 0, r.code)
		require.Contains(t, r.stderr, "invite: набор атрибутов не равен отпечатку ревизии")
	})
}

// NTF1-D07 (д): база не разрешается в ревизию хранилища — «база не найдена»
// с названным значением, код ненулевой, сверки нет; генерация не пишет.
func TestNTF1D07_UnresolvableBaseIsRed(t *testing.T) {
	tr, _ := trunk(t)
	empty := t.TempDir()
	for _, base := range []string{"", "/nonexistent/dir", empty} {
		r := tr.run("-check", "-base", base)
		require.NotEqual(t, 0, r.code, "%q", base)
		require.Contains(t, r.stderr, "база не найдена: "+quoted(base))
		require.NotContains(t, r.stdout, "сверенных")
		before := tr.files()
		require.NotEqual(t, 0, tr.run("-base", base).code)
		require.Equal(t, before, tr.files(), "генерация при ненайденной базе не пишет")
	}
	// HEAD^1 в клоне --depth 1 — родителя нет
	shallow := t.TempDir()
	out, err := gitenv.Command("", "clone", "-q", "--depth", "1", "file://"+tr.root, shallow).CombinedOutput()
	require.NoError(t, err, string(out))
	sh := &tree{t: t, root: shallow}
	r := sh.run("-check", "-base", "HEAD^1")
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.stderr, "база не найдена: \"HEAD^1\"")
	// близнец — полная история
	r = tr.run("-check", "-base", "HEAD^1")
	require.Equal(t, 0, r.code, r.stderr)
	require.Contains(t, r.stdout, "база "+tr.git("rev-parse", "HEAD^1"))
	require.NoError(t, os.RemoveAll(shallow))
}

func quoted(s string) string { return "\"" + s + "\"" }

// NTF1-D07 (е): база без каталогов notifications; знаменатель печатается на
// каждом прогоне; снятый шаблон — зелёный и в «снятых».
func TestNTF1D07_BaseWithoutTemplates(t *testing.T) {
	tr := &tree{t: t, root: t.TempDir()}
	tr.initGit()
	tr.write("svc/svc.go", "package svc\n")
	base := tr.commit("без шаблонов")
	tr.write(notifPath, inviteNotification)
	tr.write(bodyPath, inviteBody)
	tr.generate()
	r := tr.run("-check", "-base", base)
	require.Equal(t, 0, r.code, r.stderr)
	require.Contains(t, r.stdout, "в базе 0, в дереве 1, сверенных 0, новых 1, снятых 0")

	rev1 := tr.read(revPath)
	tr.write(revPath, strings.Replace(rev1, "revision: 1", "revision: 2", 1))
	r = tr.run("-check", "-base", base)
	require.NotEqual(t, 0, r.code)
	require.Contains(t, r.stderr, "invite: шаблона в базе нет, ревизия выше 1")
	require.Contains(t, r.stdout, "в базе 0, в дереве 1")
	tr.generate("-base", base)
	require.Equal(t, rev1, tr.read(revPath))

	// снятый шаблон
	withTpl := tr.commit("шаблон")
	require.NoError(t, os.RemoveAll(tr.path("svc/notifications")))
	require.NoError(t, os.Remove(tr.path("svc/notifications_invite.gen.go")))
	r = tr.run("-check", "-base", withTpl)
	require.Equal(t, 0, r.code, r.stderr)
	require.Contains(t, r.stdout, "в базе 1, в дереве 0, сверенных 0, новых 0, снятых 1")
}
