// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listnarrow

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
)

// ErrUnnamedCaller — запрос не назвал никого. Отдельный, ПЕРВЫЙ исход: это не «отказ
// прав» и не «сосед недоступен», а отсутствие вызывающего.
//
// Код `Unauthenticated`: ответ обязан говорить о ЛИЧНОСТИ, а не о том, что оператор
// прописал в конфигурации, — иначе один и тот же запрос получал бы разный ответ в
// зависимости от посадки.
func ErrUnnamedCaller() error {
	return status.Error(codes.Unauthenticated, "list filter: subject required")
}

// SubjectFromContext — субъект модели прав («user:usr_x» / «service_account:sva_x»
// / «service:<имя>») вызывающего. Субъект не подставляется и не выводится: он либо
// назван, либо запроса нет.
//
// Кто вызывающий, решает ОДНА функция — [authz.CallerSubject], та же, что у
// извлекателя звена прав (`sec-one-predicate-three-readers`): пересланный
// доверенным принципал решает; иначе служба, опознанная звеном идентичности
// служб по сертификату на методе перечня; иначе никого.
//
// Случаи «никого», и все обязаны отвергаться одинаково:
//
//   - контекст не нёс ни принципала, ни опознанной службы. Брать здесь
//     безусловный извлекатель нельзя: он отдаёт запасное значение уровня
//     начальной загрузки, которому на кластере разрешено всё, — то есть
//     безымянный запрос спрашивал бы права от имени этой учётки;
//   - пересланный принципал несёт зарезервированное слово анонимности либо тип
//     `service` (служебный субъект из заголовков не производится);
//   - тип пересланного принципала не называет тенантного субъекта (служебный,
//     неизвестный), идентификатор пуст либо содержит разделители модели прав:
//     `usr_a#member` стал бы ссылкой на набор, `usr_a:usr_b` сдвинул бы границу
//     «тип:идентификатор».
//
// Тенантскую пару судит [authz.TenantSubject] — тот же кодек, которым субъекта
// называет всякий, кто по его имени что-то находит. Своей проверки здесь нет
// намеренно: она была бы вторым словарём об одном предмете.
func SubjectFromContext(ctx context.Context) (string, error) {
	c, ok := authz.CallerSubject(ctx)
	if !ok {
		return "", ErrUnnamedCaller()
	}
	if _, isService := c.Service(); isService {
		return c.Subject(), nil
	}
	p, _ := c.Forwarded()
	subject, named := authz.TenantSubject(p.Type, p.ID)
	if !named {
		return "", ErrUnnamedCaller()
	}
	return subject, nil
}
