// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// Форма fanout генератора (NTF-3 З10): функция базы resource-event на таблице
// журнала модуля. Вход о журнале — объявление journal.yaml рядом с каталогом
// notifications/ владельца (таблица, колонки, виды с формой имени и якорем,
// словарь рода изменения: то же, что Mapping модуля). init -journal пишет
// миграцию функции и триггера, а при смене объявления или schema_rev шаблона
// resource-event — новую миграцию CREATE OR REPLACE той же функции; -check
// сверяет каждое тело с выводом его выпущенной версии шаблона, действующее
// тело — с объявлением, строку сигнала — с формой notify/spec и атрибуты
// SQL-половины — с атрибутами шаблона (Go-половиной).

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/PRO-Robotech/corelib/notify/feed/resourceevent"
	"github.com/PRO-Robotech/corelib/notify/form"
	"github.com/PRO-Robotech/corelib/notify/spec"
)

// journalFile — объявление журнала модуля в каталоге владельца.
const journalFile = "journal.yaml"

// journalDecl — journal.yaml. Поля закрыты: неизвестный ключ — отказ.
type journalDecl struct {
	Module  string              `yaml:"module"`
	Table   string              `yaml:"table"`
	Columns map[string]string   `yaml:"columns"`
	Kinds   map[string]kindDecl `yaml:"kinds"`
	Changes map[string]string   `yaml:"changes"`
}

type kindDecl struct {
	NameForm string `yaml:"name_form"`
	Scope    string `yaml:"scope"`
}

// journalColumns — закрытый набор ключей columns.
var journalColumns = []string{"change", "id", "initiator", "kind", "occurred_at", "payload", "project"}

