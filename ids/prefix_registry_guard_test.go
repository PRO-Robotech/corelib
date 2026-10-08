// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import (
	"fmt"
	"regexp"
	"testing"
)

// Страж реестров приставок: каждая запись реестра — в форме своего
// генератора, и ни одна не записана дважды.
//
// Реестров два, у каждого своя форма:
//   - дефисный (hyphenFormPrefixes) — приставка NewHyphenID: 2..3 знака,
//     первый — строчная латинская буква, остальные — строчные буквы и цифры;
//   - слитный (allKnownPrefixValues) — приставка NewID: ровно 3 строчных
//     буквы или цифры.
//
// Запись вне формы генератора — приставка, которой генератор не произведёт
// (либо запаникует на ней), а маршрутизатор её примет: классификатор и
// чеканка разошлись молча. Повтор записи — признак того, что приставку
// заводили второй раз, не найдя первой: два обоснования одного предмета, из
// которых верно в лучшем случае одно. Реестр судится как СРЕЗ, а не как
// множество KnownHyphenPrefixes(): множество повтор поглощает.
var (
	hyphenRegistryForm = regexp.MustCompile(`^[a-z][a-z0-9]{1,2}$`)
	concatRegistryForm = regexp.MustCompile(`^[a-z0-9]{3}$`)
)

// prefixRegistryFindings — находки по одному реестру: запись вне формы,
// повтор записи и пустой обход. Пустой реестр — не «находок ноль», а «читать
// нечего»: форма объявления изменилась, и страж осматривал бы пустоту.
func prefixRegistryFindings(registry string, entries []string, form *regexp.Regexp) []string {
	if len(entries) == 0 {
		return []string{fmt.Sprintf("%s: реестр пуст — страж осмотрел 0 записей, вердикта нет", registry)}
	}
	var findings []string
	firstAt := make(map[string]int, len(entries))
	for i, p := range entries {
		if !form.MatchString(p) {
			findings = append(findings, fmt.Sprintf("%s[%d] = %q вне формы приставки %s", registry, i, p, form))
		}
		if j, dup := firstAt[p]; dup {
			findings = append(findings, fmt.Sprintf("%s[%d] = %q повторяет запись %s[%d]", registry, i, p, registry, j))
			continue
		}
		firstAt[p] = i
	}
	return findings
}

// TestPrefixRegistries_EntriesAreWellFormedAndUnique — страж на живом дереве:
// оба реестра в форме своих генераторов и без повторов. Печатает объём
// осмотренного, чтобы «находок ноль» отличалось от «осмотрено ноль».
func TestPrefixRegistries_EntriesAreWellFormedAndUnique(t *testing.T) {
	hyphen := hyphenFormPrefixes
	concat := allKnownPrefixValues()

	findings := prefixRegistryFindings("hyphenFormPrefixes", hyphen, hyphenRegistryForm)
	findings = append(findings, prefixRegistryFindings("allKnownPrefixValues", concat, concatRegistryForm)...)
	for _, f := range findings {
		t.Error(f)
	}
	t.Logf("перепись: дефисный реестр — записей %d, слитный — записей %d; находок %d",
		len(hyphen), len(concat), len(findings))
}

// TestPrefixRegistryGuard_InjectionOfARealEntryTwiceIsFound — инъекция
// НАСТОЯЩИМ входом из дерева: живой дефисный реестр с повторённой настоящей
// записью (PrefixTokenFamilyHyphen) краснеет и называет повтор с координатой
// обеих записей. Меняется ровно один факт против живого реестра — лишняя
// запись в конце.
func TestPrefixRegistryGuard_InjectionOfARealEntryTwiceIsFound(t *testing.T) {
	entries := append(append([]string(nil), hyphenFormPrefixes...), PrefixTokenFamilyHyphen)
	findings := prefixRegistryFindings("hyphenFormPrefixes", entries, hyphenRegistryForm)
	if len(findings) != 1 {
		t.Fatalf("повтор настоящей записи %q: ожидалась ровно 1 находка, получено %d: %q",
			PrefixTokenFamilyHyphen, len(findings), findings)
	}
	want := regexp.MustCompile(fmt.Sprintf(`^hyphenFormPrefixes\[%d\] = "tfm" повторяет запись hyphenFormPrefixes\[\d+\]$`, len(entries)-1))
	if !want.MatchString(findings[0]) {
		t.Errorf("находка %q не называет повтор и его координату (%s)", findings[0], want)
	}
}

// TestPrefixRegistryGuard_FormInjections — каждая незаконная форма записи
// краснеет ровно одной находкой и называет себя; законные формы обоих
// реестров (двух- и трёхзнаковая дефисная, слитная с цифрой) молчат.
func TestPrefixRegistryGuard_FormInjections(t *testing.T) {
	cases := []struct {
		name     string
		registry string
		entries  []string
		form     *regexp.Regexp
		want     int
	}{
		{"законный близнец: дефисный реестр", "h", []string{"hss", "ak", "mt", "tfm"}, hyphenRegistryForm, 0},
		{"законный близнец: слитный реестр", "c", []string{"b1g", "fd8", "net"}, concatRegistryForm, 0},
		{"заглавные", "h", []string{"hss", "HSS"}, hyphenRegistryForm, 1},
		{"один знак", "h", []string{"hss", "h"}, hyphenRegistryForm, 1},
		{"четыре знака", "h", []string{"hss", "hsss"}, hyphenRegistryForm, 1},
		{"дефис внутри", "h", []string{"hss", "h-s"}, hyphenRegistryForm, 1},
		{"цифра первой", "h", []string{"hss", "1ss"}, hyphenRegistryForm, 1},
		{"пустая запись", "h", []string{"hss", ""}, hyphenRegistryForm, 1},
		{"слитная в два знака", "c", []string{"net", "ne"}, concatRegistryForm, 1},
		{"слитная с заглавной", "c", []string{"net", "Net"}, concatRegistryForm, 1},
		{"повтор в слитном", "c", []string{"net", "sub", "net"}, concatRegistryForm, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixRegistryFindings(tc.registry, tc.entries, tc.form)
			if len(got) != tc.want {
				t.Fatalf("%v: ожидалось находок %d, получено %d: %q", tc.entries, tc.want, len(got), got)
			}
		})
	}
}

// TestPrefixRegistryGuard_EmptyRegistryIsNotGreen — проверка предпосылки:
// пустой реестр даёт находку «осмотрено 0», а не молчание.
func TestPrefixRegistryGuard_EmptyRegistryIsNotGreen(t *testing.T) {
	got := prefixRegistryFindings("hyphenFormPrefixes", nil, hyphenRegistryForm)
	if len(got) != 1 {
		t.Fatalf("пустой реестр: ожидалась 1 находка «осмотрено 0», получено %d: %q", len(got), got)
	}
}
