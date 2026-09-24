// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// wirestatus_test.go — проводной код отказа и состояние HTTP не спорят друг с
// другом.
//
// Вызывающий пишет ответ из двух чисел случая — WireCode и HTTPStatus. Пара, в
// которой состояние противоречит коду (`server_error` с 409), отдаёт клиенту
// два разных утверждения об одном отказе: код говорит «сбой сервера», состояние
// — «конфликт запроса». Класс судится по всей таблице случаев, а не по
// найденной строке.
package oauthceremony_test

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// wireRow — строка таблицы случаев, как её видит вызывающий.
type wireRow struct {
	name   string
	wire   string
	status int
}

// statusNamedByDefinition — проводные коды, определение которых НАЗЫВАЕТ
// состояние HTTP. Перечень закрыт и не шире определений: код, для которого
// определение состояния не называет (отказы перенаправления, коды движка вне
// RFC), судится только единственностью состояния.
var statusNamedByDefinition = map[string]struct {
	status int
	source string
}{
	"server_error":            {500, "RFC 6749 §4.1.2.1"},
	"temporarily_unavailable": {503, "RFC 6749 §4.1.2.1"},
	"invalid_request":         {400, "RFC 6749 §5.2, RFC 6750 §3.1"},
	"invalid_grant":           {400, "RFC 6749 §5.2"},
	"unauthorized_client":     {400, "RFC 6749 §5.2"},
	"unsupported_grant_type":  {400, "RFC 6749 §5.2"},
	"invalid_scope":           {400, "RFC 6749 §5.2"},
	"invalid_token":           {401, "RFC 6750 §3.1"},
}

// wireStatusDisagreements — находки двух видов: проводной код, несущий больше
// одного состояния, и строка, чьё состояние расходится с названным
// определением кода.
func wireStatusDisagreements(rows []wireRow) []string {
	byWire := map[string]map[int][]string{}
	for _, r := range rows {
		if byWire[r.wire] == nil {
			byWire[r.wire] = map[int][]string{}
		}
		byWire[r.wire][r.status] = append(byWire[r.wire][r.status], r.name)
	}
	var findings []string
	for wire, statuses := range byWire {
		if len(statuses) < 2 {
			continue
		}
		var parts []string
		for status, names := range statuses {
			parts = append(parts, fmt.Sprintf("%d (%s)", status, strings.Join(names, ", ")))
		}
		sort.Strings(parts)
		findings = append(findings, fmt.Sprintf("проводной код %q несёт %d состояния: %s",
			wire, len(statuses), strings.Join(parts, "; ")))
	}
	for _, r := range rows {
		named, ok := statusNamedByDefinition[r.wire]
		if ok && r.status != named.status {
			findings = append(findings, fmt.Sprintf("%s: %q с состоянием %d, а %s называет %d",
				r.name, r.wire, r.status, named.source, named.status))
		}
	}
	sort.Strings(findings)
	return findings
}

// liveWireRows — таблица случаев пакета: каждый объявленный случай и
// CodeUnspecified, у которого строка тоже есть.
func liveWireRows() []wireRow {
	codes := append([]oauthceremony.FailureCode{oauthceremony.CodeUnspecified}, declaredCodes()...)
	rows := make([]wireRow, 0, len(codes))
	for _, c := range codes {
		rows = append(rows, wireRow{name: c.String(), wire: c.WireCode(), status: c.HTTPStatus()})
	}
	return rows
}

// TestWireCodeCarriesOneStatusAndTheOneItsDefinitionNames — у каждого проводного
// кода одно состояние, и там, где его называет определение кода, — названное.
func TestWireCodeCarriesOneStatusAndTheOneItsDefinitionNames(t *testing.T) {
	rows := liveWireRows()
	if len(rows) < 2 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: строк таблицы %d — судить нечего", len(rows))
	}
	wires := map[string]bool{}
	named := 0
	for _, r := range rows {
		wires[r.wire] = true
		if _, ok := statusNamedByDefinition[r.wire]; ok {
			named++
		}
	}
	if named == 0 {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: ни одна строка не несёт кода с названным состоянием — " +
			"вторая половина пробы ничего не утверждает")
	}
	for _, f := range wireStatusDisagreements(rows) {
		t.Error(f)
	}
	t.Logf("строк таблицы: %d; проводных кодов: %d; строк с кодом, чьё состояние названо определением: %d",
		len(rows), len(wires), named)
}

// TestWireStatusDisagreementIsFoundAndItsTwinIsSilent — инъекция в обе стороны.
// Каждый дефект отличается от близнеца ровно состоянием одной строки.
func TestWireStatusDisagreementIsFoundAndItsTwinIsSilent(t *testing.T) {
	cases := []struct {
		name   string
		defect []wireRow
		twin   []wireRow
		want   []string
	}{
		{
			name:   "один код — два состояния",
			defect: []wireRow{{"a", "server_error", 500}, {"b", "server_error", 409}},
			twin:   []wireRow{{"a", "server_error", 500}, {"b", "server_error", 500}},
			want:   []string{`"server_error" несёт 2 состояния`, "b: \"server_error\" с состоянием 409"},
		},
		{
			name:   "код вне перечня — два состояния",
			defect: []wireRow{{"a", "token_claim", 401}, {"b", "token_claim", 403}},
			twin:   []wireRow{{"a", "token_claim", 401}, {"b", "token_claim", 401}},
			want:   []string{`"token_claim" несёт 2 состояния`},
		},
		{
			name:   "единственная строка против определения",
			defect: []wireRow{{"a", "invalid_token", 400}},
			twin:   []wireRow{{"a", "invalid_token", 401}},
			want:   []string{"a: \"invalid_token\" с состоянием 400, а RFC 6750 §3.1 называет 401"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := wireStatusDisagreements(tc.defect)
			if len(found) != len(tc.want) {
				t.Fatalf("находок %d, ожидалось %d: %v", len(found), len(tc.want), found)
			}
			for _, w := range tc.want {
				if !slices.ContainsFunc(found, func(f string) bool { return strings.Contains(f, w) }) {
					t.Errorf("находки не называют %q: %v", w, found)
				}
			}
			if silent := wireStatusDisagreements(tc.twin); len(silent) != 0 {
				t.Errorf("законный близнец объявлен находкой: %v", silent)
			}
		})
	}
}