// readJournal читает объявление rel (через «/») под корнем root.
func readJournal(root, rel string) (journalDecl, error) {
	data, err := readUnder(root, rel)
	if err != nil {
		return journalDecl{}, fmt.Errorf("%s: %w", rel, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d journalDecl
	if err := dec.Decode(&d); err != nil {
		return journalDecl{}, fmt.Errorf("%s: объявление журнала не читается: %w", rel, err)
	}
	keys := make([]string, 0, len(d.Columns))
	for k := range d.Columns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !slices.Equal(keys, journalColumns) {
		return journalDecl{}, fmt.Errorf("%s: columns %v, нужны ровно %v", rel, keys, journalColumns)
	}
	return d, nil
}

// fanoutTemplate — шаблон resource-event владельца dir: форма fanout,
// ревизия порождена.
func fanoutTemplate(cat spec.Catalog, dir string) (spec.Template, error) {
	for _, t := range cat.Templates {
		if t.Name != resourceevent.TemplateName {
			continue
		}
		if t.Recipient != spec.RecipientFanout {
			return spec.Template{}, fmt.Errorf("%s: шаблон %s формы %s, функция базы — только у формы fanout",
				path.Join(dir, catalogDir), t.Name, t.Recipient)
		}
		if !t.HasRevision || t.Revision.Fingerprint != spec.SetFingerprint(spec.SetOf(t)) {
			return spec.Template{}, fmt.Errorf("%s: ревизия шаблона %s не порождена — выполните make notifications",
				path.Join(dir, catalogDir, t.Dir), t.Name)
		}
		return t, nil
	}
	return spec.Template{}, fmt.Errorf("%s: шаблона %s нет — функции базы нечего ставить", path.Join(dir, catalogDir), resourceevent.TemplateName)
}

// inputsOf — входы тела из объявления d, шаблона t и префикса таблиц ленты
// service.
func inputsOf(d journalDecl, t spec.Template, service string) (resourceevent.Inputs, error) {
	in := resourceevent.Inputs{
		Service: service, Module: d.Module, SchemaRev: t.Revision.Number, TTL: t.TTL, Table: d.Table,
		Columns: resourceevent.Columns{
			Kind: d.Columns["kind"], ID: d.Columns["id"], Change: d.Columns["change"], Payload: d.Columns["payload"],
			Project: d.Columns["project"], Initiator: d.Columns["initiator"], OccurredAt: d.Columns["occurred_at"],
		},
	}
	signal, ok := d.Kinds[spec.FeedSignalKey]
	if !ok {
		return resourceevent.Inputs{}, fmt.Errorf("журнал %s не объявил ключ строки сигнала %q", d.Table, spec.FeedSignalKey)
	}
	if signal.NameForm != string(resourceevent.NameFormNone) || signal.Scope != string(resourceevent.ScopeCluster) {
		return resourceevent.Inputs{}, fmt.Errorf("ключ строки сигнала %q журнала %s обязан быть уровня кластера без имени", spec.FeedSignalKey, d.Table)
	}
	for name, k := range d.Kinds {
		if name == spec.FeedSignalKey {
			continue
		}
		in.Kinds = append(in.Kinds, resourceevent.Kind{Name: name, NameForm: resourceevent.NameForm(k.NameForm), Scope: resourceevent.Scope(k.Scope)})
	}
	sort.Slice(in.Kinds, func(i, j int) bool { return in.Kinds[i].Name < in.Kinds[j].Name })
	for word, change := range d.Changes {
		in.Changes = append(in.Changes, resourceevent.Change{Word: word, Change: change})
	}
	sort.Slice(in.Changes, func(i, j int) bool { return in.Changes[i].Word < in.Changes[j].Word })
	word, err := resourceevent.SignalChangeWord(in.Changes)
	if err != nil {
		return resourceevent.Inputs{}, err
	}
	in.SignalChange = word
	if err := in.Validate(); err != nil {
		return resourceevent.Inputs{}, err
	}
	return in, nil
}

// declaredInputs — входы по объявлению journalRel и шаблону его владельца.
func declaredInputs(root, journalRel, service string) (resourceevent.Inputs, spec.Template, error) {
	d, err := readJournal(root, journalRel)
	if err != nil {
		return resourceevent.Inputs{}, spec.Template{}, err
	}
	dir := path.Dir(journalRel)
	cat, _, err := spec.Load(filepath.Join(root, filepath.FromSlash(path.Join(dir, catalogDir))))
	if err != nil {
		return resourceevent.Inputs{}, spec.Template{}, fmt.Errorf("%s: %w", path.Join(dir, catalogDir), err)
	}
	t, err := fanoutTemplate(cat, dir)
	if err != nil {
		return resourceevent.Inputs{}, spec.Template{}, err
	}
	in, err := inputsOf(d, t, service)
	if err != nil {
		return resourceevent.Inputs{}, spec.Template{}, fmt.Errorf("%s: %w", journalRel, err)
	}
	return in, t, nil
}

// fnFile — разобранная миграция функции дерева.
type fnFile struct {
	rel     string
	content []byte
	parsed  resourceevent.Parsed
}

// latestOn — действующее определение журнала table среди files: Up файла с
// наибольшим именем (метка goose впереди имени).
func latestOn(files []fnFile, table string) (fnFile, bool) {
	var (
		out   fnFile
		found bool
	)
	for _, f := range files {
		if f.parsed.File.Up.Inputs.Table != table {
			continue
		}
		if !found || path.Base(f.rel) > path.Base(out.rel) {
			out, found = f, true
		}
	}
	return out, found
}

func sameBody(a, b resourceevent.Body) bool {
	return a.Version == b.Version && len(bodyDiff(a, b)) == 0
}

// bodyDiff — расхождения входов определения got с ожидаемым want словами.
func bodyDiff(got, want resourceevent.Body) []string {
	var out []string
	g, w := got.Inputs, want.Inputs
	if got.Version != want.Version {
		out = append(out, fmt.Sprintf("версия шаблона %s, действующая %s", got.Version, want.Version))
	}
	if g.SchemaRev != w.SchemaRev {
		out = append(out, fmt.Sprintf("schema_rev %d, у шаблона %d", g.SchemaRev, w.SchemaRev))
	}
	if g.TTL != w.TTL {
		out = append(out, fmt.Sprintf("ttl %s, у шаблона %s", g.TTL, w.TTL))
	}
	if g.Module != w.Module {
		out = append(out, fmt.Sprintf("модуль %s, объявлен %s", g.Module, w.Module))
	}
	if g.Service != w.Service {
		out = append(out, fmt.Sprintf("служба %s, ожидалась %s", g.Service, w.Service))
	}
	if g.Table != w.Table || g.Columns != w.Columns {
		out = append(out, fmt.Sprintf("журнал %s %+v, объявлен %s %+v", g.Table, g.Columns, w.Table, w.Columns))
	}
	if g.SignalChange != w.SignalChange {
		out = append(out, fmt.Sprintf("слово строки сигнала %s, по словарю %s", g.SignalChange, w.SignalChange))
	}
	kinds := func(ks []resourceevent.Kind) map[string]string {
		m := map[string]string{}
		for _, k := range ks {
			m[k.Name] = string(k.NameForm) + "/" + string(k.Scope)
		}
		return m
	}
	out = append(out, mapDiff("вид", kinds(g.Kinds), kinds(w.Kinds))...)
	changes := func(cs []resourceevent.Change) map[string]string {
		m := map[string]string{}
		for _, c := range cs {
			m[c.Word] = c.Change
		}
		return m
	}
	out = append(out, mapDiff("слово рода изменения", changes(g.Changes), changes(w.Changes))...)
	return out
}

func mapDiff(what string, got, want map[string]string) []string {
	var out []string
	for _, k := range sortedNames(want) {
		switch v, ok := got[k]; {
		case !ok:
			out = append(out, fmt.Sprintf("%s %s объявлен, в теле его нет", what, k))
		case v != want[k]:
			out = append(out, fmt.Sprintf("%s %s: в теле %s, объявлено %s", what, k, v, want[k]))
		}
	}
	for _, k := range sortedNames(got) {
		if _, ok := want[k]; !ok {
			out = append(out, fmt.Sprintf("%s %s в теле, в объявлении его нет", what, k))
		}
	}
	return out
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scanFunctions — миграции функции resource-event дерева: каждый
// отслеживаемый обходом *.sql с маркером тела. Нечитаемое тело — ошибка
// с координатой.
func scanFunctions(root string) ([]fnFile, []error, error) {
	var (
		out []fnFile
		bad []error
	)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".sql") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		content, err := readUnder(root, rel)
		if err != nil {
			return err
		}
		parsed, err := resourceevent.Parse(content)
		switch {
		case errors.Is(err, resourceevent.ErrNotFunction):
			return nil
		case err != nil:
			bad = append(bad, fmt.Errorf("%s: тело функции resource-event не читается: %w", rel, err))
			return nil
		}
		out = append(out, fnFile{rel: rel, content: content, parsed: parsed})
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("notifygen: обход миграций функции resource-event: %w", err)
	}
	return out, bad, nil
}

// fanoutInputs — входы init -journal: объявление journalRel (journal.yaml
// владельца) и шаблон resource-event его владельца, префикс таблиц ленты svc.
func fanoutInputs(root, svc, journalRel string) (resourceevent.Inputs, error) {
	if path.Base(journalRel) != journalFile {
		// -check находит объявление только как journal.yaml владельца.
		return resourceevent.Inputs{}, fmt.Errorf("notifygen init: -journal %s — объявление журнала — файл %s в каталоге владельца", journalRel, journalFile)
	}
	in, _, err := declaredInputs(root, journalRel, svc)
	if err != nil {
		return resourceevent.Inputs{}, fmt.Errorf("notifygen init: %w", err)
	}
	return in, nil
}

// runInitFanout — init -journal: миграция функции журнала in.Table в каталоге
// dir. Действующее тело равно выводу на объявлении — изменений 0; тела нет —
// функция и триггер; иначе — CREATE OR REPLACE той же функции с прежним
// определением в откате.
func runInitFanout(root, dir string, in resourceevent.Inputs, stdout *sink, o options) error {
	full := filepath.Join(root, filepath.FromSlash(dir))
	files, bad, err := scanFunctions(full)
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		return fmt.Errorf("notifygen init: %w", errors.Join(bad...))
	}
	want := resourceevent.Body{Version: resourceevent.Current(), Inputs: in}
	f := resourceevent.File{Up: want}
	if cur, ok := latestOn(files, in.Table); ok {
		if sameBody(cur.parsed.File.Up, want) {
			stdout.printf("функция resource-event журнала %s по объявлению, изменений 0\n", in.Table)
			return nil
		}
		prev := cur.parsed.File.Up
		f.Prev = &prev
	}
	content, err := resourceevent.Render(f)
	if err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	label, err := nextLabel(full, o)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d_%s.sql", label, resourceevent.FunctionName)
	if err := writeUnder(full, name, content); err != nil {
		return err
	}
	stdout.printf("записана %s/%s, изменений 1\n", dir, name)
	return nil
}

