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
// Запись одна — функция resource-event формы fanout, одна на журнал модуля
// (Д33, NTF-3 З10): её вносит в реестр та же полоса NTF-3 (X2-F), которая
// даёт генератору эту форму и выпущенные версии шаблона тела. Признание
// функции — сверка с выводом шаблона (resourceevent.Recognize: извлечь входы
// из тела, вывести шаблон на них, сравнить побайтно); копии шаблона в гейте
// нет. Область — дерево kacho: на corelib и kaname запись не применяется и
// самоистечением не краснеет.
package treehygiene

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/PRO-Robotech/corelib/notify/feed/resourceevent"
)

// kachoModulePath — модуль дерева kacho: область записи resource-event.
const kachoModulePath = "github.com/PRO-Robotech/kacho"

// resourceEventEntry — имя записи функции resource-event; печатается в
// находке самоистечения.
const resourceEventEntry = "resource-event: функция базы формы fanout, одна на журнал модуля"

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
	// Recognizes — признаёт ли запись файл SQL своим предметом: операторы
	// признанного файла находками SQL не считаются. nil — запись файлов не
	// признаёт.
	Recognizes func(content []byte) bool
}

// feedWriteExceptions — реестр.
func feedWriteExceptions() []feedWriteException {
	return []feedWriteException{{
		Name:  resourceEventEntry,
		Scope: kachoModulePath,
		Reason: "Д33, NTF-3 З10: строку ленты формы fanout на каждую строку журнала модуля ставит функция базы " +
			"на таблице журнала; её тело — вывод выпущенной версии шаблона notifygen, а не ручной писатель ленты",
		Removal:    resourceEventFunctions,
		Recognizes: recognizesResourceEvent,
	}}
}

func recognizesResourceEvent(content []byte) bool {
	_, ok := resourceevent.Recognize(content)
	return ok
}

// resourceEventFunctions — предикат снятия записи resource-event: число
// функций, признанных выводом шаблона, в отслеживаемых *.sql дерева.
// Функция — пара (каталог миграций, журнал): CREATE OR REPLACE той же
// функции новой миграцией второй функцией не считается.
func resourceEventFunctions(g *goTree) (int, error) {
	seen := map[string]bool{}
	for _, rel := range g.tree.SortedFiles() {
		if !strings.HasSuffix(rel, ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(g.root, filepath.FromSlash(rel)))
		if err != nil {
			return 0, fmt.Errorf("%s не читается: %w", rel, err)
		}
		if p, ok := resourceevent.Recognize(data); ok {
			seen[path.Dir(rel)+" "+p.File.Up.Inputs.Table] = true
		}
	}
	return len(seen), nil
}

// recognizers — признающие файлы записи, применимые к дереву module.
func recognizers(module string, entries []feedWriteException) []func([]byte) bool {
	var out []func([]byte) bool
	for _, e := range entries {
		if e.Scope == module && e.Recognizes != nil {
			out = append(out, e.Recognizes)
		}
	}
	return out
}

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
