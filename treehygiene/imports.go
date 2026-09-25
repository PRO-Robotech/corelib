// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// imports.go — разбор ЗАКЛЮЧЕНИЯ набора импортов в каталог: библиотека,
// которую приносит внесённое поддерево, импортируется только внутри него.
//
// # Предмет
//
// Движок OAuth2 внесён в фундамент поддеревом вместе со своим стеком JOSE. Этот
// стек обязан остаться ВНУТРИ поддерева: подписывает наши токены один подписант
// на своей библиотеке, и второй стек рядом с ним — второй путь подписи, о
// котором никто не договаривался. Приёмка F1 (`F1-51`) требует держателя этого
// правила в каждом дереве — в фундаменте, где поддерево лежит, и в каждом
// потребителе, где его нет вовсе. Разбор один на все деревья, а правило —
// у каждого своё: у фундамента набор законен под корнем поддерева, у
// потребителя — нигде.
//
// # Почему разбор объявлений, а не образец и не граф сборки
//
// Образец находит путь и в комментарии, и в строке, и в тексте находки гейта.
// Граф сборки (`go list`) видит только файлы ТЕКУЩЕГО контекста сборки: файл
// под ограничением `//go:build integration` из него выпадает, и импорт в нём
// невидим — ровно в том месте, где его проще всего не заметить. Разбор
// объявлений импорта (`parser.ImportsOnly`) ограничений сборки не исполняет и
// читает каждый отслеживаемый файл `.go`, включая пробы.

package treehygiene

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PRO-Robotech/corelib/treecorpus"
)

// ImportConfinement — правило заключения набора путей импорта.
type ImportConfinement struct {
	// Name — имя набора; печатается в находке.
	Name string

	// Set — пути модулей набора. Путь импорта принадлежит набору, если он
	// РАВЕН элементу либо продолжает его через «/» (пакет модуля, мажорная
	// версия: `github.com/go-jose/go-jose/v3`) или через «.v» (форма
	// `gopkg.in`: `gopkg.in/square/go-jose.v2`). Иное продолжение — не набор:
	// `github.com/go-jose/go-josex` чужой модуль, и назвать его находкой
	// значило бы судить имя, а не импорт.
	//
	// Сравнение — БЕЗ УЧЁТА РЕГИСТРА. Хостинг кода не различает регистра
	// организации и репозитория, и прокси модулей отдаёт тот же модуль под
	// `github.com/Square/go-jose` и `gopkg.in/Go-Jose/go-jose.v2` (опрошено
	// 2026-09-23): такой импорт собирается, и правило, сравнивающее с учётом
	// регистра, его бы не видело. Чужой модуль в другом регистре остаётся
	// чужим — продолжение судится тем же правилом.
	Set []string

	// Homes — каталоги от корня обхода, через «/», под которыми импорт набора
	// законен. Пусто — законен НИГДЕ: так судит дерево, в котором поддерева
	// нет. Каталог-дом, которого нет в индексе, — отказ обхода, а не пустое
	// место: правило говорило бы о каталоге, которого нет, и «ноль импортёров
	// дома» было бы неотличимо от «дом переехал».
	Homes []string

	// ForbiddenAtHome — пути модулей, которые под Homes НЕ импортируются;
	// принадлежность — по тому же правилу, что у Set. Это обратная сторона
	// заключения: поддерево не берёт нашего стека, и стеки не смешиваются.
	ForbiddenAtHome []string
}

// ImportFinding — один импорт вне правила, с координатой.
type ImportFinding struct {
	// Position — файл:строка:колонка объявления импорта, от корня обхода.
	Position string
	// Path — путь импорта, как он записан.
	Path string
	// Why — какая половина правила нарушена.
	Why string
}

func (f ImportFinding) String() string {
	return fmt.Sprintf("%s: импорт %q — %s", f.Position, f.Path, f.Why)
}

// ImportCensus — объём осмотренного. Печатается всегда: «ноль находок» обязано
// быть отличимо от «ноль прочитанного», а «набор внутри дома есть» — от «обход
// дома не увидел».
type ImportCensus struct {
	// Tracked — файлов в индексе дерева.
	Tracked int
	// GoFiles — прочитано отслеживаемых файлов `.go` (включая пробы и файлы
	// под ограничениями сборки).
	GoFiles int
	// Imports — осмотрено объявлений импорта.
	Imports int
	// HomeImporters — файлов под Homes, импортирующих набор. Ноль при
	// непустых Homes — предмет правила пропал: либо поддерево потеряло стек,
	// либо обход слеп. Вердикт по нему выносит вызывающий.
	HomeImporters int
}

