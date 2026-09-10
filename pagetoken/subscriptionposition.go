// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package pagetoken

import (
	"strconv"
	"strings"
)

// SubscriptionPosition — позиция подписки: граница УСТОЯВШЕГОСЯ в журнале
// владельца.
//
// # Почему это ОТДЕЛЬНЫЙ тип, а не [Cursor]
//
// `Cursor` объявлен как «граница страницы в keyset-порядке: последняя ОТДАННАЯ
// строка». Для подписки это дословно запрещённая форма: номер выдаётся на
// вставке, видимость наступает на фиксации, поэтому писатель, закоммитивший
// позже с меньшим номером, оказывается ЗА границей по отданному — навсегда,
// молча и без пропуска в нумерации, видимого клиенту.
//
// Взять `Cursor` было бы унификацией по самой узкой семантике: два предмета
// совпадают формой («строка-токен, кодирующая место в порядке») и расходятся
// смыслом границы. Совпадение формы не есть общность предмета.
//
// # Что означает Settled
//
// Каждый номер `≤ Settled` в журнале владельца либо УЖЕ ВИДИМ, либо не появится
// НИКОГДА (писатель откатился). Это и есть свойство, ради которого позиция
// непрозрачна: возобновление с неё не пропускает ни одной строки, закоммиченной
// после её выдачи. Производит эту величину сервер подписки
// (`pkg/subscription`); кодек её только переносит.
//
// Скаляр здесь законен — незаконен его ПРОИЗВОДИТЕЛЬ «максимум видимого».
// Разрядность величины ни при чём.
type SubscriptionPosition struct {
	// Settled — граница устоявшегося. Ноль — законная величина: «журнал ещё
	// ничего не устоял».
	Settled int64
}

// subscriptionPositionVersion — метка формы тела.
//
// Она стоит здесь ровно затем, чтобы внутреннюю форму позиции можно было
// починить, не ломая контракт: токен прежней формы будет отвергнут ЯВНО, а не
// разобран во что-то похожее. Ради этого же токен другой формы того же пакета
// (`Cursor`) сюда не проходит — тела различаются меткой, а не только видом.
const subscriptionPositionVersion = "sub1"

// subscriptionPositionSep — разделитель тела. Тот же символ, что у [Cursor], и
// это не совпадение: обе формы читает один пакет, и держать два разделителя
// значило бы завести различие, которое ничего не различает.
const subscriptionPositionSep = "|"

// subscriptionPositionCodec — кодировка тела. Стандартный алфавит с дополнением,
// как у [Canonical]: позиция ездит в теле сообщения, не в URL.
var subscriptionPositionCodec = Canonical.enc

// EncodeSubscriptionPosition собирает непрозрачный токен позиции.
//
// Пустой строки конструктор НЕ выпускает НИКОГДА: пустое значение поля позиции
// означает «позиция не задана», и производитель, способный выпустить пустое,
// сделал бы эти два состояния неразличимыми у вызывающего.
func EncodeSubscriptionPosition(p SubscriptionPosition) string {
	body := subscriptionPositionVersion + subscriptionPositionSep +
		strconv.FormatInt(p.Settled, 10)
	return subscriptionPositionCodec.EncodeToString([]byte(body))
}

// DecodeSubscriptionPosition разбирает токен позиции.
//
// Пустой токен — «позиция не задана», и он возвращает `(nil, true)`: отсутствие
// представимо ОТДЕЛЬНО от всякого значения, поэтому вызывающий не обязан
// различать «позиции не было» и «позиция разобралась в ноль». Ноль здесь —
// законная величина («ничего ещё не устоялось»), и слить его с отсутствием
// значило бы отдать журнал с начала тому, кто просил хвост.
//
// Второй ответ — годность, а не ошибка: у вызывающих разный контракт отказа, и
// навязывать им общий тип значило бы заставить каждого переводить его обратно.
func DecodeSubscriptionPosition(token string) (*SubscriptionPosition, bool) {
	if token == "" {
		return nil, true
	}
	raw, err := subscriptionPositionCodec.DecodeString(token)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(string(raw), subscriptionPositionSep)
	if len(parts) != 2 || parts[0] != subscriptionPositionVersion {
		return nil, false
	}
	settled, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || settled < 0 {
		return nil, false
	}
	return &SubscriptionPosition{Settled: settled}, true
}

// SubscriptionPositionWellFormed отвечает, разбирается ли токен. Пустой — да
// («позиция не задана»).
//
// Отдельная функция нужна проверке формата на границе приложения: ей позиция не
// нужна, нужен вердикт, — а повторять разбор своим кодом есть ровно то, чего
// пакет и избегает.
func SubscriptionPositionWellFormed(token string) bool {
	_, ok := DecodeSubscriptionPosition(token)
	return ok
}
