// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listnarrow

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrModelNotWired — сужателя нет либо ему не с кем говорить. Это состояние ПОСАДКИ,
// а не ответ модели, — поэтому отказ, а не «да».
//
// Отдавать страницу здесь не на каком основании: сужаемые методы помечены
// scope-filtered, то есть пообъектной проверки на крае за них НЕ задаётся вовсе, и
// откатываться не на что. Пока это состояние означало сквозной проход, вся защита
// держалась на загрузочном страже — то есть существовала ровно до первой
// конфигурации, которая его не взвела.
func ErrModelNotWired() error {
	return status.Error(codes.PermissionDenied,
		"list filter: the rights model is not configured for this deployment")
}

// IDs — вход для списков, сужающих ГОЛЫЕ идентификаторы страницы.
//
// Порядок ветвлений здесь и есть предмет: он одинаков у всех вызывающих, потому что
// живёт в одном месте.
//
//  1. ЛИЧНОСТЬ — безусловно и ПЕРВОЙ. Ответ безымянному не зависит от того, что
//     оператор прописал в конфигурации: положение вызывающего в обоих случаях одно и
//     то же — его никто не назвал. Пока отсечка стояла за веткой посадки, посадка без
//     модели отдавала всю страницу кому угодно;
//  2. ПОСАДКА — модели нет ⇒ отказ; аварийный режим ⇒ проход, но посчитанный и
//     названный;
//  3. пустая страница ⇒ ничего, без обращения к соседу;
//  4. сужение.
func IDs(ctx context.Context, n *Narrower, resourceType, action string, ids []string) ([]string, error) {
	subject, err := gate(ctx, n)
	if err != nil {
		return nil, err
	}
	if n.cfg.Breakglass {
		return n.breakglassPass(ids, subject, resourceType, action), nil
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return n.visibleIDs(ctx, subject, resourceType, action, ids)
}

// Page — вход для списков, сужающих ЗАПИСИ страницы: оставляет только видимые строки,
// сохраняя порядок курсора.
//
// Дженерик, потому что списочные пути отличаются ТОЛЬКО типом записи и извлечением
// идентификатора; альтернатива — тот же цикл, скопированный по разу на ресурс.
//
// Второй вход в ТОТ ЖЕ контракт: он зовёт IDs, а не повторяет её ветвления, — иначе
// дыра просто переезжала бы в тот из двух входов, который забыли.
func Page[T any](
	ctx context.Context,
	n *Narrower,
	resourceType, action string,
	page []T,
	idOf func(T) string,
) ([]T, error) {
	ids := make([]string, 0, len(page))
	for _, rec := range page {
		ids = append(ids, idOf(rec))
	}
	visibleIDs, err := IDs(ctx, n, resourceType, action, ids)
	if err != nil {
		return nil, err
	}
	if len(visibleIDs) == 0 {
		return nil, nil
	}
	visible := make(map[string]struct{}, len(visibleIDs))
	for _, id := range visibleIDs {
		visible[id] = struct{}{}
	}
	out := make([]T, 0, len(visibleIDs))
	for _, rec := range page {
		if _, ok := visible[idOf(rec)]; ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// breakglassPass — аварийный режим: страница уходит несуженной, потому что модели на
// этой посадке нет вовсе.
//
// Считается ОТДЕЛЬНО от записи: по журналу видно, что проход БЫЛ, и не видно, что его
// НЕ БЫЛО. Прочитанный ноль (Counts) эти два состояния различает — без него аварийный
// режим становится тихим штатным, и «им пользуются» неотличимо от «им не пользуются».
//
// Запись называет СУБЪЕКТА и ТИП: иначе по ней нельзя установить, кто и к чему
// получил доступ в обход модели, а это единственное, ради чего запись и делается.
func (n *Narrower) breakglassPass(ids []string, subject, resourceType, action string) []string {
	total := n.breakglass.Add(1)
	n.logger.Warn("list filter BREAKGLASS: the rights model is not wired on this deployment and the "+
		"emergency bypass is armed — the page is returned UNFILTERED",
		"subject", subject, "resource_type", resourceType, "action", action,
		"page_size", len(ids), "breakglass_passes_total", total)
	return ids
}
