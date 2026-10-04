// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package resourceevent

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/spec"
	"github.com/PRO-Robotech/corelib/subscription"
)

func probeInputs() Inputs {
	return Inputs{
		Service: "svc", Module: "svc", SchemaRev: 1, TTL: 72 * time.Hour, Table: "svc_journal",
		Columns: Columns{Kind: "resource_kind", ID: "resource_id", Change: "event_type", Payload: "payload",
			Project: "project_id", Initiator: "initiator", OccurredAt: "created_at"},
		Kinds: []Kind{
			{Name: "Repository", NameForm: NameFormNone, Scope: ScopeProject},
			{Name: "Volume", NameForm: NameFormDNS, Scope: ScopeProject},
		},
		Changes:      []Change{{Word: "created", Change: "CREATED"}, {Word: "deleted", Change: "DELETED"}, {Word: "updated", Change: "UPDATED"}},
		SignalChange: "updated",
	}
}

// «Версии тела» (З10, CX3J-01 (а)): выпущенная версия шаблона не правится —
// отпечаток v1 записан литералом. Правка текста v1 или подставляемой
// постоянной меняет отпечаток, и проба краснеет: смена шаблона — новая
// версия рядом с прежней, иначе тела, применённые у потребителя, перестанут
// выводиться из своей версии.
func TestReleasedVersionsAreFrozen(t *testing.T) {
	const v1Frozen = "v1:sha256:dc41707ebb6a536ef205a6edfe395604d50bb9ef5d679db10ad8777f4f6b66cd"
	if got := fingerprint(1, v1Text); got != v1Frozen {
		t.Fatalf("отпечаток выпущенной версии 1 сменился: %s, выпущен %s — заведите версию 2, v1 не правится", got, v1Frozen)
	}
	if rel := Released(); len(rel) != len(released) || rel[len(rel)-1] != Current() {
		t.Fatalf("выпущенных версий %v, в реестре %d, действующая %s", rel, len(released), Current())
	}
}

// З10 «Версии тела» (Е10 (1) NTF-1, CX1-76 (д)): на каждую выпущенную версию
// шаблона и обе формы миграции (первая — функция и триггер; CREATE OR REPLACE
// с прежним определением в откате) входы, извлечённые из вывода, выводят
// шаблон побайтно в то же тело.
func TestExtractedInputsRenderTheSameBody(t *testing.T) {
	for _, v := range Released() {
		in := probeInputs()
		next := probeInputs()
		next.Kinds = []Kind{next.Kinds[0], {Name: "Snapshot", NameForm: NameFormDNS, Scope: ScopeProject}, next.Kinds[1]}
		next.SchemaRev = 2
		for name, f := range map[string]File{
			"первая":  {Up: Body{Version: v, Inputs: in}},
			"замена":  {Up: Body{Version: v, Inputs: next}, Prev: &Body{Version: v, Inputs: in}},
			"кластер": {Up: Body{Version: v, Inputs: withClusterKind(in)}},
		} {
			out, err := Render(f)
			if err != nil {
				t.Fatalf("%s %s: %v", v, name, err)
			}
			p, ok := Recognize([]byte(out))
			if !ok {
				t.Fatalf("%s %s: вывод шаблона не признан своим же разбором", v, name)
			}
			again, err := Render(p.File)
			if err != nil || again != out {
				t.Fatalf("%s %s: вывод на извлечённых входах расходится (%v)", v, name, err)
			}
			if p.Signal != spec.FeedSignalObjectType+":svc" {
				t.Fatalf("%s %s: строка сигнала %q", v, name, p.Signal)
			}
			if name == "замена" && strings.Contains(strings.ToUpper(out), "CREATE TRIGGER") {
				t.Fatalf("замена пересоздаёт триггер")
			}
		}
	}
}

func withClusterKind(in Inputs) Inputs {
	in.Kinds = append([]Kind{{Name: "AddressPlan", NameForm: NameFormNone, Scope: ScopeCluster}}, in.Kinds...)
	return in
}

