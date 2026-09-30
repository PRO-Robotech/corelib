// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// record_aging_test.go — старение записи в подставке (agedCopy, instantsOf,
// instantsNotShifted) знает каждую форму, в которой запись несёт мгновение.
//
// Пробы истечения старят запись вместо паузы на часах (memoryPorts.
// ageCodeRecord), и предпосылка у них одна: состаренная запись — прошедшее
// время целиком. Мгновение, которого сдвиг не коснулся, делает запись другой, а
// не старой, и проба судила бы не то. Поэтому здесь сдвиг проверен на
// синтетическом значении, несущем мгновение в каждой законной форме; отказ — на
// каждой форме, которой сдвинуть нельзя; сверка — инъекцией: несдвинутое
// мгновение она называет путём, полностью сдвинутое значение — молчит.
package oauthceremony_test

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// agingSample несёт мгновение в каждой форме, которую обходит agedValue, и
// рядом — то, что сдвигаться не должно: нулевое мгновение, пустой указатель,
// строку и неэкспортированное поле без мгновения.
type agingSample struct {
	At     time.Time
	Zero   time.Time
	Ptr    *time.Time
	NilPtr *time.Time
	Loose  any
	List   []time.Time
	Fixed  [2]time.Time
	ByKind map[string]time.Time
	Nested struct{ At time.Time }
	Label  string
	hidden int
}

var agingBase = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

// agingAt — мгновение через minutes минут после agingBase: у каждой формы
// образца своё, чтобы перепутанное место не сошлось с ожидаемым.
func agingAt(minutes int) time.Time { return agingBase.Add(time.Duration(minutes) * time.Minute) }

func newAgingSample() agingSample {
	ptr := agingAt(1)
	sample := agingSample{
		At:     agingAt(0),
		Ptr:    &ptr,
		Loose:  agingAt(2),
		List:   []time.Time{agingAt(3), agingAt(4)},
		Fixed:  [2]time.Time{agingAt(5), agingAt(6)},
		ByKind: map[string]time.Time{"a": agingAt(7), "b": agingAt(8)},
		Label:  "label",
		hidden: 7,
	}
	sample.Nested.At = agingAt(9)
	return sample
}

const agingShift = -time.Hour

// TestAgedCopyShiftsEveryInstantOfTheValue — каждое ненулевое мгновение
// образца в копии сдвинуто ровно на agingShift, прочее — как было, и сам
// образец не тронут. Ожидание выписано по полям образца, а не снято обходом:
// обход судил бы сдвиг им же самим.
func TestAgedCopyShiftsEveryInstantOfTheValue(t *testing.T) {
	sample := newAgingSample()
	aged, err := agedCopy(sample, agingShift)
	if err != nil {
		t.Fatalf("образец не состарен: %v", err)
	}

	loose, isInstant := aged.Loose.(time.Time)
	if !isInstant {
		t.Fatalf("мгновение под `any` стало %T", aged.Loose)
	}
	if aged.Ptr == nil || len(aged.List) != 2 || len(aged.ByKind) != 2 {
		t.Fatalf("копия потеряла состав: указатель %v, срез %v, карта %v", aged.Ptr, aged.List, aged.ByKind)
	}
	for _, c := range []struct {
		path string
		got  time.Time
		was  int
	}{
		{"At", aged.At, 0}, {"Ptr", *aged.Ptr, 1}, {"Loose", loose, 2},
		{"List[0]", aged.List[0], 3}, {"List[1]", aged.List[1], 4},
		{"Fixed[0]", aged.Fixed[0], 5}, {"Fixed[1]", aged.Fixed[1], 6},
		{"ByKind[a]", aged.ByKind["a"], 7}, {"ByKind[b]", aged.ByKind["b"], 8},
		{"Nested.At", aged.Nested.At, 9},
	} {
		if want := agingAt(c.was).Add(agingShift); !c.got.Equal(want) {
			t.Errorf("%s: %s, ожидалось %s", c.path, c.got, want)
		}
	}

	if !aged.Zero.IsZero() {
		t.Errorf("нулевое мгновение стало %s: состаренное отсутствие стало значением", aged.Zero)
	}
	if aged.NilPtr != nil {
		t.Errorf("пустой указатель стал %v", aged.NilPtr)
	}
	if aged.Label != sample.Label || aged.hidden != sample.hidden {
		t.Errorf("не-мгновения изменились: строка %q → %q, скрытое поле %d → %d",
			sample.Label, aged.Label, sample.hidden, aged.hidden)
	}

	if !reflect.DeepEqual(sample, newAgingSample()) {
		t.Errorf("сдвиг тронул сам образец, а не его копию: %+v", sample)
	}
	if aged.Ptr == sample.Ptr {
		t.Error("копия делит указатель с образцом")
	}
}

