// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package spec — формат шаблона почтового извещения и ЕДИНСТВЕННЫЙ его
// валидатор (Р7 приёмки NTF-1, З4 замысла issue-2915). Функция, открывающая
// или разбирающая notification.yaml, body.<locale>.yaml или revision.yaml,
// живёт только здесь (NTF1-A09): генератор, гейт сборки notify и notify на
// старте зовут Load.
//
// Раскладка каталога шаблонов владельца:
//
//	<owner>/notifications/<name>/notification.yaml
//	<owner>/notifications/<name>/body.<locale>.yaml
//	<owner>/notifications/<name>/revision.yaml   (порождает генератор)
//
// notification.yaml:
//
//	name: invite                      # имя каталога
//	class: security                   # security | notice
//	recipient: address                # address | subject | fanout | account_owner;
//	                                  # нет поля — address
//	ttl: 168h                         # [1s..720h], целые секунды
//	limits:                           # обязательны для security
//	  - {scope: recipient, window: 24h, max: 3}   # recipient | initiator | project
//	attributes:
//	  inviter_name: {type: text, presence: required}   # optional — под when
//	  token: {type: token, presence: required}
//	subject:
//	  ru: "Приглашение в облако"      # подстановки — только атрибуты required
//	  en: "Invitation to the cloud"   # тема и тело — на каждой локали Locales
//
// body.ru.yaml — только блоки закрытого набора (BlockKinds), у блока ровно
// один вид и необязательный when:
//
//	blocks:
//	  - heading: "Вас пригласили"
//	  - p: "{{ inviter_name }} приглашает вас."
//	    when: inviter_name
//	  - button: {text: "Принять", token: token, path: "/iam/invitations/accept"}
//	  - button: {text: "Открыть", path: target}
//	  - list: ["первый", "{{ note }}"]
//	  - kv: [{key: "Когда", value: "{{ issued_at }}"}]
//	  - code: "{{ code }}"
//	  - warning: "…"
//	  - divider: {}
//
// Формат закрыт: неизвестный ключ, посторонний файл, повтор ключа — находка.
// Формат только расширяется: замороженный корпус corpus/<версия>/ каждой
// выпущенной версии обязан приниматься (NTF1-A08).
package spec

import (
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/PRO-Robotech/corelib/notify/form"
)

const (
	notificationFile = "notification.yaml"
	revisionFile     = "revision.yaml"
)