// Инъекция одного факта в вывод — тело не признано; близнец — вывод как есть.
// Правка, согласованная во всём теле (строка таблицы видов другой законной
// формы), — вывод шаблона на других входах: его признание законно, а
// расхождение с объявлением журнала судит notifygen -check.
func TestRecognizeRefusesAHandEditedBody(t *testing.T) {
	out, err := Render(File{Up: Body{Version: Current(), Inputs: probeInputs()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Recognize([]byte(out)); !ok {
		t.Fatal("близнец: вывод шаблона не признан")
	}
	for name, edit := range map[string][2]string{
		"флаг":          {"kacho_feed.enabled", "kacho_feed.enabledx"},
		"вставка ленты": {"'pending'", "'sent'"},
		"заголовок":     {"-- schema_rev: 1\n", "-- schema_rev: 2\n"},
		"строка таблицы видов вне формы": {"('Volume', 'dns', 'project')", "('Volume', 'dns', 'project', 1)"},
		"строка сигнала":                 {"notification_feed:svc", "notification_feed:svx"},
		"лишний оператор":                {"-- +goose Down\n", "-- +goose Down\nDELETE FROM \"svc_notification_outbox\";\n"},
	} {
		if _, ok := Recognize([]byte(strings.Replace(out, edit[0], edit[1], 1))); ok {
			t.Errorf("%s: правленое тело признано выводом шаблона", name)
		}
	}
}

// Каждое слово, попадающее в текст тела, — из закрытой формы: кавычка,
// пробел, чужое слово рода — отказ ErrInputs, тела нет. Близнец — входы пробы.
func TestValidateRefusesWordsOutsideTheirForm(t *testing.T) {
	if err := probeInputs().Validate(); err != nil {
		t.Fatalf("близнец: %v", err)
	}
	cases := map[string]func(*Inputs){
		"кавычка в виде":          func(in *Inputs) { in.Kinds[0].Name = "Repo'sitory" },
		"кавычка в слове рода":    func(in *Inputs) { in.Changes[0].Word = "creat'ed" },
		"род вне набора":          func(in *Inputs) { in.Changes[0].Change = "MOVED" },
		"колонка с пробелом":      func(in *Inputs) { in.Columns.Payload = "payload ; drop" },
		"таблица с кавычкой":      func(in *Inputs) { in.Table = `svc"journal` },
		"служба вне формы":        func(in *Inputs) { in.Service = "Svc" },
		"модуль вне DNS-метки":    func(in *Inputs) { in.Module = "svc_1" },
		"ключ сигнала в таблице":  func(in *Inputs) { in.Kinds[0].Name = spec.FeedSignalKey },
		"слово сигнала не UPDATE": func(in *Inputs) { in.SignalChange = "created" },
		"виды не по порядку":      func(in *Inputs) { in.Kinds[0], in.Kinds[1] = in.Kinds[1], in.Kinds[0] },
		"ttl дробный":             func(in *Inputs) { in.TTL = 1500 * time.Millisecond },
		"schema_rev ноль":         func(in *Inputs) { in.SchemaRev = 0 },
	}
	for name, mutate := range cases {
		in := probeInputs()
		in.Kinds = append([]Kind(nil), in.Kinds...)
		in.Changes = append([]Change(nil), in.Changes...)
		mutate(&in)
		if err := in.Validate(); !errors.Is(err, ErrInputs) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := Render(File{Up: Body{Version: Current(), Inputs: in}}); !errors.Is(err, ErrInputs) {
			t.Errorf("%s: тело выведено: %v", name, err)
		}
	}
}

// Слово строки сигнала — единственное слово словаря, переводимое в род
// строки сигнала notify/spec; ноль или два — отказ.
func TestSignalChangeWordIsTheOnlyWordOfTheSignalChange(t *testing.T) {
	w, err := SignalChangeWord(probeInputs().Changes)
	if err != nil || w != "updated" {
		t.Fatalf("близнец: %q %v", w, err)
	}
	two := append(probeInputs().Changes, Change{Word: "moved", Change: "UPDATED"})
	if _, err := SignalChangeWord(two); !errors.Is(err, ErrInputs) {
		t.Fatalf("два слова рода UPDATED приняты: %v", err)
	}
	if _, err := SignalChangeWord(probeInputs().Changes[:2]); !errors.Is(err, ErrInputs) {
		t.Fatalf("словарь без рода UPDATED принят: %v", err)
	}
}

// Файл без маркера — ErrNotFunction (не находка); маркер с испорченным
// заголовком — ErrUnreadable; отпечаток не выпущен — ErrVersion.
func TestParseOutcomes(t *testing.T) {
	if _, err := Parse([]byte("-- +goose Up\nCREATE TABLE x (id text);\n-- +goose Down\nDROP TABLE x;\n")); !errors.Is(err, ErrNotFunction) {
		t.Fatalf("файл без функции: %v", err)
	}
	out, err := Render(File{Up: Body{Version: Current(), Inputs: probeInputs()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(strings.Replace(out, "-- ttl_seconds:", "-- ttl:", 1))); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("испорченный заголовок: %v", err)
	}
	if _, err := Parse([]byte(strings.Replace(out, Current(), "v9:sha256:"+strings.Repeat("0", 64), 1))); !errors.Is(err, ErrVersion) {
		t.Fatalf("невыпущенная версия: %v", err)
	}
}

// Постоянные, которые тело повторяет за другими пакетами, равны им: алфавит
// id — буквам каталога ids (в обе стороны), ключ имени — ключу подписки.
func TestBodyConstantsAgreeWithTheirSources(t *testing.T) {
	if namePayloadKey != subscription.NamePayloadKey {
		t.Fatalf("ключ имени %q, у подписки %q", namePayloadKey, subscription.NamePayloadKey)
	}
	if len(crockford) != 32 {
		t.Fatalf("алфавит %d букв", len(crockford))
	}
	for c := byte(0x21); c < 0x7f; c++ {
		id := ids.PrefixNotificationHyphen + "-" + strings.Repeat(string(c), 17)
		if in := strings.IndexByte(crockford, c) >= 0; in != ids.IsValidHyphen(id, ids.PrefixNotificationHyphen) {
			t.Errorf("буква %q: в алфавите тела %t, у ids %t", c, in, !in)
		}
	}
}
