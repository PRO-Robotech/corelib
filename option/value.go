// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package option — величина, которой может НЕ БЫТЬ, причём отдельно от того,
// какое значение она принимает, когда есть.
//
// # Зачем это, если есть нулевое значение
//
// Затем, что нулевое значение бывает ЗАКОННЫМ. Имя ресурса — пример из
// собственных конвенций: пустая строка есть законный вход создания
// (`api-conventions.md` §«Имя ресурса»), поэтому «имя не прислали» и «прислали
// пустое» — разные события с разными исходами, и различить их нулевым значением
// нельзя by construction. То же у необязательной ссылки: «не задавали» против
// «сняли».
//
// Указатель решал бы ту же задачу и приносил бы вторую — разыменование nil, —
// а также делал бы структуру нестабильной по значению: две одинаковые записи
// перестали бы быть равными.
//
// # Откуда взялось
//
// До 2026-09-04 тип брался из стороннего модуля, не несшего лицензии НИ ОДНИМ
// файлом. Отсутствие лицензии означает «все права защищены», и публичный
// репозиторий с таким пином распространял чужой код без разрешения. Модуль снят
// целиком; поверхность здесь — та, которую дерево ЧИТАЛО, а не вся, что была.
// Держится гейтом `internal/repohygiene` `TestEveryPinnedModuleCarriesALicense`.
package option

import (
	"encoding/json"
	"fmt"
)

// ValueOf — величина типа T вместе с признаком того, задана ли она.
//
// Нулевое значение — «не задано», поэтому `var v ValueOf[T]` годно к
// употреблению сразу и означает именно отсутствие.
//
// Поля неэкспортируемые НАМЕРЕННО: признак заданности и значение обязаны
// меняться вместе. Дай к ним доступ порознь — и появится состояние «задано, а
// значения нет», ради исключения которого тип и заведён.
type ValueOf[T any] struct {
	value T
	some  bool
}

// MustNewOption — заданная величина. Пустое значение T здесь означает «задано
// пустым», а не «не задано»: в этом всё различие.
func MustNewOption[T any](v T) ValueOf[T] {
	return ValueOf[T]{value: v, some: true}
}

// IsNone — величина не задана.
func (val *ValueOf[T]) IsNone() bool { return !val.some }

// Set — задать величину.
func (val *ValueOf[T]) Set(v T) { val.value, val.some = v, true }

// Unset — снять величину. Значение сбрасывается в нулевое, чтобы снятое не
// удерживало ссылок и не всплывало через отражение.
func (val *ValueOf[T]) Unset() {
	var zero T
	val.value, val.some = zero, false
}

// Maybe — величина и признак её заданности. Единственный способ прочитать
// значение: он вынуждает вызывающего решить, что делать с отсутствием.
func (val *ValueOf[T]) Maybe() (T, bool) { return val.value, val.some }

// SomeOr — величина либо названное умолчание.
func (val *ValueOf[T]) SomeOr(defVal T) T {
	if val.some {
		return val.value
	}
	return defVal
}

// IsEq — равенство по признаку заданности И по значению. Сравнение значений
// задаёт вызывающий: T произвольный, и оператор равенства к нему неприменим.
//
// Два незаданных равны; заданное незаданному — никогда, даже если значение
// нулевое. Иначе различие, ради которого тип существует, терялось бы в
// сравнении.
func (val *ValueOf[T]) IsEq(other ValueOf[T], eq func(a, b T) bool) bool {
	if val.some != other.some {
		return false
	}
	if !val.some {
		return true
	}
	return eq(val.value, other.value)
}

// String — fmt.Stringer. Отсутствие называется словом, а не пустотой: пустая
// строка есть законное ЗНАЧЕНИЕ, и печатать её на месте отсутствия значило бы
// повторить в выводе ровно ту путаницу, которую тип устраняет.
func (val ValueOf[T]) String() string {
	if !val.some {
		return "none"
	}
	return fmt.Sprintf("%v", val.value)
}

// MarshalJSON — отсутствие кодируется как null.
func (val ValueOf[T]) MarshalJSON() ([]byte, error) {
	if !val.some {
		return []byte("null"), nil
	}
	return json.Marshal(val.value)
}

// UnmarshalJSON — null читается как отсутствие; всё прочее разбирается в T и
// становится заданным. Неразбираемое НЕ задаёт величину: полуразобранное
// значение хуже отсутствующего.
func (val *ValueOf[T]) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		val.Unset()
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	val.Set(v)
	return nil
}

// Validate — проверка ДЕЛЕГИРУЕТСЯ внутреннему типу и только когда величина
// задана: проверять отсутствие нечем и незачем.
//
// Внутренний тип, не умеющий проверять себя, годен by construction — иначе
// каждый необязательный примитив пришлось бы оборачивать.
func (val ValueOf[T]) Validate() error {
	if !val.some {
		return nil
	}
	if v, ok := any(val.value).(interface{ Validate() error }); ok {
		return v.Validate()
	}
	return nil
}
