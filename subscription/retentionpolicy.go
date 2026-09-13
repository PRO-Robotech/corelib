// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription

// Retention — что владелец обещает про удержание журнала. Ноль объявлением не
// является: «удерживаю всё» — свойство владельца, а не умолчание формы.
type Retention uint8

const (
	// RetentionUnset — владелец не сказал ничего.
	RetentionUnset Retention = iota

	// RetainsEverything — журнал не чистится; отказ «позиция утрачена» не
	// наступает никогда, и служебное сообщение говорит это прямо.
	RetainsEverything

	// RetainsFromEarliestRow — журнал чистится; нижняя возобновимая позиция
	// выводится из самой ранней удержанной строки.
	RetainsFromEarliestRow
)
