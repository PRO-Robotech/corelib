// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// catalogDir — имя каталога шаблонов владельца.
const catalogDir = "notifications"

// owner — каталог пакета источника, несущий notifications/.
type owner struct {
	dir string // от корня дерева, через «/»
}

func (o owner) catalog() string { return path.Join(o.dir, catalogDir) }

// genFile — порождённый файл шаблона name в пакете владельца.
func (o owner) genFile(name string) string {
	return path.Join(o.dir, "notifications_"+name+".gen.go")
}

// revisionFile — revision.yaml шаблона name.
func (o owner) revisionFile(name string) string {
	return path.Join(o.dir, catalogDir, name, "revision.yaml")
}

// skipDir — каталоги, которые обход дерева не читает.
func skipDir(name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", "testdata":
		return true
	}
	return strings.HasPrefix(name, ".") && name != "."
}

// findOwners — владельцы дерева: каждый каталог с подкаталогом notifications.
func findOwners(root string) ([]owner, error) {
	var out []owner
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.Name() != catalogDir || p == root {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		out = append(out, owner{dir: filepath.ToSlash(rel)})
		return filepath.SkipDir
	})
	if err != nil {
		return nil, fmt.Errorf("notifygen: обход дерева %s: %w", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out, nil
}

// templateDirs — имена каталогов шаблонов владельца по файловой системе, без
// валидатора: множество порождаемых файлов не зависит от исхода сверки (D02).
func templateDirs(root string, o owner) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(o.catalog())))
	if err != nil {
		return nil, fmt.Errorf("notifygen: %s не читается: %w", o.catalog(), err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// packageName — имя пакета Go владельца по объявлению package его
// не-тестовых файлов. Каталог без пакета — находка: имя не угадывается.
func packageName(root string, o owner) (string, error) {
	dir := filepath.Join(root, filepath.FromSlash(o.dir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.PackageClauseOnly)
		if err != nil {
			return "", fmt.Errorf("%s/%s: %w", o.dir, n, err)
		}
		return f.Name.Name, nil
	}
	return "", fmt.Errorf("%s: каталог источника без пакета Go — имя пакета порождённого файла не из чего взять", o.dir)
}

// generatedSet — множество порождаемых файлов дерева: путь от корня через
// «/», отсортировано, без повторов (D01).
func generatedSet(root string) ([]string, error) {
	owners, err := findOwners(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range owners {
		names, err := templateDirs(root, o)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out = append(out, o.genFile(n), o.revisionFile(n))
		}
	}
	sort.Strings(out)
	return out, nil
}

// printList печатает множество строками «file <путь>».
func printList(root string, stdout, stderr *sink) error {
	set, err := generatedSet(root)
	if err != nil {
		stderr.printf("%s\n", err)
		return errRed
	}
	for _, p := range set {
		stdout.printf("file %s\n", p)
	}
	return nil
}

// readUnder читает файл rel (через «/») только под корнем root.
func readUnder(root, rel string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }() // закрытие каталога-корня: данных не несёт
	return r.ReadFile(filepath.FromSlash(rel))
}
