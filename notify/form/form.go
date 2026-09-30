// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package form — форма значений атрибутов почтового извещения: одна функция на
// обеих сторонах ленты. У источника её зовёт feed.Put до первого оператора SQL,
// в notify — прогон каждой строки перед рендером (Р7 приёмки NTF-1, З2
// замысла issue-2915).
//
// Что пакет держит:
//
//   - закрытый перечень видов атрибута (Kind) и исчерпывающий разбор вида без
//     ветки «принять без проверки»: вид вне перечня — ErrUnknownType. Какие виды
//     допускает формат шаблона, объявляет notify/spec (spec.AttrKinds); проба
//     сверяет form с этим перечнем в одну сторону;
//   - одну функцию «нуль ли это» — Presence: нормализовать → решить → проверить;
//   - одно написание отметки времени в ленте — FormatTimestamp и ParseTimestamp;
//   - непрозрачные типы Path, Token, HeaderText и Value: признак «задано» —
//     отдельное поле, единственный выход значения — Value(), прочие выходы
//     (fmt, slog, json, text) нейтральны либо отвечают ErrNotSerializable.
//
// Пакет не импортирует notify/spec, html/template, text/template, net/smtp и
// mime/multipart: рантайм источника его тянет, а формат шаблонов и рендер —
// нет (NTF1-B28, D05; проба TestFormDependsOnNoFormatRenderOrMail).
//
// Ошибки пакета значения не несут: вызывающий называет атрибут, а значение
// атрибута может быть секретом.
package form

import "errors"

// Kind — вид атрибута. Перечень закрыт; значения вне констант ниже Presence
// отвергает ErrUnknownType. Разбор вида — исчерпывающий switch без default,
// полноту держит линтер exhaustive (.github/golangci.yml).
type Kind string

// Виды атрибута (Р7). Какие из них принимает формат шаблона — spec.AttrKinds.
const (
	KindText      Kind = "text"
	KindSecret    Kind = "secret"
	KindPath      Kind = "path"
	KindToken     Kind = "token"
	KindTimestamp Kind = "timestamp"
)

// Сторожа пакета — различимы через errors.Is. Текст ошибки называет правило и
// никогда — значение.
var (
	// ErrUnset — нулевое значение непрозрачного типа: его построили не функции
	// пакета (var p form.Path, form.Path{}).
	ErrUnset = errors.New("form: значение не задано (нулевое значение типа)")
	// ErrEmpty — пустое значение там, где значение обязательно.
	ErrEmpty = errors.New("form: пустое значение")
	// ErrUnknownType — вид вне закрытого перечня: значение отвергается, ветки
	// «принять без проверки» нет.
	ErrUnknownType = errors.New("form: вид атрибута вне закрытого перечня")
	// ErrMalformed — значение вне формы своего вида.
	ErrMalformed = errors.New("form: значение вне формы")
	// ErrNotSerializable — сериализация мимо Value(): у непрозрачных типов
	// единственный выход значения — Value().
	ErrNotSerializable = errors.New("form: значение выдаётся только через Value()")
)
