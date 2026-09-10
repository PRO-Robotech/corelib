// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"

	coreerrors "github.com/PRO-Robotech/corelib/errors"
)

// ListForCaller — единственная точка, через которую tenant-facing список
// операций попадает в сервисы: ключ владения выводится из ctx, предикат владения
// уходит внутрь SQL WHERE (ListOwned), несуженный Repo.List не вызывается
// никогда.
//
// Зачем отдельная функция, а не «зовите ListOwned сами». Правильный вызов
// складывается из трёх шагов, и пропуск любого возвращает исходную дыру:
// получить ownership-апгрейд репозитория, вывести ключ владения именно из ctx
// (а не из PrincipalFromContext — тот на пустом контексте отдаёт системную
// личность, которая владеет каждой системно записанной строкой), и на отсутствие
// ключа отказать, а не продолжить. Собранные в одном месте, эти шаги
// проверяются одним гейтом; рассыпанные по пятнадцати вызывающим — пятнадцатью
// прочтениями.
//
// Исходы ровно три:
//
//   - ключ владения есть → страница своих операций (полная проекция: строка
//     принадлежит вызывающему, поэтому и содержимое, и личность на ней — его
//     собственные);
//   - ключа нет (контекст без принципала, принципал снят недоверенным
//     форвардером, именованная анонимность) → ПУСТАЯ страница. Не несуженная
//     выдача и не «своих нет, значит покажем чужие»;
//   - репозиторий не несёт ownership-апгрейда (ошибка провязки) → отказ с
//     фиксированным текстом. Откат на несуженный путь запрещён: молчаливый
//     откат — это ровно тот исход, ради недопущения которого функция и написана.
//
// Формат страницы проверяется ДО схлопывания в пустую: иначе мусорный курсор у
// неопознанного вызывающего вернул бы пустую страницу вместо отказа по формату
// (api-conventions: format-validate → authz → repo).
//
// Несуженный Repo.List остаётся законным для доверенного внутреннего яруса
// (кластерный аудит, реконсайлер) — там вызывающий уже авторизован иначе, и
// именно поэтому запрет выражен гейтом с поимённым списком, а не удалением
// метода.
func ListForCaller(ctx context.Context, repo Repo, filter ListFilter) ([]Operation, string, error) {
	owned, ok := AsOwned(repo)
	if !ok {
		return nil, "", coreerrors.Internal("list operations failed").Err()
	}
	if err := ValidateListPagination(filter); err != nil {
		return nil, "", err
	}
	owner, hasOwner := OwnerFromContext(ctx)
	if !hasOwner {
		return nil, "", nil
	}
	return owned.ListOwned(ctx, filter, owner)
}

// ValidateListPagination проверяет page_size/page_token страницы операций по
// общей дисциплине List-RPC (api-conventions): page_size вне [0..Max] и мусорный
// курсор → InvalidArgument, а не «тихо поправим» и не INTERNAL.
//
// Вынесена отдельно, потому что проверка обязана происходить ДО любого
// short-circuit'а выдачи (пустой ключ владения, пустой грант): иначе ошибка
// формата подменяется пустой страницей и клиент видит 200 там, где контракт
// обещает 400.
func ValidateListPagination(filter ListFilter) error {
	if _, err := validatePageSize(filter.PageSize); err != nil {
		return err
	}
	if filter.PageToken == "" {
		return nil
	}
	if _, _, err := decodePageToken(filter.PageToken); err != nil {
		return invalidPageTokenErr()
	}
	return nil
}
