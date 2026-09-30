// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package catalogderive

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PRO-Robotech/corelib/authz"
)

// Bind привязывает записи формы ScopeBound (З14) к экземплярам, к которым
// процесс привязал свои серверы: `bound` — тип объекта → идентификатор
// экземпляра. Возвращает НОВУЮ карту; выведенная не меняется.
//
// # Почему привязка — отдельный шаг, а не параметр вывода
//
// Вывод читает аннотации, то есть свойство БИНАРЯ: один и тот же на каждом
// старте. Идентификатор экземпляра — свойство ПРОЦЕССА: его знает корень,
// поднявший сервер, и приносит его сам сервер (лента — имя своего модуля).
// Смешай их — и карта, которую сверяет гейт «каталог == аннотации», зависела бы
// от того, кто её собрал.
//
// # Отказы — все разом, с именами
//
//   - метод формы, для типа которого привязки нет: иначе его извлекатель пуст,
//     и каждый вызов отвергался бы голосом прав — отказом, не называющим ни
//     метода, ни пропуска;
//   - привязка, которую не читает ни один метод карты: сервер привязан, а его
//     формы в карте нет — проводка без предмета, второе место об одном
//     предмете, из которых верно одно;
//   - идентификатор, которым нельзя назвать объект модели (пустой, с
//     разделителем): отказ при старте, а не на каждом вызове.
//
// Две привязки одного типа здесь невыразимы — это ключ карты; их отвергает
// конструктор дескриптора (`servicecontract.New`).
func Bind(m authz.RPCMap, bound map[string]string) (authz.RPCMap, error) {
	var problems []string

	for typ, id := range bound {
		if _, err := authz.FormatObject(typ, id); err != nil {
			problems = append(problems, fmt.Sprintf("binding %s → %q names no object: %v", typ, id, err))
		}
	}

	out := make(authz.RPCMap, len(m))
	read := map[string]bool{}
	for key, e := range m {
		if e.BoundType != "" {
			id, ok := bound[e.BoundType]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s is checked against the %s instance the "+
					"server is bound to, and this process declares no binding of that type", key, e.BoundType))
			} else {
				read[e.BoundType] = true
				e.Extract = boundExtractor(e.BoundType, id)
			}
		}
		out[key] = e
	}
	for typ := range bound {
		if !read[typ] {
			problems = append(problems, fmt.Sprintf("binding of type %s is read by no method of the "+
				"derived map: the server is bound, and no annotation carries bound_to_server on that type", typ))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("catalogderive: server bindings do not match the ScopeBound methods: %s",
			strings.Join(problems, "; "))
	}
	return out, nil
}

// boundExtractor — объект проверки есть экземпляр, к которому привязан сервер.
// Запрос не читается вовсе: назвать другой экземпляр вызывающему нечем.
func boundExtractor(objectType, id string) authz.ObjectExtractor {
	return func(any) (string, string, error) { return objectType, id, nil }
}
