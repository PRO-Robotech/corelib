// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package validate

import "strings"

// FieldNameEq отвечает, называют ли две строки ОДНО поле контракта.
//
// Форму имени в `update_mask` выбирает не сервис. Край разбирает тело запроса
// через protojson, и тот приводит `updateMask` к именам полей контракта:
// клиент прислал `countryCode`, сервис получил `country_code`. Прямой вызов
// gRPC несёт то, что положил вызывающий. Сравнение строк ДОСЛОВНО поэтому
// отвечает «нет» на верном входе — и делает это тихо: поле объявлено
// изменяемым, запрос принят, ответ успешен, значение не изменилось.
//
// Класс не виден, пока все изменяемые поля односложны: у `status` и `labels`
// обе формы совпадают. Первое многословное поле вскрывает его на пути ЗАПИСИ,
// а не проверки — то есть на два шага дальше того места, где о форме имени
// вообще думают.
//
// Обе стороны приводятся к форме имени поля контракта; точка разделяет
// вложенные поля и в приведении не участвует.
func FieldNameEq(a, b string) bool {
	return a == b || CanonFieldName(a) == CanonFieldName(b)
}

// CanonFieldName приводит имя поля к форме контракта (`hostClasses` →
// `host_classes`), посегментно относительно точки.
func CanonFieldName(name string) string {
	segs := strings.Split(name, ".")
	for i, seg := range segs {
		var b strings.Builder
		b.Grow(len(seg) + 4)
		for _, r := range seg {
			if r >= 'A' && r <= 'Z' {
				b.WriteByte('_')
				b.WriteRune(r + ('a' - 'A'))
				continue
			}
			b.WriteRune(r)
		}
		segs[i] = b.String()
	}
	return strings.Join(segs, ".")
}
