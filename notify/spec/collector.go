// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"io/fs"
	"os"
	"slices"

	"gopkg.in/yaml.v3"
)

// collector копит находки одного файла. Разбор идёт по узлам yaml.Node, а не
// декодированием в структуру: так видны неизвестный ключ, повтор ключа и
// строка каждой находки.
type collector struct {
	file string
	out  Findings
}

func (c *collector) add(n *yaml.Node, block int, field, rule, detail string) {
	line := 0
	if n != nil {
		line = n.Line
	}
	c.out = append(c.out, Finding{File: c.file, Line: line, Block: block, Field: field, Rule: rule, Detail: detail})
}

// docNode — корневой узел документа; пустой документ — nil.
func docNode(root *yaml.Node) *yaml.Node {
	if root == nil {
		return nil
	}
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		return root.Content[0]
	}
	return root
}

// mapping — узел-отображение либо находка «не той формы».
func (c *collector) mapping(n *yaml.Node, block int, field string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		c.add(n, block, field, RuleFieldForm, "ожидается отображение")
		return nil
	}
	return n
}

// orderedKeys — ключи отображения в порядке файла.
type orderedKeys struct {
	order []string
	nodes map[string]*yaml.Node
}

// keys — ключи отображения; ключ не строкой и повтор ключа — находки.
func (c *collector) keys(m *yaml.Node, prefix string, block int) orderedKeys {
	k := orderedKeys{nodes: map[string]*yaml.Node{}}
	for i := 0; i+1 < len(m.Content); i += 2 {
		kn, vn := m.Content[i], m.Content[i+1]
		if kn.Kind != yaml.ScalarNode {
			c.add(kn, block, prefix, RuleFieldForm, "ключ — строка")
			continue
		}
		if _, dup := k.nodes[kn.Value]; dup {
			c.add(kn, block, join(prefix, kn.Value), RuleDuplicateKey, kn.Value)
			continue
		}
		k.order = append(k.order, kn.Value)
		k.nodes[kn.Value] = vn
	}
	return k
}

// fields — ключи отображения из закрытого перечня allowed; прочие — находка
// «ключ вне формата».
func (c *collector) fields(m *yaml.Node, prefix string, block int, allowed ...string) map[string]*yaml.Node {
	k := c.keys(m, prefix, block)
	out := make(map[string]*yaml.Node, len(k.order))
	for _, name := range k.order {
		if !slices.Contains(allowed, name) {
			c.add(k.nodes[name], block, join(prefix, name), RuleUnknownKey, name)
			continue
		}
		out[name] = k.nodes[name]
	}
	return out
}

// str — строковый скаляр либо находка «не той формы».
func (c *collector) str(n *yaml.Node, block int, field string) (string, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		c.add(n, block, field, RuleFieldForm, "ожидается строка")
		return "", false
	}
	return n.Value, true
}

// requiredStr — обязательное непустое строковое поле key отображения.
func (c *collector) requiredStr(f map[string]*yaml.Node, parent *yaml.Node, block int, prefix, key string) (string, bool) {
	n, ok := f[key]
	if !ok {
		c.add(parent, block, join(prefix, key), RuleMissingField, "")
		return "", false
	}
	s, ok := c.str(n, block, join(prefix, key))
	if !ok {
		return "", false
	}
	if s == "" {
		c.add(n, block, join(prefix, key), RuleBlockEmpty, "")
		return "", false
	}
	return s, true
}

// orNode — n, а если его нет — запасной узел (для строки находки).
func orNode(n, fallback *yaml.Node) *yaml.Node {
	if n != nil {
		return n
	}
	return fallback
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func fsFromDir(dir string) fs.FS { return os.DirFS(dir) }
