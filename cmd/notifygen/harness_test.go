// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/gitenv"
)

// A01 — каталог invite (класс security, лимиты на адресата и инициатора).
const inviteNotification = `name: invite
class: security
ttl: 168h
limits:
  - {scope: recipient, window: 24h, max: 3}
  - {scope: initiator, window: 1h, max: 20}
attributes:
  inviter_name: {type: text, presence: required}
  token: {type: token, presence: required}
subject:
  ru: "Приглашение в облако"
`

const inviteBody = `blocks:
  - heading: "Вас пригласили"
  - p: "{{ inviter_name }} приглашает вас в облако."
  - button: {text: "Принять приглашение", token: token, path: "/iam/invitations/accept"}
`

// tree — копия дерева источника в t.TempDir: пакет svc/ с каталогом
// notifications/invite.
type tree struct {
	t    *testing.T
	root string
}

func newTree(t *testing.T) *tree {
	t.Helper()
	tr := &tree{t: t, root: t.TempDir()}
	tr.write("svc/svc.go", "package svc\n")
	tr.write("svc/notifications/invite/notification.yaml", inviteNotification)
	tr.write("svc/notifications/invite/body.ru.yaml", inviteBody)
	return tr
}

func (tr *tree) path(rel string) string { return filepath.Join(tr.root, filepath.FromSlash(rel)) }

func (tr *tree) write(rel, content string) {
	tr.t.Helper()
	p := tr.path(rel)
	require.NoError(tr.t, os.MkdirAll(filepath.Dir(p), 0o750))
	require.NoError(tr.t, os.WriteFile(p, []byte(content), 0o600))
}

func (tr *tree) read(rel string) string {
	tr.t.Helper()
	b, err := os.ReadFile(tr.path(rel))
	require.NoError(tr.t, err)
	return string(b)
}

func (tr *tree) exists(rel string) bool {
	_, err := os.Stat(tr.path(rel))
	return err == nil
}

func (tr *tree) edit(rel, old, new string) {
	tr.t.Helper()
	s := tr.read(rel)
	require.Contains(tr.t, s, old)
	tr.write(rel, strings.Replace(s, old, new, 1))
}

// files — множество файлов дерева (путь от корня через «/»).
func (tr *tree) files() map[string]string {
	tr.t.Helper()
	out := map[string]string{}
	require.NoError(tr.t, filepath.WalkDir(tr.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(tr.root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	}))
	return out
}

// result — исход одного запуска генератора.
type result struct {
	code           int
	stdout, stderr string
}

func (tr *tree) run(args ...string) result {
	tr.t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"-root", tr.root}, args...), &out, &errb, testOptions())
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

// generate — make notifications.
func (tr *tree) generate(args ...string) result {
	tr.t.Helper()
	r := tr.run(args...)
	require.Equal(tr.t, 0, r.code, "генерация: %s%s", r.stdout, r.stderr)
	return r
}

func listed(stdout string) []string {
	var out []string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, "file ") {
			out = append(out, strings.TrimPrefix(l, "file "))
		}
	}
	sort.Strings(out)
	return out
}

// git — команда git в дереве.
func (tr *tree) git(args ...string) string {
	tr.t.Helper()
	cmd := gitenv.Command(tr.root, args...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=probe", "GIT_AUTHOR_EMAIL=probe@example.invalid",
		"GIT_COMMITTER_NAME=probe", "GIT_COMMITTER_EMAIL=probe@example.invalid", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(tr.t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func (tr *tree) commit(msg string) string {
	tr.t.Helper()
	tr.git("add", "-A")
	tr.git("commit", "-q", "--allow-empty", "-m", msg)
	return tr.git("rev-parse", "HEAD")
}

func (tr *tree) initGit() {
	tr.t.Helper()
	tr.git("init", "-q", "-b", "main")
}

// listDiff — сверка множества записанных генерацией файлов с выводом -list в
// обе стороны: лишний путь в выводе и недостающий — оба находки.
func listDiff(written, listed []string) []string {
	w := map[string]bool{}
	for _, p := range written {
		w[p] = true
	}
	l := map[string]bool{}
	for _, p := range listed {
		l[p] = true
	}
	var out []string
	for _, p := range listed {
		if !w[p] {
			out = append(out, "лишний в -list: "+p)
		}
	}
	for _, p := range written {
		if !l[p] {
			out = append(out, "недостающий в -list: "+p)
		}
	}
	sort.Strings(out)
	return out
}
