// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_exceptions.go — реестр исключений гейта писателей таблиц ленты
// (З16, CX1-43 (а), УК48). Реестр живёт в самом гейте: запись — имя,
// область (модуль дерева), довод и предикат снятия. Запись без довода или без
// предиката снятия — отказ гейта. Предикат снятия — функция, чей ноль
// означает, что исключению нечего исключать: гейт исполняет его на дереве
// области и краснеет на пустом исключении с именем записи. Запись чужой
// области к дереву не применяется и самоистечением не краснеет; гейт печатает
// число применимых записей (CX1-74 (а)).
//
// На пинах NTF-1 записей 0. Единственная предусмотренная запись — функция
// resource-event формы fanout, одна на журнал модуля (Д33): её вносит в реестр
// та же полоса NTF-3, которая даёт генератору эту форму, эталон тела и
// выпущенные версии шаблона (X2-F); признание функции — сверка с выводом
// эталона генератора, копии шаблона в гейте нет.
package treehygiene

import (
	"errors"
	"fmt"
)

// feedWriteException — запись реестра исключений.
type feedWriteException struct {
	// Name — имя записи; печатается в находке.
	Name string
	// Scope — путь модуля дерева, к которому запись применяется.
	Scope string
	// Reason — довод исключения.
	Reason string
	// Removal — предикат снятия: число предметов исключения в дереве
	// области. Ноль — исключать нечего.
	Removal func(*goTree) (int, error)
}

// feedWriteExceptions — реестр. На пинах NTF-1 пуст.
func feedWriteExceptions() []feedWriteException { return nil }

func (r *FeedWritesReport) applyExceptions(g *goTree, entries []feedWriteException, add func(pos, kind, why string)) error {
	var bad []error
	for i, e := range entries {
		switch {
		case e.Name == "":
			bad = append(bad, fmt.Errorf("запись %d реестра без имени", i))
		case e.Reason == "":
			bad = append(bad, fmt.Errorf("запись %s реестра без довода", e.Name))
		case e.Removal == nil:
			bad = append(bad, fmt.Errorf("запись %s реестра без предиката снятия", e.Name))
		case e.Scope == "":
			bad = append(bad, fmt.Errorf("запись %s реестра без области", e.Name))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("treehygiene: реестр исключений не по форме: %w", errors.Join(bad...))
	}
	for _, e := range entries {
		if e.Scope != g.module {
			continue
		}
		r.Exceptions++
		n, err := e.Removal(g)
		if err != nil {
			return fmt.Errorf("treehygiene: предикат снятия записи %s не исполнился: %w", e.Name, err)
		}
		if n == 0 {
			add("", writeExceptionEmpty, "запись реестра "+e.Name+" без предмета: исключать в дереве нечего — снять запись")
			continue
		}
		r.Recognized += n
	}
	return nil
}
