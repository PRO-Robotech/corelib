// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

// generator — один прогон над деревом.
type generator struct {
	root           string
	base           *baseTree
	stdout, stderr *sink
	opts           options
	findings       []string
}

func (g *generator) find(format string, a ...any) {
	g.findings = append(g.findings, fmt.Sprintf(format, a...))
}

// flush печатает находки; непустой перечень — errRed.
func (g *generator) flush() error {
	if len(g.findings) == 0 {
		return nil
	}
	sort.Strings(g.findings)
	for _, f := range g.findings {
		g.stderr.printf("%s\n", f)
	}
	return errRed
}

// loaded — проверенный каталог владельца.
type loaded struct {
	owner owner
	pkg   string
	cat   spec.Catalog
}

// load читает каталоги всех владельцев валидатором notify/spec. Находки
// валидатора и владелец без пакета Go — находки прогона.
func (g *generator) load() ([]loaded, spec.Census, error) {
	owners, err := findOwners(g.root)
	if err != nil {
		return nil, spec.Census{}, err
	}
	var (
		out   []loaded
		total spec.Census
	)
	for _, o := range owners {
		pkg, perr := packageName(g.root, o)
		if perr != nil {
			g.find("%v", perr)
		}
		cat, census, err := spec.Load(filepath.Join(g.root, filepath.FromSlash(o.catalog())))
		total.Templates += census.Templates
		total.Files += census.Files
		total.Blocks += census.Blocks
		var fs spec.Findings
		switch {
		case errors.As(err, &fs):
			for _, f := range fs {
				g.find("%s/%s", o.catalog(), f.Error())
			}
			continue
		case err != nil:
			return nil, total, err
		}
		if perr == nil {
			out = append(out, loaded{owner: o, pkg: pkg, cat: cat})
		}
	}
	return out, total, nil
}

// generate — make notifications: порождает файлы и пишет их, только если
// прогон без находок; файл с прежним содержимым не переписывается.
func (g *generator) generate() error {
	owners, census, err := g.load()
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	for _, l := range owners {
		for _, t := range l.cat.Templates {
			rev := g.nextRevision(l.owner, t)
			revBytes, err := spec.WriteRevision(rev)
			if err != nil {
				g.find("%s: %v", path.Join(l.owner.catalog(), t.Dir), err)
				continue
			}
			code, err := render(l.pkg, t, rev.Number)
			if err != nil {
				g.find("%s: %v", path.Join(l.owner.catalog(), t.Dir), err)
				continue
			}
			files[l.owner.revisionFile(t.Dir)] = revBytes
			files[l.owner.genFile(t.Dir)] = code
		}
	}
	if err := g.flush(); err != nil {
		return err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// Запись — только под корнем дерева (os.Root): ссылка вне дерева не
	// уводит запись наружу.
	root, err := os.OpenRoot(g.root)
	if err != nil {
		return fmt.Errorf("notifygen: корень %s: %w", g.root, err)
	}
	defer func() { _ = root.Close() }() // закрытие каталога-корня: данных не несёт
	written := 0
	for _, p := range paths {
		rel := filepath.FromSlash(p)
		if old, err := root.ReadFile(rel); err == nil && bytes.Equal(old, files[p]) {
			continue
		}
		if err := root.WriteFile(rel, files[p], 0o600); err != nil {
			return fmt.Errorf("notifygen: запись %s: %w", p, err)
		}
		written++
	}
	g.stdout.printf("шаблонов %d, файлов %d, записано %d\n", census.Templates, len(files), written)
	return nil
}

// nextRevision — ревизию ведёт генератор (NTF1-D07): она растёт ровно со
// сменой набора. С базой — от ревизии базы: набор тот же — ревизия базы,
// иной — база + 1, шаблона в базе нет — 1. Без базы — от записанной.
func (g *generator) nextRevision(o owner, t spec.Template) spec.Revision {
	fp := spec.SetFingerprint(spec.SetOf(t))
	if g.base != nil {
		b, ok := g.base.templates[path.Join(o.catalog(), t.Dir)]
		switch {
		case !ok:
			return spec.Revision{Number: 1, Fingerprint: fp}
		case spec.SameSet(spec.SetOf(t), b.set):
			return spec.Revision{Number: b.rev.Number, Fingerprint: fp}
		default:
			return spec.Revision{Number: b.rev.Number + 1, Fingerprint: fp}
		}
	}
	switch {
	case !t.HasRevision:
		return spec.Revision{Number: 1, Fingerprint: fp}
	case t.Revision.Fingerprint == fp:
		return t.Revision
	}
	return spec.Revision{Number: t.Revision.Number + 1, Fingerprint: fp}
}

// check — сверка без записи (NTF1-D02, D04, D07): ревизия против набора,
// эталон порождённого файла, побайтовое содержимое выпущенных миграций
// ленты; с базой — правило базы Р7 со знаменателем.
func (g *generator) check() error {
	owners, census, err := g.load()
	if err != nil {
		return err
	}
	for _, l := range owners {
		for _, t := range l.cat.Templates {
			id := path.Join(l.owner.catalog(), t.Dir)
			if !t.HasRevision {
				g.find("%s: ревизия не порождена — выполните make notifications", id)
				continue
			}
			if t.Revision.Fingerprint != spec.SetFingerprint(spec.SetOf(t)) {
				g.find("%s: набор атрибутов не равен отпечатку ревизии — выполните make notifications", id)
			}
			want, err := render(l.pkg, t, t.Revision.Number)
			if err != nil {
				g.find("%s: %v", id, err)
				continue
			}
			gen := l.owner.genFile(t.Dir)
			got, err := readUnder(g.root, gen)
			if err != nil || !bytes.Equal(got, want) {
				g.find("%s: эталон расходится с выводом генератора — выполните make notifications", gen)
			}
		}
	}
	migrations, err := g.checkMigrations()
	if err != nil {
		return err
	}
	functions, err := g.checkFanout(owners)
	if err != nil {
		return err
	}
	if g.base != nil {
		g.checkBase(owners)
	}
	g.stdout.printf("шаблонов %d, файлов %d, блоков %d, миграций ленты %d, функций resource-event %d\n",
		census.Templates, census.Files, census.Blocks, migrations, functions)
	return g.flush()
}
