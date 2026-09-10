// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quota

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/quota/quotaread"
)

// Чтение строк учёта — ОДИН оператор на всех владельцев.
//
// Таблица у всех одна и та же с точностью до имени схемы, поэтому и оператор
// один. Написанный пять раз порознь, он разошёлся бы на составе столбцов или на
// порядке — то есть там, где расхождение не видно глазом и не ломает сборку.

// Querier — то единственное, что нужно чтению от носителя: один глагол.
// Транзакция, соединение из пула — подходит любое.
//
// Отдельный интерфейс, а не расширение `Execer`: у `Execer` есть свои
// реализации, и добавление третьего метода потребовало бы от них глагола,
// которым они не пользуются, — то есть заставило бы дописать код ради формы.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ListStates отдаёт строки учёта носителя из названной схемы.
//
// `ORDER BY kind` — по КОЛОНКЕ ПОРЯДКА, а не по времени вставки. Строки заводит
// материализация одной транзакцией, поэтому метка времени у них совпадает, и
// сортировка по ней разрешалась бы идентификатором, то есть случайной строкой:
// ответ переставлял бы виды от прогона к прогону, а клиент, ведущий состояние по
// индексу, читал бы перестановку как изменение (`api-conventions.md` §«Порядок
// повторяющегося поля — часть контракта либо его нет»).
//
// Пустой срез означает «строк учёта ещё нет» и НИЧЕГО не говорит о пределах:
// различать это состояние обязана полоса чтения (`quotaread.Band`). Здесь
// сообщается только то, что видно в таблице.
func ListStates(
	ctx context.Context, q Querier, schema, carrierType, carrierID string,
) ([]quotaread.State, error) {
	if q == nil {
		return nil, fmt.Errorf("quota states: querier is required")
	}
	// Имя схемы попадает в оператор подстановкой, поэтому проверяется здесь же,
	// а не у вызывающего: у вызывающего это была бы пятая копия, а пропустивший
	// её владелец собрал бы оператор с чужим содержимым.
	if !validSchemaName(schema) {
		return nil, fmt.Errorf("quota states: schema %q is not a plain identifier", schema)
	}

	stmt := fmt.Sprintf(`
		SELECT kind, limit_value, used, source_scope, source_scope_id
		  FROM %s.project_resource_quotas
		 WHERE carrier_type = $1 AND carrier_id = $2
		 ORDER BY kind`, schema)

	rows, err := q.Query(ctx, stmt, carrierType, carrierID)
	if err != nil {
		return nil, fmt.Errorf("list quota states: %w", err)
	}
	defer rows.Close()

	out := make([]quotaread.State, 0, 8)
	for rows.Next() {
		st := quotaread.State{CarrierType: carrierType, CarrierID: carrierID}
		if err := rows.Scan(&st.Kind, &st.Limit, &st.Used, &st.SourceScope, &st.SourceScopeID); err != nil {
			return nil, fmt.Errorf("scan quota state: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list quota states: %w", err)
	}
	return out, nil
}
