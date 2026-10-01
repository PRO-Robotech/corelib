// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing/fstest"

	"github.com/PRO-Robotech/corelib/gitenv"
	"github.com/PRO-Robotech/corelib/notify/spec"
)

// baseTemplate — шаблон в ревизии ствола: набор и записанная ревизия.
type baseTemplate struct {
	set spec.Set
	rev spec.Revision
}

// baseTree — шаблоны ревизии ствола по пути каталога шаблона от корня.
type baseTree struct {
	commit    string
	templates map[string]baseTemplate
}

// baseNotification — notification.yaml шаблона в выводе git ls-tree.
var baseNotification = regexp.MustCompile(`^(.+/)?notifications/([^/]+)/notification\.yaml$`)

func git(root string, args ...string) ([]byte, error) {
	// gitenv снимает GIT_DIR и родню: из хука git они указали бы на чужой
	// репозиторий сильнее рабочего каталога.
	cmd := gitenv.Command(root, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// resolveBase разрешает -base в ревизию хранилища проверяемого дерева и
// читает её шаблоны узким чтением набора (spec.ReadSetOnly): формат,
// ужесточённый после базы, базу «ненайденной» не делает. Значение, не
// разрешающееся в коммит, — «база не найдена» с названным значением, сверки
// нет (NTF1-D07 (д)).
func resolveBase(root, rev string) (*baseTree, error) {
	notFound := func(why error) error {
		return fmt.Errorf("notifygen: база не найдена: %q (%v)", rev, why)
	}
	if rev == "" {
		return nil, notFound(fmt.Errorf("значение пусто"))
	}
	out, err := git(root, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return nil, notFound(err)
	}
	commit := strings.TrimSpace(string(out))
	prefixOut, err := git(root, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, notFound(err)
	}
	prefix := strings.TrimSpace(string(prefixOut))
	names, err := git(root, "ls-tree", "-r", "-z", "--name-only", "--full-tree", commit)
	if err != nil {
		return nil, notFound(err)
	}
	b := &baseTree{commit: commit, templates: map[string]baseTemplate{}}
	for _, name := range strings.Split(string(names), "\x00") {
		if !strings.HasPrefix(name, prefix) || !baseNotification.MatchString(name) {
			continue
		}
		dir := path.Dir(name)
		notif, err := git(root, "show", commit+":"+name)
		if err != nil {
			return nil, fmt.Errorf("notifygen: база %s: %w", commit, err)
		}
		set, err := spec.ReadSetOnly(fstest.MapFS{"notification.yaml": {Data: notif}}, ".")
		if err != nil {
			return nil, fmt.Errorf("notifygen: база %s: %s: %w", commit, dir, err)
		}
		revBytes, err := git(root, "show", commit+":"+dir+"/revision.yaml")
		if err != nil {
			// Шаблон ствола без ревизии — до генератора; судится как новый.
			continue
		}
		r, err := spec.ReadRevision(revBytes)
		if err != nil {
			return nil, fmt.Errorf("notifygen: база %s: %s/revision.yaml: %w", commit, dir, err)
		}
		b.templates[strings.TrimPrefix(dir, prefix)] = baseTemplate{set: set, rev: r}
	}
	return b, nil
}

// checkBase — правило базы Р7 (NTF1-D07): каждая находка называет шаблон;
// знаменатель печатается на каждом прогоне.
func (g *generator) checkBase(owners []loaded) {
	inTree := map[string]bool{}
	var compared, added int
	for _, l := range owners {
		for _, t := range l.cat.Templates {
			id := path.Join(l.owner.catalog(), t.Dir)
			inTree[id] = true
			if !t.HasRevision {
				continue
			}
			r := t.Revision.Number
			b, ok := g.base.templates[id]
			if !ok {
				added++
				if r > 1 {
					g.find("%s: шаблона в базе нет, ревизия выше 1", id)
				}
				continue
			}
			compared++
			rb := b.rev.Number
			switch same := spec.SameSet(spec.SetOf(t), b.set); {
			case r < rb:
				g.find("%s: ревизия ниже стволовой (%d < %d)", id, r, rb)
			case same && r > rb:
				g.find("%s: ревизия поднята без смены набора (%d при стволовой %d)", id, r, rb)
			case !same && r == rb:
				g.find("%s: набор изменён, ревизия не поднята (%d)", id, r)
			case !same && r > rb+1:
				g.find("%s: ревизия выше стволовой + 1 (%d при стволовой %d)", id, r, rb)
			}
		}
	}
	removed := 0
	ids := make([]string, 0, len(g.base.templates))
	for id := range g.base.templates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !inTree[id] {
			removed++
		}
	}
	g.stdout.printf("база %s: в базе %d, в дереве %d, сверенных %d, новых %d, снятых %d\n",
		g.base.commit, len(g.base.templates), len(inTree), compared, added, removed)
}
