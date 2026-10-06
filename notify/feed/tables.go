// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import "github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"

// Tables — таблицы ленты одной службы, узнаваемые по имени, которое отдаёт
// сервер. Им владелец, судящий отказ своей схемы по имени таблицы (перепись
// проверок, перевод отказа в код), отличает таблицу ленты, не собирая её имени
// сам: суффиксы таблиц ленты производит только corelib (УК89). Имени таблицы
// Tables не отдаёт — текстом оператора её не сделать.
//
// Нулевое значение не узнаёт ни одной таблицы.
type Tables struct {
	svc string
}

// TablesOf — таблицы ленты службы svc: того же префикса, что служба отдаёт
// NewSource и notifygen init -service. Префикс судится сразу: негодный —
// отказ, а не набор, молча не узнающий ничего.
func TablesOf(svc string) (Tables, error) {
	if err := tablename.Valid(svc); err != nil {
		return Tables{}, err
	}
	return Tables{svc: svc}, nil
}

// Has — является ли table таблицей ленты службы. table — имя так, как его
// отдаёт сервер: поле TableName отказа (pgconn.PgError) или
// information_schema.tables.table_name — без схемы и без кавычек.
func (t Tables) Has(table string) bool {
	_, ok := tablename.KindOf(t.svc, table)
	return ok
}