// nextLabel — метка новой миграции: часы, но старше каждой миграции
// каталога full.
func nextLabel(full string, o options) (uint64, error) {
	entries, err := os.ReadDir(full)
	if err != nil {
		return 0, fmt.Errorf("notifygen init: %w", err)
	}
	var maxLabel uint64
	for _, e := range entries {
		if m := migrationLabel.FindStringSubmatch(e.Name()); m != nil {
			if l, err := strconv.ParseUint(m[1], 10, 64); err == nil && l > maxLabel {
				maxLabel = l
			}
		}
	}
	label, err := strconv.ParseUint(o.now().UTC().Format("20060102150405"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("notifygen init: метка: %w", err)
	}
	return max(label, maxLabel+1), nil
}

// writeUnder пишет файл name только под каталогом full (os.Root).
func writeUnder(full, name, content string) error {
	r, err := os.OpenRoot(full)
	if err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	defer func() { _ = r.Close() }() // закрытие каталога-корня: данных не несёт
	if err := r.WriteFile(name, []byte(content), 0o600); err != nil {
		return fmt.Errorf("notifygen init: %w", err)
	}
	return nil
}

// checkFanout — сверка формы fanout по дереву; ответ — число функций
// (журналов с телом).
func (g *generator) checkFanout(owners []loaded) (int, error) {
	files, bad, err := scanFunctions(g.root)
	if err != nil {
		return 0, err
	}
	for _, e := range bad {
		g.find("%v", e)
	}
	tables := map[string]bool{}
	for _, f := range files {
		tables[f.parsed.File.Up.Inputs.Table] = true
		g.checkBody(f)
	}
	declared := map[string]bool{}
	for _, l := range owners {
		rel := path.Join(l.owner.dir, journalFile)
		if _, err := readUnder(g.root, rel); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return 0, fmt.Errorf("notifygen: %s: %w", rel, err)
		}
		d, err := readJournal(g.root, rel)
		if err != nil {
			g.find("%v", err)
			continue
		}
		declared[d.Table] = true
		cur, ok := latestOn(files, d.Table)
		if !ok {
			g.find("%s: функции resource-event на журнале %s нет — выполните notifygen init -journal %s", rel, d.Table, rel)
			continue
		}
		in, t, err := declaredInputs(g.root, rel, cur.parsed.File.Up.Inputs.Service)
		if err != nil {
			g.find("%v", err)
			continue
		}
		want := resourceevent.Body{Version: resourceevent.Current(), Inputs: in}
		if diff := bodyDiff(cur.parsed.File.Up, want); len(diff) > 0 {
			g.find("%s: действующее тело функции resource-event отстаёт от объявления %s: %s — выполните notifygen init -journal %s",
				cur.rel, rel, strings.Join(diff, "; "), rel)
		}
		g.checkHalves(cur, t)
	}
	for _, table := range sortedKeys(tables) {
		if !declared[table] {
			cur, _ := latestOn(files, table)
			g.find("%s: функция resource-event на журнале %s без объявления %s у владельца", cur.rel, table, journalFile)
		}
	}
	return len(tables), nil
}

