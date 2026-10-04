// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package resourceevent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

// goose — разделы миграции.
const (
	gooseUp       = "-- +goose Up\n"
	gooseDown     = "-- +goose Down\n"
	statementTail = "-- +goose StatementEnd\n"
)

// headerKeys — ключи строк заголовка определения по порядку. Порядок
// фиксирован шаблоном; иное — тело не читается.
var headerKeys = []string{"schema_rev", "ttl_seconds", "service", "module", "signal", "journal", "columns", "signal_change"}

var (
	columnsRe   = regexp.MustCompile(`^kind=(\S+) id=(\S+) change=(\S+) payload=(\S+) project=(\S+) initiator=(\S+) occurred_at=(\S+)$`)
	kindRowRe   = regexp.MustCompile(`(?m)^      \('([^']*)', '([^']*)', '([^']*)'\),?$`)
	changeRowRe = regexp.MustCompile(`(?m)^      \('([^']*)', '([^']*)'\),?$`)
)

// Parsed — то, что извлечено из файла: определения и строка сигнала
// каждого из них, как она записана в теле.
type Parsed struct {
	File File
	// Signal — объект строки сигнала, записанный в заголовке Up.
	Signal string
	// PrevSignal — то же у прежнего определения (пусто без него).
	PrevSignal string
}

// Parse извлекает из миграции функции версии шаблона и входы определений.
// Файл без определения — ErrNotFunction; определение, из которого входы не
// извлекаются, — ErrUnreadable; версия не выпущена — ErrVersion. Parse не
// судит, что файл — вывод шаблона: это делает Recognize.
func Parse(content []byte) (Parsed, error) {
	s := string(content)
	if !strings.Contains(s, bodyMarker) {
		return Parsed{}, ErrNotFunction
	}
	upAt := strings.Index(s, gooseUp)
	downAt := strings.Index(s, gooseDown)
	if upAt < 0 || downAt < upAt {
		return Parsed{}, fmt.Errorf("%w: нет разделов goose Up и Down по порядку", ErrUnreadable)
	}
	up, down := s[upAt:downAt], s[downAt:]
	if strings.Count(up, bodyMarker) != 1 || strings.Count(down, bodyMarker) > 1 {
		return Parsed{}, fmt.Errorf("%w: определений в Up %d, в Down %d — ожидалось одно и не больше одного",
			ErrUnreadable, strings.Count(up, bodyMarker), strings.Count(down, bodyMarker))
	}
	var p Parsed
	body, sig, err := parseBody(up)
	if err != nil {
		return Parsed{}, err
	}
	p.File.Up, p.Signal = body, sig
	if strings.Contains(down, bodyMarker) {
		prev, psig, err := parseBody(down)
		if err != nil {
			return Parsed{}, fmt.Errorf("прежнее определение: %w", err)
		}
		p.File.Prev, p.PrevSignal = &prev, psig
	}
	return p, nil
}

// parseBody — определение, начинающееся маркером внутри section.
func parseBody(section string) (Body, string, error) {
	at := strings.Index(section, bodyMarker)
	rest := section[at+len(bodyMarker):]
	end := strings.Index(rest, statementTail)
	if end < 0 {
		return Body{}, "", fmt.Errorf("%w: определение без конца оператора", ErrUnreadable)
	}
	text := rest[:end]
	lines := strings.SplitN(text, "\n", len(headerKeys)+2)
	if len(lines) < len(headerKeys)+2 {
		return Body{}, "", fmt.Errorf("%w: заголовок определения короче %d строк", ErrUnreadable, len(headerKeys)+1)
	}
	b := Body{Version: lines[0]}
	if _, ok := released[b.Version]; !ok {
		return Body{}, "", fmt.Errorf("%w: %s", ErrVersion, b.Version)
	}
	h := map[string]string{}
	for i, key := range headerKeys {
		v, ok := strings.CutPrefix(lines[i+1], "-- "+key+": ")
		if !ok {
			return Body{}, "", fmt.Errorf("%w: строка %d заголовка — не %s", ErrUnreadable, i+2, key)
		}
		h[key] = v
	}
	in := &b.Inputs
	var err error
	if in.SchemaRev, err = strconv.Atoi(h["schema_rev"]); err != nil {
		return Body{}, "", fmt.Errorf("%w: schema_rev %q", ErrUnreadable, h["schema_rev"])
	}
	ttl, err := strconv.ParseInt(h["ttl_seconds"], 10, 64)
	if err != nil || ttl <= 0 || ttl > int64(spec.TTLMax/time.Second) {
		return Body{}, "", fmt.Errorf("%w: ttl_seconds %q", ErrUnreadable, h["ttl_seconds"])
	}
	in.TTL = time.Duration(ttl) * time.Second
	in.Service, in.Module, in.Table, in.SignalChange = h["service"], h["module"], h["journal"], h["signal_change"]
	m := columnsRe.FindStringSubmatch(h["columns"])
	if m == nil {
		return Body{}, "", fmt.Errorf("%w: строка columns вне формы", ErrUnreadable)
	}
	in.Columns = Columns{Kind: m[1], ID: m[2], Change: m[3], Payload: m[4], Project: m[5], Initiator: m[6], OccurredAt: m[7]}
	for _, r := range kindRowRe.FindAllStringSubmatch(text, -1) {
		in.Kinds = append(in.Kinds, Kind{Name: r[1], NameForm: NameForm(r[2]), Scope: Scope(r[3])})
	}
	for _, r := range changeRowRe.FindAllStringSubmatch(text, -1) {
		in.Changes = append(in.Changes, Change{Word: r[1], Change: r[2]})
	}
	if err := in.Validate(); err != nil {
		return Body{}, "", fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	return b, h["signal"], nil
}

// Recognize — файл есть вывод выпущенной версии шаблона на извлечённых из
// него входах: Parse, затем Render, затем побайтовое равенство. Признанный
// файл отвечает своим разбором.
func Recognize(content []byte) (Parsed, bool) {
	p, err := Parse(content)
	if err != nil {
		return Parsed{}, false
	}
	want, err := Render(p.File)
	if err != nil || want != string(content) {
		return Parsed{}, false
	}
	return p, true
}