// String — перепись одной строкой.
func (c ImportCensus) String() string {
	return fmt.Sprintf("перепись: в индексе %d, прочитано файлов .go %d, объявлений импорта %d, "+
		"импортёров набора внутри дома %d", c.Tracked, c.GoFiles, c.Imports, c.HomeImporters)
}

// InImportSet отвечает, принадлежит ли путь импорта набору (правило — у поля
// ImportConfinement.Set, в том числе почему без учёта регистра).
// Экспортирована ради пробы границы правила: форма, о которой разбор не знает,
// — не находка и не чистота, а невидимость.
func InImportSet(path string, set []string) bool {
	path = strings.ToLower(path)
	for _, root := range set {
		root = strings.ToLower(root)
		if path == root || strings.HasPrefix(path, root+"/") || strings.HasPrefix(path, root+".v") {
			return true
		}
	}
	return false
}

// AuditImportConfinement обходит отслеживаемые файлы `.go` дерева и судит
// каждое объявление импорта по правилу.
//
// Состав берётся из индекса git (treecorpus), а не с диска: игнорируемый каталог
// с чужой копией дерева не предмет. Файл, который не разбирается, — отказ
// обхода с именем файла: без его объявлений перепись завысила бы объём
// осмотренного, а находка в нём осталась бы невидимой.
func AuditImportConfinement(root string, rule ImportConfinement) (
	findings []ImportFinding, census ImportCensus, err error) {
	if len(rule.Set) == 0 {
		return nil, census, errors.New("treehygiene: набор правила заключения пуст — судить нечего")
	}
	homes := make([]string, 0, len(rule.Homes))
	for _, h := range rule.Homes {
		h = strings.Trim(filepath.ToSlash(h), "/")
		if h == "" || h == "." {
			return nil, census, fmt.Errorf("treehygiene: дом %q правила заключения — весь корень; "+
				"такое правило ничего не заключает", rule.Homes)
		}
		homes = append(homes, h)
	}

	tree, err := treecorpus.NewTree(root)
	if err != nil {
		return nil, census, err
	}
	census.Tracked = tree.Count()
	for _, h := range homes {
		if !tree.HasDir(h) {
			return nil, census, fmt.Errorf("treehygiene: дома %s правила заключения в индексе нет — "+
				"правило говорит о каталоге, которого нет", h)
		}
	}

	fset := token.NewFileSet()
	for _, rel := range tree.SortedFiles() {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(tree.Root(), filepath.FromSlash(rel)), nil, parser.ImportsOnly)
		if perr != nil {
			return nil, census, fmt.Errorf("treehygiene: %s не разбирается — его импорты не осмотрены: %w", rel, perr)
		}
		census.GoFiles++

		atHome := underAny(rel, homes)
		homeImporter := false
		for _, spec := range file.Imports {
			path, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				return nil, census, fmt.Errorf("treehygiene: %s: путь импорта %s не разбирается: %w", rel, spec.Path.Value, uerr)
			}
			census.Imports++
			pos := fset.Position(spec.Pos())
			where := fmt.Sprintf("%s:%d:%d", rel, pos.Line, pos.Column)

			switch {
			case InImportSet(path, rule.Set) && !atHome:
				findings = append(findings, ImportFinding{Position: where, Path: path,
					Why: rule.Name + " импортируется вне " + homesPhrase(homes)})
			case InImportSet(path, rule.Set):
				homeImporter = true
			case atHome && InImportSet(path, rule.ForbiddenAtHome):
				findings = append(findings, ImportFinding{Position: where, Path: path,
					Why: "внутри " + homesPhrase(homes) + " импортируется запрещённый там модуль: стеки не смешиваются"})
			}
		}
		if homeImporter {
			census.HomeImporters++
		}
	}
	if census.GoFiles == 0 {
		return nil, census, errors.New("treehygiene: в индексе нет ни одного файла .go — " +
			"«ноль находок» на «ноль прочитанного» не вердикт")
	}
	// Порядок находок — порядок обхода (файлы отсортированы, импорты — в
	// порядке объявления): детерминизм входа — часть контракта проверки.
	return findings, census, nil
}

func underAny(rel string, homes []string) bool {
	for _, h := range homes {
		if strings.HasPrefix(rel, h+"/") {
			return true
		}
	}
	return false
}

func homesPhrase(homes []string) string {
	if len(homes) == 0 {
		return "дерева (дом не объявлен: набор здесь не законен нигде)"
	}
	return "дома " + strings.Join(homes, ", ")
}