var (
	bodyFileRe     = regexp.MustCompile(`^body\.([^.]+)\.yaml$`)
	templateNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z][a-z0-9]*)*$`)
	// reservedAttrNames — имена, занятые полями постановки XAttrs (To,
	// Initiator): атрибут с таким именем дал бы два поля одного имени.
	reservedAttrNames = map[string]bool{"to": true, "initiator": true}
)

// Load проверяет каталог шаблонов владельца на диске. См. LoadFS.
func Load(dir string) (Catalog, Census, error) {
	return LoadFS(fsFromDir(dir), ".")
}

// LoadFS проверяет каталог шаблонов root в fsys: каждый подкаталог — шаблон.
// Находки возвращаются все сразу ошибкой типа Findings, и проверенного
// каталога тогда нет. Census — знаменатель: сколько шаблонов, файлов и блоков
// прочитано; пустой каталог — Census{} без ошибки, судить знаменатель —
// дело вызывающего. Имена на «.» пропускаются.
func LoadFS(fsys fs.FS, root string) (Catalog, Census, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return Catalog{}, Census{}, fmt.Errorf("notify/spec: каталог шаблонов %s не читается: %w", root, err)
	}
	var (
		census Census
		all    Findings
		cat    Catalog
	)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !e.IsDir() {
			all = append(all, Finding{File: name, Rule: RuleFile})
			continue
		}
		census.Templates++
		tpl, fs, err := loadTemplate(fsys, root, name, &census)
		if err != nil {
			return Catalog{}, census, err
		}
		all = append(all, fs...)
		if len(fs) == 0 {
			cat.Templates = append(cat.Templates, tpl)
		}
	}
	if len(all) > 0 {
		return Catalog{}, census, dedupe(all)
	}
	sort.Slice(cat.Templates, func(i, j int) bool { return cat.Templates[i].Name < cat.Templates[j].Name })
	return cat, census, nil
}

// attrInfo — объявление атрибута и то, насколько оно разобрано: проверки,
// зависящие от вида или обязательности, на неразобранном поле не
// выполняются — иначе одна ошибка объявления давала бы каскад находок.
type attrInfo struct {
	attr       Attr
	kindOK     bool
	presenceOK bool
}

// tmplState — шаблон в разборе.
type tmplState struct {
	dir        string
	t          Template
	classOK    bool
	attrs      map[string]*attrInfo
	subjectSet bool
	subject    map[string]*yaml.Node
	// subjectLocs — локали темы, объявленные в файле, в том числе с пустой
	// или неразобранной темой: согласие локалей судится по объявлению, иначе
	// пустая тема давала бы вторую находку.
	subjectLocs map[string]*yaml.Node
	bodyLocs    map[string]bool
}

func loadTemplate(fsys fs.FS, root, dir string, census *Census) (Template, Findings, error) {
	st := &tmplState{dir: dir, t: Template{Dir: dir, Subject: map[string]string{}}, attrs: map[string]*attrInfo{}, subject: map[string]*yaml.Node{}, subjectLocs: map[string]*yaml.Node{}, bodyLocs: map[string]bool{}}
	var out Findings
	entries, err := fs.ReadDir(fsys, path.Join(root, dir))
	if err != nil {
		return Template{}, nil, fmt.Errorf("notify/spec: каталог шаблона %s не читается: %w", dir, err)
	}
	var (
		haveNotification bool
		bodies           []string
		haveRevision     bool
	)
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "."):
		case e.IsDir():
			out = append(out, Finding{File: dir + "/" + name, Rule: RuleFile})
		case name == notificationFile:
			haveNotification = true
		case name == revisionFile:
			haveRevision = true
		case bodyFileRe.MatchString(name):
			bodies = append(bodies, name)
		default:
			out = append(out, Finding{File: dir + "/" + name, Rule: RuleFile})
		}
	}
	if !haveNotification {
		return Template{}, append(out, Finding{File: dir, Rule: RuleNoNotification}), nil
	}

	read := func(name string) (*yaml.Node, *collector, error) {
		c := &collector{file: dir + "/" + name}
		b, err := fs.ReadFile(fsys, path.Join(root, dir, name))
		if err != nil {
			return nil, c, fmt.Errorf("notify/spec: %s не читается: %w", c.file, err)
		}
		census.Files++
		var node yaml.Node
		if err := yaml.Unmarshal(b, &node); err != nil {
			c.add(nil, 0, "", RuleYAML, yamlReason(err))
			return nil, c, nil
		}
		return &node, c, nil
	}

	node, c, err := read(notificationFile)
	if err != nil {
		return Template{}, nil, err
	}
	if node != nil {
		st.parseNotification(c, node)
	}
	out = append(out, c.out...)

	var parsedBodies []Body
	for _, name := range bodies {
		loc := bodyFileRe.FindStringSubmatch(name)[1]
		if !slices.Contains(Locales(), loc) {
			out = append(out, Finding{File: dir + "/" + name, Rule: RuleLocale, Detail: loc})
			continue
		}
		st.bodyLocs[loc] = true
		node, c, err := read(name)
		if err != nil {
			return Template{}, nil, err
		}
		if node != nil {
			body := Body{Locale: loc, Blocks: parseBody(c, node, census)}
			st.checkBody(c, body)
			parsedBodies = append(parsedBodies, body)
		}
		out = append(out, c.out...)
	}
	sort.Slice(parsedBodies, func(i, j int) bool { return parsedBodies[i].Locale < parsedBodies[j].Locale })
	st.t.Bodies = parsedBodies

	if haveRevision {
		node, c, err := read(revisionFile)
		if err != nil {
			return Template{}, nil, err
		}
		if node != nil {
			r, fs := parseRevision(node, c.file)
			c.out = append(c.out, fs...)
			st.t.Revision, st.t.HasRevision = r, true
		}
		out = append(out, c.out...)
	}

	out = append(out, st.checkSubjectAndLocales()...)
	if len(out) > 0 {
		return Template{}, out, nil
	}
	for _, a := range st.attrs {
		st.t.Attrs = append(st.t.Attrs, a.attr)
	}
	sort.Slice(st.t.Attrs, func(i, j int) bool { return st.t.Attrs[i].Name < st.t.Attrs[j].Name })
	return st.t, nil, nil
}

func (st *tmplState) parseNotification(c *collector, root *yaml.Node) {
	m := c.mapping(docNode(root), 0, "")
	if m == nil {
		return
	}
	f := c.fields(m, "", 0, "name", "class", "recipient", "ttl", "limits", "attributes", "subject")

	if n, ok := f["name"]; !ok {
		c.add(m, 0, "name", RuleMissingField, "")
	} else if s, ok := c.str(n, 0, "name"); ok {
		if s != st.dir || !templateNameRe.MatchString(s) {
			c.add(n, 0, "name", RuleName, "каталог "+st.dir)
		}
		st.t.Name = s
	}

	if n, ok := f["class"]; !ok {
		c.add(m, 0, "class", RuleClass, "")
	} else if n.Kind != yaml.ScalarNode || (Class(n.Value) != ClassSecurity && Class(n.Value) != ClassNotice) {
		c.add(n, 0, "class", RuleClass, "")
	} else {
		st.t.Class, st.classOK = Class(n.Value), true
	}

	st.t.Recipient = RecipientAddress
	if n, ok := f["recipient"]; ok {
		if n.Kind != yaml.ScalarNode || !slices.Contains(Recipients(), Recipient(n.Value)) {
			c.add(n, 0, "recipient", RuleRecipient, "")
		} else {
			st.t.Recipient = Recipient(n.Value)
		}
	}

	st.parseTTL(c, m, f)
	st.parseLimits(c, f)
	st.parseAttributes(c, f)

	if n, ok := f["subject"]; !ok {
		c.add(m, 0, "subject", RuleMissingField, "")
	} else if sm := c.mapping(n, 0, "subject"); sm != nil {
		st.subjectSet = true
		keys := c.keys(sm, "subject", 0)
		for _, loc := range keys.order {
			v := keys.nodes[loc]
			field := "subject." + loc
			if !slices.Contains(Locales(), loc) {
				c.add(v, 0, field, RuleLocale, loc)
				continue
			}
			st.subjectLocs[loc] = v
			s, ok := c.str(v, 0, field)
			if !ok {
				continue
			}
			if s == "" {
				c.add(v, 0, field, RuleSubjectEmpty, "")
				continue
			}
			st.t.Subject[loc] = s
			st.subject[loc] = v
		}
	}

	if st.classOK && st.t.Class == ClassSecurity && len(st.t.Limits) == 0 && f["limits"] == nil {
		c.add(m, 0, "limits", RuleSecurityNeedsLimits, st.dir)
	}
}

func (st *tmplState) parseTTL(c *collector, m *yaml.Node, f map[string]*yaml.Node) {
	n, ok := f["ttl"]
	if !ok {
		c.add(m, 0, "ttl", RuleTTLPositive, "ttl нет")
		return
	}
	d, err := time.ParseDuration(n.Value)
	switch {
	case n.Kind != yaml.ScalarNode || err != nil:
		c.add(n, 0, "ttl", RuleTTLPositive, "не длительность")
	case d <= 0:
		c.add(n, 0, "ttl", RuleTTLPositive, "")
	case d < TTLMin || d > TTLMax:
		c.add(n, 0, "ttl", RuleTTLRange, "")
	case d%time.Second != 0:
		c.add(n, 0, "ttl", RuleTTLWholeSeconds, "")
	default:
		st.t.TTL = d
	}
}

func (st *tmplState) parseLimits(c *collector, f map[string]*yaml.Node) {
	n, ok := f["limits"]
	if !ok {
		return
	}
	if n.Kind != yaml.SequenceNode {
		c.add(n, 0, "limits", RuleFieldForm, "ожидается список")
		return
	}
	if len(n.Content) == 0 && st.classOK && st.t.Class == ClassSecurity {
		c.add(n, 0, "limits", RuleSecurityNeedsLimits, st.dir)
		return
	}
	for i, item := range n.Content {
		field := "limits[" + strconv.Itoa(i+1) + "]"
		lm := c.mapping(item, 0, field)
		if lm == nil {
			continue
		}
		lf := c.fields(lm, field, 0, "scope", "window", "max")
		lim := Limit{}
		good := true
		switch s, ok := lf["scope"]; {
		case !ok || s.Kind != yaml.ScalarNode || !slices.Contains(Scopes(), Scope(s.Value)):
			c.add(orNode(s, lm), 0, field+".scope", RuleLimitScope, "")
			good = false
		case Scope(s.Value) == ScopeRecipient && st.t.Recipient == RecipientFanout:
			c.add(s, 0, field+".scope", RuleFanoutRecipientLimit, "")
			good = false
		default:
			lim.Scope = Scope(s.Value)
		}
		if w, ok := lf["window"]; !ok {
			c.add(lm, 0, field+".window", RuleLimitWindow, "")
			good = false
		} else if d, err := time.ParseDuration(w.Value); w.Kind != yaml.ScalarNode || err != nil || d <= 0 || d%time.Second != 0 {
			c.add(w, 0, field+".window", RuleLimitWindow, "")
			good = false
		} else {
			lim.Window = d
		}
		if mx, ok := lf["max"]; !ok {
			c.add(lm, 0, field+".max", RuleLimitMax, "")
			good = false
		} else if v, err := strconv.ParseInt(mx.Value, 10, 64); mx.Kind != yaml.ScalarNode || mx.Tag != "!!int" || err != nil || v < 1 || v > math.MaxInt32 {
			c.add(mx, 0, field+".max", RuleLimitMax, "целое в [1..2147483647]")
			good = false
		} else {
			lim.Max = int(v)
		}
		if good {
			st.t.Limits = append(st.t.Limits, lim)
		}
	}
}

func (st *tmplState) parseAttributes(c *collector, f map[string]*yaml.Node) {
	n, ok := f["attributes"]
	if !ok {
		return
	}
	am := c.mapping(n, 0, "attributes")
	if am == nil {
		return
	}
	keys := c.keys(am, "attributes", 0)
	for _, name := range keys.order {
		v := keys.nodes[name]
		field := "attributes." + name
		if !attrNameRe.MatchString(name) || reservedAttrNames[name] {
			c.add(v, 0, field, RuleAttrName, name)
		}
		info := &attrInfo{attr: Attr{Name: name}}
		st.attrs[name] = info
		vm := c.mapping(v, 0, field)
		if vm == nil {
			continue
		}
		vf := c.fields(vm, field, 0, "type", "presence")
		if k, ok := vf["type"]; !ok || k.Kind != yaml.ScalarNode || !slices.Contains(AttrKinds(), form.Kind(k.Value)) {
			c.add(orNode(k, vm), 0, field+".type", RuleAttrKind, "")
		} else {
			info.attr.Kind, info.kindOK = form.Kind(k.Value), true
		}
		if p, ok := vf["presence"]; !ok {
			c.add(vm, 0, field+".presence", RulePresenceMissing, "")
		} else if p.Kind != yaml.ScalarNode || (Presence(p.Value) != PresenceRequired && Presence(p.Value) != PresenceOptional) {
			c.add(p, 0, field+".presence", RulePresenceValue, "")
		} else {
			info.attr.Presence, info.presenceOK = Presence(p.Value), true
		}
	}
}

// parseBody разбирает блоки тела. В тело попадают только блоки без находок
// разбора: проверки мест ссылок на сломанном блоке дали бы каскад.
func parseBody(c *collector, root *yaml.Node, census *Census) []Block {
	m := c.mapping(docNode(root), 0, "")
	if m == nil {
		return nil
	}
	f := c.fields(m, "", 0, "blocks")
	n, ok := f["blocks"]
	if !ok {
		c.add(m, 0, "blocks", RuleBodyEmpty, "")
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		c.add(n, 0, "blocks", RuleFieldForm, "ожидается список блоков")
		return nil
	}
	if len(n.Content) == 0 {
		c.add(n, 0, "blocks", RuleBodyEmpty, "")
		return nil
	}
	var out []Block
	for i, item := range n.Content {
		census.Blocks++
		before := len(c.out)
		b := parseBlock(c, item, i+1)
		if len(c.out) == before {
			out = append(out, b)
		}
	}
	return out
}

func parseBlock(c *collector, item *yaml.Node, idx int) Block {
	b := Block{Index: idx}
	field := "blocks[" + strconv.Itoa(idx) + "]"
	if item.Kind != yaml.MappingNode {
		c.add(item, idx, field, RuleFieldForm, "блок — отображение «вид: содержимое»")
		return b
	}
	keys := c.keys(item, field, idx)
	var kinds []string
	unknown := 0
	for _, k := range keys.order {
		switch {
		case k == "when":
		case slices.Contains(BlockKinds(), BlockKind(k)):
			kinds = append(kinds, k)
		default:
			unknown++
			c.add(keys.nodes[k], idx, field+"."+k, RuleBlockKind, k)
		}
	}
	switch {
	case len(kinds) == 0 && unknown > 0:
		return b // вид вне набора уже назван
	case len(kinds) != 1:
		c.add(item, idx, field, RuleBlockKind, "у блока ровно один вид из набора")
		return b
	}
	b.Kind = BlockKind(kinds[0])
	if w, ok := keys.nodes["when"]; ok {
		if s, ok := c.str(w, idx, field+".when"); ok {
			if s == "" {
				c.add(w, idx, field+".when", RuleFieldForm, "when — имя атрибута")
			}
			b.When = s
		}
	}
	v := keys.nodes[kinds[0]]
	vf := field + "." + kinds[0]
	switch b.Kind {
	case BlockHeading, BlockP, BlockWarning, BlockCode:
		if s, ok := c.str(v, idx, vf); ok {
			if s == "" {
				c.add(v, idx, vf, RuleBlockEmpty, "")
			}
			b.Text = s
		}
	case BlockList:
		if v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
			c.add(v, idx, vf, RuleBlockEmpty, "list — хотя бы один элемент")
			break
		}
		for j, it := range v.Content {
			jf := vf + "[" + strconv.Itoa(j+1) + "]"
			if s, ok := c.str(it, idx, jf); ok {
				if s == "" {
					c.add(it, idx, jf, RuleBlockEmpty, "")
				}
				b.Items = append(b.Items, s)
			}
		}
	case BlockKV:
		if v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
			c.add(v, idx, vf, RuleBlockEmpty, "kv — хотя бы одна пара")
			break
		}
		for j, it := range v.Content {
			jf := vf + "[" + strconv.Itoa(j+1) + "]"
			if it.Kind != yaml.MappingNode {
				c.add(it, idx, jf, RuleFieldForm, "пара — {key, value}")
				continue
			}
			pf := c.fields(it, jf, idx, "key", "value")
			key, kok := c.requiredStr(pf, it, idx, jf, "key")
			val, vok := c.requiredStr(pf, it, idx, jf, "value")
			if kok && hasSubst(key) {
				c.add(pf["key"], idx, jf+".key", RuleLiteralNoRef, "")
			}
			if kok && vok {
				b.Pairs = append(b.Pairs, Pair{Key: key, Value: val})
			}
		}
	case BlockButton:
		parseButton(c, v, idx, vf, &b)
	case BlockDivider:
		if v.Kind != yaml.MappingNode || len(v.Content) != 0 {
			c.add(v, idx, vf, RuleFieldForm, "divider: {}")
		}
	}
	return b
}

func parseButton(c *collector, v *yaml.Node, idx int, field string, b *Block) {
	if v.Kind != yaml.MappingNode {
		c.add(v, idx, field, RuleFieldForm, "button — {text, path[, token]}")
		return
	}
	f := c.fields(v, field, idx, "text", "path", "token")
	if text, ok := c.requiredStr(f, v, idx, field, "text"); ok {
		if hasSubst(text) {
			c.add(f["text"], idx, field+".text", RuleLiteralNoRef, "")
		}
		b.Button.Text = text
	}
	var pathNode = f["path"]
	p := ""
	if pathNode != nil {
		if s, ok := c.str(pathNode, idx, field+".path"); ok {
			p = s
		} else {
			return
		}
	}
	if tn, ok := f["token"]; ok {
		tok, ok := c.str(tn, idx, field+".token")
		if !ok {
			return
		}
		if !attrNameRe.MatchString(tok) {
			c.add(tn, idx, field+".token", RuleButtonTokenAttr, "")
			return
		}
		b.Button.Token = tok
		switch {
		case pathNode == nil || p == "":
			c.add(orNode(pathNode, v), idx, field+".path", RuleButtonTokenNeedsLiteral, "")
		case isAbsoluteLink(p):
			c.add(pathNode, idx, field+".path", RuleLinkIsPathOrToken, "")
		default:
			if _, err := form.ParsePath(p); err != nil {
				c.add(pathNode, idx, field+".path", RuleButtonPathForm, err.Error())
			}
		}
		b.Button.Path = p
		return
	}
	switch {
	case pathNode == nil || p == "":
		c.add(orNode(pathNode, v), idx, field+".path", RuleMissingField, "")
	case isAbsoluteLink(p):
		c.add(pathNode, idx, field+".path", RuleLinkIsPathOrToken, "")
	case !attrNameRe.MatchString(p):
		c.add(pathNode, idx, field+".path", RuleButtonPathAttr, "")
	}
	b.Button.Path = p
}

// isAbsoluteLink — значение называет узел или схему: ссылка — только путь или
// токен, origin задаёт установка.
func isAbsoluteLink(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(s, "//") || (strings.Contains(s, ":") && !strings.HasPrefix(s, "/"))
}

// checkBody судит места ссылок блоков тела одной функцией RefSites (CX1-51):
// необъявленный атрибут, секрет вне code и button.token, вид атрибута у
// кнопки, правила when и optional (Д21).
func (st *tmplState) checkBody(c *collector, body Body) {
	for _, b := range body.Blocks {
		whenValid, whenBad := false, false
		if b.When != "" {
			a, ok := st.attrs[b.When]
			switch {
			case !ok:
				c.add(nil, b.Index, blockField(b)+".when", RuleWhenUndeclared, b.When)
				whenBad = true
			case !a.presenceOK:
				whenBad = true // обязательность не разобрана — названо в notification.yaml
			case a.attr.Presence == PresenceRequired:
				c.add(nil, b.Index, blockField(b)+".when", RuleWhenOnRequired, b.When)
				whenBad = true
			default:
				whenValid = true
			}
		}
		for _, site := range RefSites(b) {
			sf := blockField(b) + "." + site.Place
			if site.Form == SiteText {
				if _, err := Segments(site.Text); err != nil {
					c.add(nil, b.Index, sf, RuleRef, "")
					continue
				}
			}
			for _, ref := range site.Refs() {
				a, ok := st.attrs[ref]
				if !ok {
					c.add(nil, b.Index, sf, RuleUndeclaredAttr, ref)
					continue
				}
				if a.kindOK {
					switch {
					case site.Form == SiteAttr && site.Place == "path" && a.attr.Kind != form.KindPath:
						c.add(nil, b.Index, sf, RuleButtonPathAttr, ref)
					case site.Form == SiteAttr && site.Place == "token" && a.attr.Kind != form.KindToken:
						c.add(nil, b.Index, sf, RuleButtonTokenAttr, ref)
					case a.attr.Kind == form.KindSecret && b.Kind != BlockCode:
						c.add(nil, b.Index, sf, RuleSecretPlace, ref)
					}
				}
				if a.presenceOK && a.attr.Presence == PresenceOptional && !whenBad {
					switch {
					case b.When == "":
						c.add(nil, b.Index, sf, RuleOptionalNeedsWhen, ref)
					case whenValid && b.When != ref:
						c.add(nil, b.Index, sf, RuleWhenOnOther, ref)
					}
				}
			}
		}
	}
}

func blockField(b Block) string {
	return "blocks[" + strconv.Itoa(b.Index) + "]." + string(b.Kind)
}

// checkSubjectAndLocales судит ссылки темы (только required, без секрета) и
// согласие локалей темы и тела.
func (st *tmplState) checkSubjectAndLocales() Findings {
	c := &collector{file: st.dir + "/" + notificationFile}
	for _, loc := range sortedKeys(st.t.Subject) {
		field := "subject." + loc
		n := st.subject[loc]
		segs, err := Segments(st.t.Subject[loc])
		if err != nil {
			c.add(n, 0, field, RuleRef, "")
			continue
		}
		for _, seg := range segs {
			if seg.Attr == "" {
				continue
			}
			a, ok := st.attrs[seg.Attr]
			switch {
			case !ok:
				c.add(n, 0, field, RuleUndeclaredAttr, seg.Attr)
				continue
			case a.kindOK && a.attr.Kind == form.KindSecret:
				c.add(n, 0, field, RuleSecretPlace, seg.Attr)
			case a.presenceOK && a.attr.Presence == PresenceOptional:
				c.add(n, 0, field, RuleSubjectRequiredOnly, seg.Attr)
			}
			a.attr.Subject = true
		}
	}
	if st.subjectSet {
		for _, loc := range Locales() {
			sn, inSubject := st.subjectLocs[loc]
			switch {
			case inSubject && !st.bodyLocs[loc]:
				c.add(sn, 0, "subject."+loc, RuleLocalesAgree, "тела "+loc+" нет")
			case !inSubject && st.bodyLocs[loc]:
				c.out = append(c.out, Finding{File: st.dir + "/body." + loc + ".yaml", Rule: RuleLocalesAgree, Detail: "темы " + loc + " нет"})
			case !inSubject && !st.bodyLocs[loc]:
				// Ни темы, ни тела на локали набора (NTF-3 Р21).
				c.out = append(c.out, Finding{File: st.dir + "/body." + loc + ".yaml", Rule: RuleLocaleMissing, Detail: loc})
			}
		}
	}
	return c.out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupe(fs Findings) Findings {
	seen := map[Finding]bool{}
	out := fs[:0:0]
	for _, f := range fs {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func yamlReason(err error) string {
	// Текст разборщика называет строку и причину. Шаблон данных извещения не
	// несёт, поэтому секрета в этом тексте быть не может.
	return strings.TrimPrefix(err.Error(), "yaml: ")
}
