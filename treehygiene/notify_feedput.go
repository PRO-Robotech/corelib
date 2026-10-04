// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_feedput.go — NTF1-B28 (гейт Put) и УК63.
//
// Описание атрибутов в постановке ленты в рабочем коде — только описание
// генератора. Глаголов постановки два — feed.Put и feed.PutID (тот же Put,
// отвечающий id строки); узел — ссылка на объект любого из них (вызов или
// значение-функция, по идентичности из проверки типов) в не-тестовом файле
// вне пакета corelib/notify/feed и вне множества файлов, которое печатает
// `notifygen -check -list` по дереву (NTF1-D01). Множество берётся из этого
// вывода, а не из заголовка файла; вывод, который не разбирается, — отказ
// гейта, а не пустое множество. Вызов глагола мимо ссылки на объект
// (go:linkname, ассемблер, объектный файл, cgo) этот узел не видит — его судит
// обход типобезопасности (notify_typesafety.go).
//
// УК63: уборщик ленты поднимается вне вопроса о флаге (CX1-55 (а)). Вызов
// feed.StartSweeper в теле ветки условия, читающего флаг ((feed.Enabled).On
// либо (*feed.Source).Enabled), — находка.
package treehygiene

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/types"
	"path"
	"strings"

	"golang.org/x/tools/go/ast/inspector"
)

// FeedPutKind — вид находки гейта ссылок на feed.
type FeedPutKind string

// Виды находок.
const (
	FeedPutOutsideGenerator FeedPutKind = "feed-put-outside-generator"
	FeedSweeperUnderFlag    FeedPutKind = "start-sweeper-under-flag"
)

// FeedPutReport — исход гейта по одному дереву.
type FeedPutReport struct {
	Census TreeCensus
	// GeneratorFiles — файлов Go из множества -list.
	GeneratorFiles int
	// RefsInGenerated, RefsOutside — ссылок на feed.Put и feed.PutID в файлах генератора и
	// вне их (вне пакета feed).
	RefsInGenerated, RefsOutside int
	// SweeperStarts — вызовов feed.StartSweeper.
	SweeperStarts int
	Findings      []Finding
}

func (r FeedPutReport) String() string {
	return fmt.Sprintf("ссылки на feed.Put/PutID: %s · файлов генератора %d · ссылок в них %d · вне их %d · StartSweeper %d",
		r.Census, r.GeneratorFiles, r.RefsInGenerated, r.RefsOutside, r.SweeperStarts)
}

// parseGeneratedList разбирает вывод `notifygen -check -list`: строки
// «file <путь от корня>». Иная строка, пустой путь и путь вне дерева — отказ.
func parseGeneratedList(out []byte) (map[string]bool, error) {
	set := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if line == "" {
			continue
		}
		p, ok := strings.CutPrefix(line, "file ")
		if !ok {
			return nil, fmt.Errorf("treehygiene: вывод notifygen -list, строка %d не разбирается: %q", n, line)
		}
		clean := path.Clean(p)
		if p == "" || clean != p || path.IsAbs(p) || strings.HasPrefix(clean, "../") || clean == ".." {
			return nil, fmt.Errorf("treehygiene: вывод notifygen -list, строка %d: путь %q не под корнем дерева", n, p)
		}
		set[p] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("treehygiene: вывод notifygen -list не прочитан: %w", err)
	}
	return set, nil
}

// AuditFeedPutReferences — NTF1-B28 (гейт Put) и УК63 по дереву root;
// generatedList — вывод `notifygen -check -list` по тому же дереву, который
// вызывающий получил исполнением генератора. Генератор, не исполнившийся, —
// отказ вызывающего до этого вызова.
func AuditFeedPutReferences(root, stubDir string, generatedList []byte) (FeedPutReport, error) {
	listed, err := parseGeneratedList(generatedList)
	if err != nil {
		return FeedPutReport{}, err
	}
	g, err := loadGoTree(root, stubDir)
	if err != nil {
		return FeedPutReport{}, err
	}
	r := FeedPutReport{Census: g.census()}
	for p := range listed {
		if strings.HasSuffix(p, ".go") {
			if !g.tree.HasFile(p) {
				return r, fmt.Errorf("treehygiene: файла %s из вывода -list нет в индексе дерева — множество не о нём", p)
			}
			r.GeneratorFiles++
		}
	}
	for _, gf := range g.files {
		if gf.pkg.PkgPath == feedPkg {
			continue
		}
		info := gf.pkg.TypesInfo
		gen := listed[gf.rel]
		in := inspector.New([]*ast.File{gf.file})
		in.WithStack([]ast.Node{(*ast.Ident)(nil)}, func(n ast.Node, push bool, stack []ast.Node) bool {
			if !push {
				return true
			}
			id := n.(*ast.Ident)
			fn, ok := info.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != feedPkg {
				return true
			}
			switch fn.Name() {
			case "Put", "PutID":
				if gen {
					r.RefsInGenerated++
					return true
				}
				r.RefsOutside++
				r.Findings = append(r.Findings, Finding{Position: g.pos(id.Pos()), Kind: string(FeedPutOutsideGenerator),
					Why: "ссылка на feed." + fn.Name() + " вне файлов, которые порождает notifygen — описание атрибутов не генератора"})
			case "StartSweeper":
				r.SweeperStarts++
				if cond := flagCondition(info, stack); cond != nil {
					r.Findings = append(r.Findings, Finding{Position: g.pos(id.Pos()), Kind: string(FeedSweeperUnderFlag),
						Why: "feed.StartSweeper под условием, читающим флаг (" + g.pos(cond.Pos()) + ") — уборщик обязан работать при любом флаге"})
				}
			}
			return true
		})
	}
	sortFindings(r.Findings)
	return r, nil
}

// flagCondition — условие объемлющей ветки, читающее флаг, в теле (или
// ветке else) которой стоит узел; nil — такого нет. Инициализатор if и само
// условие телом не являются.
func flagCondition(info *types.Info, stack []ast.Node) ast.Expr {
	for i := len(stack) - 2; i >= 0; i-- {
		var cond ast.Expr
		child := stack[i+1]
		switch s := stack[i].(type) {
		case *ast.IfStmt:
			if child == s.Body || child == s.Else {
				cond = s.Cond
			}
		case *ast.CaseClause:
			for _, e := range s.List {
				if readsFlag(info, e) {
					return e
				}
			}
			if i > 0 {
				if sw, ok := stack[i-1].(*ast.SwitchStmt); ok && sw.Tag != nil && readsFlag(info, sw.Tag) {
					return sw.Tag
				}
			}
		}
		if cond != nil && readsFlag(info, cond) {
			return cond
		}
	}
	return nil
}

// readsFlag — выражение зовёт (feed.Enabled).On либо (*feed.Source).Enabled.
func readsFlag(info *types.Info, e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return !found
		}
		fn, ok := info.Uses[sel.Sel].(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != feedPkg {
			return true
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig.Recv() == nil {
			return true
		}
		named, ok := types.Unalias(derefType(sig.Recv().Type())).(*types.Named)
		if !ok {
			return true
		}
		switch {
		case named.Obj().Name() == "Enabled" && fn.Name() == "On",
			named.Obj().Name() == "Source" && fn.Name() == "Enabled":
			found = true
		}
		return !found
	})
	return found
}
