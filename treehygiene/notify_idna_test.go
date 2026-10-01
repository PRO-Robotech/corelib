// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

func idna(t *testing.T, root string) treehygiene.IDNAReport {
	t.Helper()
	r, err := treehygiene.AuditIDNASingular(root, "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, kindsOf(r.Findings))
	return r
}

// NTF1-B27 (гейт): вызов idna.Lookup.ToASCII в services/notify — находка;
// значение-функция в kaname — находка; близнец — strings.ToLower над именем
// заголовка — гейт молчит.
func TestNTF1B27Gate_IDNAOutsideAddressIsFound(t *testing.T) {
	r := idna(t, goSynth(t, kachoModule, map[string]string{
		"services/notify/hdr/h.go": `
package hdr

import (
	"strings"

	"golang.org/x/net/idna"
)

// Key — ключ заголовка.
func Key(name string) string { return strings.ToLower(name) }

// Domain — вторая нормализация домена.
func Domain(d string) (string, error) { return idna.Lookup.ToASCII(d) }
`,
	}))
	if len(r.Findings) != 1 || !strings.HasPrefix(r.Findings[0].Position, "services/notify/hdr/h.go:13") {
		t.Fatalf("ожидалась одна находка на вызове ToASCII:\n%s", kindsOf(r.Findings))
	}

	r = idna(t, goSynth(t, "example.invalid/kaname", map[string]string{
		"internal/norm/n.go": `
package norm

import "golang.org/x/net/idna"

// F — значение-функция профиля.
var F = idna.Lookup.ToASCII
`,
	}))
	if len(r.Findings) != 1 || !strings.HasPrefix(r.Findings[0].Position, "internal/norm/n.go:6") {
		t.Fatalf("значение-функция не найдена:\n%s", kindsOf(r.Findings))
	}

	r = idna(t, goSynth(t, kachoModule, map[string]string{
		"services/notify/hdr/h.go": `
package hdr

import "strings"

// Key — ключ заголовка.
func Key(name string) string { return strings.ToLower(name) }
`,
	}))
	if len(r.Findings) != 0 || r.Nodes != 0 {
		t.Fatalf("близнец: находок %d, узлов %d", len(r.Findings), r.Nodes)
	}
}

// Прогон по corelib: вызов IDNA ровно в notify/address, находок 0.
func TestNTF1B27Gate_OnCorelib(t *testing.T) {
	r, err := treehygiene.AuditIDNASingular(corelibRoot(t), "api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s", r)
	if len(r.Findings) != 0 {
		t.Fatalf("находки:\n%s", kindsOf(r.Findings))
	}
	if r.Nodes == 0 {
		t.Fatal("узлов IDNA 0 — допустимое место notify/address не увидено, обход слеп")
	}
}

func domainReceivers(t *testing.T, root string) treehygiene.DomainReport {
	t.Helper()
	r, err := treehygiene.AuditDomainReceivers(root, "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, kindsOf(r.Findings))
	return r
}

// NTF1-B27 (гейт, приёмник address.Domain), инъекции (1)–(3) и (4) формы (а)
// CX1-56: каждая — находка с координатой и видом.
func TestNTF1B27Receiver_InjectionsAreFound(t *testing.T) {
	for name, tc := range map[string]struct {
		module, file, src string
		kind              treehygiene.DomainKind
	}{
		"(1) параметр типа address.Domain в kaname": {"example.invalid/kaname", "internal/k/k.go", `
package k

import "github.com/PRO-Robotech/corelib/notify/address"

// Use — приёмник.
func Use(d address.Domain) bool { _, err := d.Value(); return err == nil }
`, treehygiene.DomainTypeReference},
		"(2) ошибка производителя отброшена": {kachoModule, "services/notify/d/d.go", `
package d

import "github.com/PRO-Robotech/corelib/notify/address"

// Same — домены равны.
func Same(a string) bool {
	d, _ := address.NormalizeDomain(a)
	v, err := d.Value()
	return err == nil && v != ""
}
`, treehygiene.DomainErrorDiscarded},
		"(3) подстановка в параметр типа": {kachoModule, "services/notify/d/d.go", `
package d

import "github.com/PRO-Robotech/corelib/notify/address"

func zero[T any](T) T { var z T; return z }

// Zero — нулевое значение домена без имени типа.
func Zero(a string) error {
	d, err := address.NormalizeDomain(a)
	if err != nil {
		return err
	}
	_, err = zero(d).Value()
	return err
}
`, treehygiene.DomainTypeArgument},
		"(4) значение-функция производителя": {kachoModule, "services/notify/d/d.go", `
package d

import "github.com/PRO-Robotech/corelib/notify/address"

// Via — производитель через значение-функцию.
func Via(s string) bool {
	f := address.NormalizeDomain
	d, err := f(s)
	if err != nil {
		return false
	}
	v, err := d.Value()
	return err == nil && v != ""
}
`, treehygiene.DomainProducerValue},
	} {
		t.Run(name, func(t *testing.T) {
			r := domainReceivers(t, goSynth(t, tc.module, map[string]string{tc.file: tc.src}))
			found := false
			for _, f := range r.Findings {
				if treehygiene.DomainKind(f.Kind) == tc.kind && strings.HasPrefix(f.Position, tc.file) {
					found = true
				}
			}
			if !found {
				t.Fatalf("вида %s нет:\n%s", tc.kind, kindsOf(r.Findings))
			}
		})
	}
}

// Близнец: сравнение доменов NTF-4 и ключ kaname из Normalize — производителей
// 2, узлов 0; близнец инъекции (2) — ошибка под именем.
func TestNTF1B27Receiver_TwinIsSilent(t *testing.T) {
	r := domainReceivers(t, goSynth(t, kachoModule, map[string]string{
		"services/notify/d/d.go": `
package d

import "github.com/PRO-Robotech/corelib/notify/address"

// Same — домены NTF-4.
func Same(a, b string) (bool, error) {
	d1, err1 := address.NormalizeDomain(a)
	d2, err2 := address.NormalizeDomain(b)
	if err1 != nil || err2 != nil {
		return false, err1
	}
	if _, err := d1.Value(); err != nil {
		return false, err
	}
	return d1 == d2, nil
}

// Key — ключ на адрес.
func Key(s string) (string, error) {
	n, err := address.Normalize(s)
	if err != nil {
		return "", err
	}
	return n.Value()
}
`,
	}))
	if len(r.Findings) != 0 {
		t.Fatalf("близнец дал находки:\n%s", kindsOf(r.Findings))
	}
	if r.ProducerCalls != 2 {
		t.Fatalf("вызовов производителя %d, ожидалось 2", r.ProducerCalls)
	}
}

func valueErr(t *testing.T, root string) treehygiene.ValueErrReport {
	t.Helper()
	r, err := treehygiene.AuditValueErrorDiscard(root, "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, kindsOf(r.Findings))
	return r
}

// УК46, CX1-41: ошибка Value() непрозрачного типа, присвоенная «_», — находка;
// близнец — ошибка обработана.
func TestUK46_ValueErrorDiscardedIsFound(t *testing.T) {
	r := valueErr(t, goSynth(t, kachoModule, map[string]string{
		"services/notify/v/v.go": `
package v

import "github.com/PRO-Robotech/corelib/notify/form"

// S — значение пути.
func S(p form.Path) string {
	s, _ := p.Value()
	return s
}
`,
	}))
	if len(r.Findings) != 1 || !strings.HasPrefix(r.Findings[0].Position, "services/notify/v/v.go:7") {
		t.Fatalf("инъекция s, _ := p.Value() не найдена:\n%s", kindsOf(r.Findings))
	}
	r = valueErr(t, goSynth(t, kachoModule, map[string]string{
		"services/notify/v/v.go": `
package v

import "github.com/PRO-Robotech/corelib/notify/form"

// S — значение пути.
func S(p form.Path) (string, error) {
	s, err := p.Value()
	if err != nil {
		return "", err
	}
	return s, nil
}
`,
	}))
	if len(r.Findings) != 0 || r.Calls != 1 {
		t.Fatalf("близнец: находок %d, вызовов %d", len(r.Findings), r.Calls)
	}
}
