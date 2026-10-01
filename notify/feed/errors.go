// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import "errors"

// Сторожа Put — ПЯТЬ различимых значений (З7, CX1-67). Ни один не оборачивает
// другой; детали отказа (имя шаблона, атрибута) несёт обёртка %w над одним
// сторожем. Текст сторожа значения атрибута и адреса не несёт.
//
// После любого сторожа транзакция вызывающего пригодна к коммиту: всё, что
// может отвергнуть вызов, стоит до первого оператора SQL либо откатывается к
// точке сохранения Put. Пригодность — не разрешение: ErrSecondLimitedPut —
// дефект программы вызывающего, и его мутация не коммитится (Е9).
var (
	// ErrDeliveryNotConfigured — флаг источника выключен, письмо класса
	// security. Для notice при выключенном флаге Put возвращает nil без строки.
	ErrDeliveryNotConfigured = errors.New("email delivery is not configured in this installation")
	// ErrAttrsInvalid — описание или атрибуты не по форме: ревизия < 1,
	// атрибут вне описания, нуль объявленного атрибута, значение вне формы,
	// лимиты описания не возрастают строго.
	ErrAttrsInvalid = errors.New("feed: notification attributes are invalid")
	// ErrRecipientInvalid — адрес не разбирается address.Normalize либо ключ
	// окна построен из нулевого address.Normalized.
	ErrRecipientInvalid = errors.New("feed: recipient address is invalid")
	// ErrLimitExhausted — окно лимита исчерпано: ноль строк условного
	// оператора окна, а не ошибка SQL. Вклад постановки откачен к точке
	// сохранения.
	ErrLimitExhausted = errors.New("feed: notification limit is exhausted")
	// ErrSecondLimitedPut — вторая постановка с лимитами в одной транзакции
	// вызывающего (УК84, УК85). Дефект программы вызывающего, а не состояние
	// мира; Put сам увеличивает счётчик дефектов.
	ErrSecondLimitedPut = errors.New("feed: second limited put in one transaction")
)

// ErrSourceUnbound — Put позван на контексте без источника (Source.Bind): это
// отказ проводки корня, а не сторож постановки. Ни с одним сторожем errors.Is
// его не путает.
var ErrSourceUnbound = errors.New("feed: no notification source bound to the context")

// PutGuards — перечень пяти сторожей Put; классификатор отказов вызывающего
// строится по строке на каждый, без корзины «прочее».
func PutGuards() []error {
	return []error{ErrDeliveryNotConfigured, ErrAttrsInvalid, ErrRecipientInvalid, ErrLimitExhausted, ErrSecondLimitedPut}
}
