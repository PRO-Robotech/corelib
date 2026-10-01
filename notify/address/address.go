// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package address — нормализация адреса почтового извещения: одна функция на
// обеих сторонах ленты. Её зовут feed.Put у источника (ключ окна лимита) и
// notify (ключ сетки на адресата) — Р8 приёмки NTF-1, З3 замысла issue-2915.
//
// Что пакет держит:
//
//   - Normalize: локальная часть без изменений, домен — NormalizeDomain;
//   - NormalizeDomain: ASCII-форма IDNA по одному именованному профилю
//     (переменная profile). Смена профиля перекладывает ключи окон и сетки —
//     её замечает замороженный корпус corpus_test.go (NTF1-B27 (IDNA));
//   - непрозрачные типы Normalized и Domain: непустое значение вне пакета
//     строят только функции пакета, единственный выход значения — Value(),
//     прочие выходы (fmt, slog, json, text) нейтральны либо отвечают
//     ErrNotSerializable (CX1-42).
//
// Пакет не импортирует прочие notify/* (З1). Ошибки пакета значения адреса не
// несут: адрес — персональные данные.
package address

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Сторожа пакета — различимы через errors.Is.
var (
	// ErrMalformed — адрес или домен, который пакет не разбирает: нет «@»,
	// пустая локальная часть, управляющий символ, не UTF-8, домен вне профиля.
	ErrMalformed = errors.New("address: адрес не разбирается")
	// ErrUnset — нулевое значение непрозрачного типа: его построили не функции
	// пакета (address.Normalized{}).
	ErrUnset = errors.New("address: значение не задано (нулевое значение типа)")
	// ErrNotSerializable — сериализация мимо Value(): единственный выход
	// значения — Value().
	ErrNotSerializable = errors.New("address: значение выдаётся только через Value()")
)

// profile — ЕДИНСТВЕННЫЙ профиль IDNA ключа адреса: непереходная обработка
// UTS 46 с проверками поиска. Регистр сводится отображением профиля,
// Unicode-домен и его A-label дают один ключ, «ß» в «ss» не отображается.
// Смена значения — ломающая правка ключа окон и сетки (З3, CX1-30).
var profile = idna.New(
	idna.MapForLookup(),
	idna.Transitional(false),
	idna.BidiRule(),
	idna.ValidateLabels(true),
	idna.StrictDomainName(true),
)

// Normalized — адрес, нормализованный Normalize: ключ окна лимита у источника
// и ключ сетки в notify принимают только его. Нулевое значение — «не задано»,
// Value() на нём отвечает ErrUnset.
type Normalized struct{ v string }

// Domain — доменная часть, нормализованная NormalizeDomain. Приёмников вне
// пакета нет (решение Д19): вне пакета законны вызов-производитель с ошибкой
// под именем, сравнение значений и Value().
type Domain struct{ v string }

// Value — единственный выход значения. Нулевое значение — ErrUnset.
func (n Normalized) Value() (string, error) { return valueOf(n.v) }

// Value — единственный выход значения. Нулевое значение — ErrUnset.
func (d Domain) Value() (string, error) { return valueOf(d.v) }

func valueOf(v string) (string, error) {
	if v == "" {
		return "", ErrUnset
	}
	return v, nil
}

// Normalize разбирает адрес: локальная часть — без изменений, домен —
// NormalizeDomain. Разделитель — последний «@». Ошибка — ErrMalformed с
// названием правила, без значения.
func Normalize(s string) (Normalized, error) {
	return normalizeWith(profile, s)
}

// NormalizeDomain приводит домен к ASCII-форме IDNA профилем пакета.
func NormalizeDomain(s string) (Domain, error) {
	v, err := domainWith(profile, s)
	if err != nil {
		return Domain{}, err
	}
	return Domain{v: v}, nil
}

func normalizeWith(p *idna.Profile, s string) (Normalized, error) {
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return Normalized{}, fmt.Errorf("%w: нет «@»", ErrMalformed)
	}
	local, domain := s[:at], s[at+1:]
	if local == "" {
		return Normalized{}, fmt.Errorf("%w: пустая локальная часть", ErrMalformed)
	}
	if err := checkText(local, "локальная часть"); err != nil {
		return Normalized{}, err
	}
	d, err := domainWith(p, domain)
	if err != nil {
		return Normalized{}, err
	}
	return Normalized{v: local + "@" + d}, nil
}

func domainWith(p *idna.Profile, s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("%w: пустой домен", ErrMalformed)
	}
	if err := checkText(s, "домен"); err != nil {
		return "", err
	}
	a, err := p.ToASCII(s)
	if err != nil {
		// Текст ошибки idna несёт метку домена — наружу он не уходит.
		return "", fmt.Errorf("%w: домен вне профиля IDNA", ErrMalformed)
	}
	if a == "" {
		return "", fmt.Errorf("%w: пустой домен", ErrMalformed)
	}
	return a, nil
}

// checkText отвергает не UTF-8 и управляющие символы (C0, DEL, C1): CR и LF
// в адресе — путь к подстановке заголовка.
func checkText(s, part string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: %s — не UTF-8", ErrMalformed, part)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s несёт управляющий символ", ErrMalformed, part)
		}
	}
	return nil
}