// checkBody — тело есть вывод своей версии шаблона на извлечённых входах, и
// строка сигнала — по форме notify/spec.
func (g *generator) checkBody(f fnFile) {
	if _, ok := resourceevent.Recognize(f.content); !ok {
		g.find("%s: тело функции resource-event расходится с выводом шаблона %s на извлечённых из него входах — правка вручную; правка тела — только новой миграцией notifygen init",
			f.rel, f.parsed.File.Up.Version)
	}
	check := func(b resourceevent.Body, got string) {
		sig, err := spec.FeedSignal(b.Inputs.Module)
		if err != nil {
			g.find("%s: %v", f.rel, err)
			return
		}
		if got != sig.Object {
			g.find("%s: строка сигнала %s, форма notify/spec — %s", f.rel, got, sig.Object)
		}
	}
	check(f.parsed.File.Up, f.parsed.Signal)
	if f.parsed.File.Prev != nil {
		check(*f.parsed.File.Prev, f.parsed.PrevSignal)
	}
}

// checkHalves — атрибуты Go-половины (шаблон t, из которого порождён
// SendResourceEvent) и SQL-половины (тело cur) совпадают по имени, виду и
// обязательности; initiator SQL-половина ставит из колонки журнала, шаблон
// его не объявляет (имя занято ключом окна).
func (g *generator) checkHalves(cur fnFile, t spec.Template) {
	sql, err := resourceevent.AttrsOf(cur.parsed.File.Up.Version)
	if err != nil {
		g.find("%s: %v", cur.rel, err)
		return
	}
	set := map[string]resourceevent.Attr{}
	for _, a := range sql {
		set[a.Name] = a
	}
	declared := map[string]bool{}
	for _, a := range t.Attrs {
		declared[a.Name] = true
		s, ok := set[a.Name]
		switch {
		case !ok:
			g.find("%s: атрибут %s шаблона resource-event SQL-половина не ставит — половины расходятся", cur.rel, a.Name)
		case form.Kind(s.Kind) != a.Kind:
			g.find("%s: атрибут %s: вид у шаблона %s, у SQL-половины %s", cur.rel, a.Name, a.Kind, s.Kind)
		case s.Optional != (a.Presence == spec.PresenceOptional):
			g.find("%s: атрибут %s: обязательность у шаблона %s, SQL-половина опускает ключ: %t", cur.rel, a.Name, a.Presence, s.Optional)
		}
	}
	for _, a := range sql {
		if a.Name != resourceevent.ReservedAttr && !declared[a.Name] {
			g.find("%s: атрибут %s ставит SQL-половина, шаблон resource-event его не объявляет — половины расходятся", cur.rel, a.Name)
		}
	}
}

// sortedKeys — ключи множества по возрастанию.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