// TestInstantsNotShiftedNamesEveryInstantLeftBehind — сверка сдвига. Законный
// близнец — полностью состаренная копия: сверка молчит. Каждая инъекция
// возвращает одному месту копии прежнее мгновение (или кладёт мгновение туда,
// где его не было), и сверка называет ровно это место. Образец без мгновений —
// «сверять нечего», это отказ, а не «сдвинуто всё».
func TestInstantsNotShiftedNamesEveryInstantLeftBehind(t *testing.T) {
	sample := newAgingSample()
	aged, err := agedCopy(sample, agingShift)
	if err != nil {
		t.Fatalf("образец не состарен: %v", err)
	}
	if off, err := instantsNotShifted(sample, aged, agingShift); err != nil || len(off) != 0 {
		t.Fatalf("законный близнец: сверка назвала %v (%v) у полностью состаренной копии", off, err)
	}

	for _, tc := range []struct {
		path  string
		spoil func(*agingSample)
	}{
		{"agingSample.At", func(a *agingSample) { a.At = sample.At }},
		{"agingSample.Ptr", func(a *agingSample) { was := *sample.Ptr; a.Ptr = &was }},
		{"agingSample.Loose", func(a *agingSample) { a.Loose = sample.Loose }},
		{"agingSample.List[1]", func(a *agingSample) { a.List = slices.Clone(a.List); a.List[1] = sample.List[1] }},
		{"agingSample.Fixed[0]", func(a *agingSample) { a.Fixed[0] = sample.Fixed[0] }},
		{"agingSample.ByKind[b]", func(a *agingSample) { a.ByKind = maps.Clone(a.ByKind); a.ByKind["b"] = sample.ByKind["b"] }},
		{"agingSample.Nested.At", func(a *agingSample) { a.Nested.At = sample.Nested.At }},
		{"agingSample.Zero", func(a *agingSample) { a.Zero = agingBase }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			spoiled := aged
			tc.spoil(&spoiled)
			off, err := instantsNotShifted(sample, spoiled, agingShift)
			if err != nil {
				t.Fatalf("сверка отказала: %v", err)
			}
			if !slices.Equal(off, []string{tc.path}) {
				t.Errorf("сверка назвала %v, ожидалось [%s]", off, tc.path)
			}
		})
	}

	if _, err := instantsNotShifted(agingSample{}, agingSample{}, agingShift); err == nil {
		t.Error("образец без мгновений прошёл сверку: «сверять нечего» прочитано как «сдвинуто всё»")
	}
}

// hiddenInstant и instantKeyed — формы, которым сдвинуть мгновение нельзя:
// неэкспортированное поле не записать, а сдвиг ключа менял бы саму карту.
type hiddenInstant struct{ at time.Time }

type instantKeyed struct{ ByTime map[time.Time]string }

// TestAgedCopyRefusesAFormItCannotShift — на форме, которой сдвиг не
// поддаётся, и сдвиг, и снятие мгновений отказывают и называют место: пропуск
// сделал бы состаренную запись другой, а не старой, молча.
func TestAgedCopyRefusesAFormItCannotShift(t *testing.T) {
	for _, tc := range []struct {
		name  string
		age   func() error
		scan  func() error
		place string
	}{
		{
			name: "неэкспортированное поле с мгновением",
			age: func() error {
				_, err := agedCopy(hiddenInstant{at: agingBase}, agingShift)
				return err
			},
			scan: func() error {
				return instantsOf(reflect.ValueOf(hiddenInstant{at: agingBase}), "hiddenInstant", map[string]time.Time{})
			},
			place: "hiddenInstant.at",
		},
		{
			name: "ключ карты — мгновение",
			age: func() error {
				_, err := agedCopy(instantKeyed{ByTime: map[time.Time]string{agingBase: "x"}}, agingShift)
				return err
			},
			scan: func() error {
				return instantsOf(reflect.ValueOf(instantKeyed{ByTime: map[time.Time]string{agingBase: "x"}}),
					"instantKeyed", map[string]time.Time{})
			},
			place: "instantKeyed.ByTime",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for verb, run := range map[string]func() error{"сдвиг": tc.age, "снятие": tc.scan} {
				err := run()
				switch {
				case err == nil:
					t.Errorf("%s прошёл форму, которой сдвинуть нельзя", verb)
				case !strings.Contains(err.Error(), tc.place):
					t.Errorf("%s отказал, не назвав место %s: %v", verb, tc.place, err)
				}
			}
		})
	}
}
