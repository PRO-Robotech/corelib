// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/PRO-Robotech/corelib/notify/form"
)

// FingerprintVersion — версия алгоритма отпечатка набора. Первая выпущенная —
// v1, и она уже включает класс и обязательность (З4). Смена состава набора
// поднимает версию; «набор тот же» решается пересчётом (SameSet), а не
// сравнением записанных строк.
const FingerprintVersion = "v1"

// Set — набор ревизии шаблона (Р7): имя, вид, обязательность и вхождение в
// тему каждого объявленного атрибута и класс шаблона. Текст тела, ttl и
// limits в набор не входят.
type Set struct {
	Class Class
	Attrs []Attr
}

// SetOf — набор ревизии проверенного шаблона.
func SetOf(t Template) Set {
	return Set{Class: t.Class, Attrs: append([]Attr(nil), t.Attrs...)}
}

// SetFingerprint — ЕДИНСТВЕННАЯ функция отпечатка набора (CX1-45 (а)).
// Каноническая форма: атрибуты по имени; каждое поле закодировано явно, с
// длиной — name, kind, presence, subject каждого атрибута, затем class.
// Порядок ключей YAML, кавычки, комментарии и пробелы в неё не попадают:
// отпечаток считается по разобранному набору, а не по байтам файла.
func SetFingerprint(s Set) string {
	attrs := append([]Attr(nil), s.Attrs...)
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })

	h := sha256.New()
	field := func(v string) {
		_, _ = fmt.Fprintf(h, "%d:%s,", len(v), v)
	}
	field("kacho.notify.set")
	field(FingerprintVersion)
	field(strconv.Itoa(len(attrs)))
	for _, a := range attrs {
		field(a.Name)
		field(string(a.Kind))
		field(string(a.Presence))
		if a.Subject {
			field("1")
		} else {
			field("0")
		}
	}
	field(string(s.Class))
	return FingerprintVersion + ":sha256:" + hex.EncodeToString(h.Sum(nil))
}

// SameSet — «набор тот же»: пересчёт обоих наборов ТЕКУЩЕЙ функцией
// отпечатка. Смена версии алгоритма при прежнем наборе поэтому ревизию не
// поднимает (CX1-45 (б)).
func SameSet(a, b Set) bool { return SetFingerprint(a) == SetFingerprint(b) }

// ReadSetOnly — узкое чтение набора ревизии из notification.yaml каталога
// шаблона dir (CX1-45 (в)): только класс, атрибуты с видом и обязательностью
// и вхождение в тему, без правил формата текущей версии. Так читается база
// `-check -base`: формат, ужесточённый после базы, базу «ненайденной» не
// делает. Без класса или без вида и обязательности атрибута набора нет — это
// ошибка, а не пустой набор.
func ReadSetOnly(fsys fs.FS, dir string) (Set, error) {
	file := path.Join(dir, notificationFile)
	b, err := fs.ReadFile(fsys, file)
	if err != nil {
		return Set{}, fmt.Errorf("notify/spec: база %s не читается: %w", file, err)
	}
	var doc struct {
		Class      string `yaml:"class"`
		Attributes map[string]struct {
			Type     string `yaml:"type"`
			Presence string `yaml:"presence"`
		} `yaml:"attributes"`
		Subject map[string]string `yaml:"subject"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return Set{}, fmt.Errorf("notify/spec: база %s не разбирается: %w", file, err)
	}
	if doc.Class == "" {
		return Set{}, fmt.Errorf("notify/spec: база %s без класса", file)
	}
	inSubject := map[string]bool{}
	for _, text := range doc.Subject {
		for _, m := range substRe.FindAllStringSubmatch(text, -1) {
			inSubject[strings.TrimSpace(m[1])] = true
		}
	}
	set := Set{Class: Class(doc.Class)}
	for name, a := range doc.Attributes {
		if a.Type == "" || a.Presence == "" {
			return Set{}, fmt.Errorf("notify/spec: база %s: атрибут %s без вида или обязательности", file, name)
		}
		set.Attrs = append(set.Attrs, Attr{Name: name, Kind: form.Kind(a.Type), Presence: Presence(a.Presence), Subject: inSubject[name]})
	}
	sort.Slice(set.Attrs, func(i, j int) bool { return set.Attrs[i].Name < set.Attrs[j].Name })
	return set, nil
}
