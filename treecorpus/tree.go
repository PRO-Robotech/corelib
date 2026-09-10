// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// tree.go — состав дерева, разложенный для ОБХОДА: файлы плюс каталоги-предки.
//
// # Чем это отличается от Under
//
// `Under` отвечает на вопрос «какие файлы лежат под этим каталогом» и годится,
// когда проверка их просто читает. Обходчику этого мало: он идёт по каталогам и
// обязан уметь отсечь целое поддерево (`filepath.SkipDir`), а не фильтровать
// файлы поштучно — игнорируемая рабочая копия дерева весит сотни мегабайт, и
// читать её ради последующего отбрасывания незачем.
//
// # Почему это ЗДЕСЬ, а не в тестовом файле пакета гейтов
//
// Раскладка жила в `_test.go` пакета гейтов и потому была доступна ровно одному
// пакету. Полтора десятка гейтов на неё опирались, и вынести любой из них в
// соседний пакет было нельзя, не унеся с собой её же копию: помощник в тестовом
// файле — это дом на одну семью. Пока он там стоял, расщепление пакета гейтов
// упиралось не в предмет обхода, а в место объявления помощника.
//
// Здесь у него дом: обычный пакет, который импортирует кто угодно. Тестовая
// обёртка (fatal вместо error) остаётся у каждого пакета своя и стоит двадцать
// строк — цена, за которую пакеты не связываются между собой ради помощника.
package treecorpus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Tree — состав дерева: множество файлов и множество каталогов, в которых есть
// хоть один файл этого состава. Пути — от корня, слэш-разделённые.
type Tree struct {
	root  string
	files map[string]bool
	dirs  map[string]bool
}

// NewTree читает ИНДЕКС git и раскладывает его в два множества.
//
// Недоступность git — ОТКАЗ, а не пропуск. Молчаливый откат «нет git — иду по
// диску» вернул бы ровно тот дефект, ради которого написан этот пакет, и сделал
// бы это невидимо: на машине без git проверка продолжала бы «работать», читая
// игнорируемые каталоги.
func NewTree(root string) (*Tree, error) {
	// То же, что у Under: кешированный вердикт проверки дерева недействителен.
	// Разбор и замеры — cachedverdict.go.
	if msg := CachedVerdictRefusal(); msg != "" {
		return nil, errors.New("treecorpus: " + msg)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("treecorpus: абсолютный путь для %s: %w", root, err)
	}
	// Состав берётся через общий кеш прогона: спрашивающих у одного дерева
	// десятки, а поднимать процесс git на каждого — та цена, из-за которой
	// прогон проверок подошёл к пределу времени вплотную.
	out, err := listFilesCached(abs)
	if err != nil {
		return nil, fmt.Errorf("treecorpus: git ls-files в %s: %w — проверка не может "+
			"назвать дерево, о котором говорит, а обход диска вместо индекса читал бы "+
			"игнорируемые каталоги (рабочие копии агентов, отчёты прогонов). "+
			"Это отказ, а не пропуск", abs, err)
	}
	return ParseIndex(abs, out), nil
}

// SyntheticTree — состав СИНТЕТИЧЕСКОГО дерева, собранного самой проверкой во
// временном каталоге.
//
// Такое дерево репозиторием не является, спрашивать у него индекс нечего, и
// обход файловой системы здесь — не откат, а единственный возможный авторитет.
// Конструктор ОТДЕЛЬНЫЙ намеренно: откат внутри NewTree был бы невидим, а
// отдельное имя вызывающий выбирает сам и осознанно.
func SyntheticTree(root string) (*Tree, error) {
	t := &Tree{root: root, files: map[string]bool{}, dirs: map[string]bool{}}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		t.add(filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("treecorpus: обход синтетического дерева %s: %w", root, err)
	}
	return t, nil
}

// ParseIndex — разбор вывода `git ls-files -z`. Отдельно от NewTree, чтобы
// инъекция могла подать синтетический ввод, не заводя репозитория.
func ParseIndex(root string, nulSeparated []byte) *Tree {
	t := &Tree{root: root, files: map[string]bool{}, dirs: map[string]bool{}}
	for _, rel := range strings.Split(string(nulSeparated), "\x00") {
		if rel == "" {
			continue
		}
		t.add(filepath.ToSlash(rel))
	}
	return t
}

func (t *Tree) add(rel string) {
	t.files[rel] = true
	for d := filepath.ToSlash(filepath.Dir(rel)); d != "." && d != "/"; d = filepath.ToSlash(filepath.Dir(d)) {
		t.dirs[d] = true
	}
}

// Root — каталог, о котором говорит этот состав.
func (t *Tree) Root() string { return t.root }

// HasFile — файл входит в состав.
func (t *Tree) HasFile(rel string) bool { return t.files[filepath.ToSlash(rel)] }

// HasDir — в каталоге (или ниже) есть хоть один файл состава. Каталог, о котором
// состав не знает, обходить незачем.
func (t *Tree) HasDir(rel string) bool {
	rel = filepath.ToSlash(rel)
	return rel == "." || rel == "" || t.dirs[rel]
}

// Count — сколько файлов прочитано. Вызывающие печатают это как перепись, чтобы
// «ноль находок» отличалось от «ноль прочитанного».
func (t *Tree) Count() int { return len(t.files) }

// Files — множество файлов состава. Отдаётся ссылкой на внутреннюю карту:
// вызывающие обходят её десятками тысяч раз за прогон, а копия на каждый обход
// стоила бы дороже всего остального вместе. Карта предназначена ТОЛЬКО для
// чтения; менять её значит менять состав дерева под ногами у соседнего гейта.
func (t *Tree) Files() map[string]bool { return t.files }

// SortedFiles — то же множество списком, отсортированным: детерминизм входа
// проверки — часть её контракта, а порядок обхода карты в Go случаен.
func (t *Tree) SortedFiles() []string {
	out := make([]string, 0, len(t.files))
	for f := range t.files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
