// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// schemaCatalog — выпущенные версии схемы ленты: их содержимое генератор
// держит встроенным (сборкой из notify/feed/schema) и сверяет с файлами
// дерева побайтово (D04).
type schemaCatalog interface {
	Versions() []schema.Version
	Current() schema.Version
	Migration(svc string, v schema.Version) (string, error)
	Upgrade(svc string, from, to schema.Version) (string, error)
}

// releasedSchemas — версии пина corelib.
type releasedSchemas struct{}

func (releasedSchemas) Versions() []schema.Version { return schema.Versions() }
func (releasedSchemas) Current() schema.Version    { return schema.Current() }
func (releasedSchemas) Migration(svc string, v schema.Version) (string, error) {
	return schema.Migration(svc, v)
}

func (releasedSchemas) Upgrade(_ string, from, to schema.Version) (string, error) {
	return "", fmt.Errorf("переход схемы ленты v%d→v%d не выпущен", int(from), int(to))
}

// feedMigration — имя файла миграции ленты: <метка>_notification_feed_v<N>
// либо …_v<N>_from_v<M>.
var feedMigration = regexp.MustCompile(`^([0-9]+)_notification_feed_v([0-9]+)(?:_from_v([0-9]+))?\.sql$`)

// migrationLabel — числовая метка файла миграции goose.
var migrationLabel = regexp.MustCompile(`^([0-9]+)_`)

// header — служба, версия и исходная версия из строки заголовка миграции.
type header struct {
	svc      string
	version  int
	from     int
	fromSeen bool
}

func parseHeader(content []byte) (header, bool) {
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, schema.HeaderPrefix) {
			continue
		}
		var h header
		for _, kv := range strings.Fields(strings.TrimPrefix(line, schema.HeaderPrefix)) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return header{}, false
			}
			switch k {
			case "service":
				h.svc = v
			case "version", "from":
				n, err := strconv.Atoi(v)
				if err != nil {
					return header{}, false
				}
				if k == "version" {
					h.version = n
				} else {
					h.from, h.fromSeen = n, true
				}
			default:
				return header{}, false
			}
		}
		return h, h.svc != "" && h.version > 0
	}
	return header{}, false
}

func (g *generator) expected(h header) (string, error) {
	if h.fromSeen {
		return g.opts.schemas.Upgrade(h.svc, schema.Version(h.from), schema.Version(h.version))
	}
	return g.opts.schemas.Migration(h.svc, schema.Version(h.version))
}

// checkMigrations сверяет каждый файл миграции ленты дерева с выпущенным
// содержимым своей версии побайтово: правку применённого файла гейт
// монотонности не видит — он судит только номер добавленного файла (D04).
func (g *generator) checkMigrations() (int, error) {
	n := 0
	err := filepath.WalkDir(g.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != g.root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !feedMigration.MatchString(d.Name()) {
			return nil
		}
		n++
		rel, err := filepath.Rel(g.root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		content, err := readUnder(g.root, rel)
		if err != nil {
			return err
		}
		h, ok := parseHeader(content)
		if !ok {
			g.find("%s: заголовок notifygen миграции ленты не читается", rel)
			return nil
		}
		want, err := g.expected(h)
		if err != nil {
			g.find("%s: %v", rel, err)
			return nil
		}
		if !bytes.Equal(content, []byte(want)) {
			g.find("%s: применённый файл миграции ленты правлен — генератор держит выпущенное содержимое; правка — только новой миграцией", rel)
		}
		return nil
	})
	return n, err
}

// runInit — notifygen init: новая миграция ленты только при смене версии
// схемы (NTF1-D03, D04). Применённые файлы не трогаются; метка нового файла
// старше каждой миграции каталога.
func runInit(root string, args []string, stdout, stderr *sink, o options) error {
	fs := flag.NewFlagSet("notifygen init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	svc := fs.String("service", "", "префикс таблиц ленты службы")
	dir := fs.String("migrations", "", "каталог миграций службы от корня")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *svc == "" || *dir == "" {
		return fmt.Errorf("notifygen init: нужны -service и -migrations")
	}
	full := filepath.Join(root, filepath.FromSlash(*dir))
	if err := os.MkdirAll(full, 0o750); err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	var maxLabel uint64
	applied := 0
	for _, e := range entries {
		if m := migrationLabel.FindStringSubmatch(e.Name()); m != nil {
			if l, err := strconv.ParseUint(m[1], 10, 64); err == nil && l > maxLabel {
				maxLabel = l
			}
		}
		if m := feedMigration.FindStringSubmatch(e.Name()); m != nil {
			if v, err := strconv.Atoi(m[2]); err == nil && v > applied {
				applied = v
			}
		}
	}
	cur := int(o.schemas.Current())
	if applied == cur {
		stdout.printf("схема ленты v%d уже применена, изменений 0\n", cur)
		return nil
	}
	if applied > cur {
		return fmt.Errorf("notifygen init: в %s схема ленты v%d новее пина corelib (v%d)", *dir, applied, cur)
	}
	label, err := strconv.ParseUint(o.now().UTC().Format("20060102150405"), 10, 64)
	if err != nil {
		return fmt.Errorf("notifygen init: метка: %w", err)
	}
	label = max(label, maxLabel+1)
	var (
		name, content string
	)
	if applied == 0 {
		name = fmt.Sprintf("%d_notification_feed_v%d.sql", label, cur)
		content, err = o.schemas.Migration(*svc, schema.Version(cur))
	} else {
		name = fmt.Sprintf("%d_notification_feed_v%d_from_v%d.sql", label, cur, applied)
		content, err = o.schemas.Upgrade(*svc, schema.Version(applied), schema.Version(cur))
	}
	if err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	dirRoot, err := os.OpenRoot(full)
	if err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	defer func() { _ = dirRoot.Close() }() // закрытие каталога-корня: данных не несёт
	if err := dirRoot.WriteFile(name, []byte(content), 0o600); err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	stdout.printf("записана %s/%s, изменений 1\n", *dir, name)
	return nil
}
