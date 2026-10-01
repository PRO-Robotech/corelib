// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_domainreceivers.go — NTF1-B27 (гейт, приёмник address.Domain).
// Производитель и приёмник различаются (Р8, Д19): вызов address.NormalizeDomain
// с ошибкой, связанной с именем, — производитель, узлом не является. Узлы —
// не-тестовый код вне пакета corelib/notify/address, по идентичности из
// проверки типов:
//
//	(1) ссылка на тип address.Domain (переменная, параметр, результат, поле,
//	    элемент, ключ, составной литерал, new, утверждение типа, ветка
//	    переключателя типа);
//	(2) подстановка address.Domain в параметр типа, явная или выведенная;
//	(3) прямой вызов address.NormalizeDomain, чья ошибка присвоена «_» или
//	    отброшена вместе с результатом;
//	(4) иное использование производителя — форма (а) CX1-56: всякое
//	    использование объекта NormalizeDomain вне позиции вызываемого в прямом
//	    вызове (значение-функция, аргумент, поле). Тот же выбор, что у узла
//	    IDNA: двух правил об одном предмете в гейте нет.
//
// Допустимых мест приёмника нет.
package treehygiene

import (
	"fmt"
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/ast/inspector"
)

// DomainKind — вид узла приёмника address.Domain.
type DomainKind string

// Виды узла приёмника.
const (
	DomainTypeReference  DomainKind = "domain-type-reference"
	DomainTypeArgument   DomainKind = "domain-type-argument"
	DomainErrorDiscarded DomainKind = "normalize-domain-error-discarded"
	DomainProducerValue  DomainKind = "normalize-domain-other-use"
)

// DomainReport — исход гейта по одному дереву.
type DomainReport struct {
	Census TreeCensus
	// ProducerCalls — прямых вызовов NormalizeDomain вне пакета.
	ProducerCalls int
	Findings      []Finding
}

func (r DomainReport) String() string {
	return fmt.Sprintf("приёмник address.Domain: %s · вызовов производителя %d · узлов %d",
		r.Census, r.ProducerCalls, len(r.Findings))
}

func isDomain(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == addressPkg && n.Obj().Name() == "Domain"
}

// mentionsDomain — тип есть address.Domain либо собран из него.
func mentionsDomain(t types.Type) bool {
	switch x := types.Unalias(t).(type) {
	case *types.Named:
		if isDomain(x) {
			return true
		}
		for i := 0; i < x.TypeArgs().Len(); i++ {
			if mentionsDomain(x.TypeArgs().At(i)) {
				return true
			}
		}
	case *types.Pointer:
		return mentionsDomain(x.Elem())
	case *types.Slice:
		return mentionsDomain(x.Elem())
	case *types.Array:
		return mentionsDomain(x.Elem())
	case *types.Map:
		return mentionsDomain(x.Key()) || mentionsDomain(x.Elem())
	case *types.Chan:
		return mentionsDomain(x.Elem())
	}
	return false
}

// AuditDomainReceivers — узел приёмника address.Domain по дереву root.
func AuditDomainReceivers(root, stubDir string) (DomainReport, error) {
	g, err := loadGoTree(root, stubDir)
	if err != nil {
		return DomainReport{}, err
	}
	r := DomainReport{Census: g.census()}
	add := func(pos string, k DomainKind, why string) {
		r.Findings = append(r.Findings, Finding{Position: pos, Kind: string(k), Why: why})
	}
	for _, gf := range g.files {
		if gf.pkg.PkgPath == addressPkg {
			continue
		}
		info := gf.pkg.TypesInfo
		for id, inst := range info.Instances {
			if inst.TypeArgs == nil || gf.pkg.Fset.File(id.Pos()) != gf.pkg.Fset.File(gf.file.Pos()) {
				continue
			}
			for i := 0; i < inst.TypeArgs.Len(); i++ {
				if mentionsDomain(inst.TypeArgs.At(i)) {
					add(g.pos(id.Pos()), DomainTypeArgument, "address.Domain подставлен в параметр типа "+id.Name)
					break
				}
			}
		}
		in := inspector.New([]*ast.File{gf.file})
		in.WithStack([]ast.Node{(*ast.Ident)(nil)}, func(n ast.Node, push bool, stack []ast.Node) bool {
			if !push {
				return true
			}
			id := n.(*ast.Ident)
			switch obj := info.Uses[id].(type) {
			case *types.TypeName:
				if isDomain(obj.Type()) {
					add(g.pos(id.Pos()), DomainTypeReference, "ссылка на тип address.Domain вне пакета")
				}
			case *types.Func:
				if obj.Pkg() == nil || obj.Pkg().Path() != addressPkg || obj.Name() != "NormalizeDomain" {
					return true
				}
				call, ok := calleeCall(stack)
				if !ok {
					add(g.pos(id.Pos()), DomainProducerValue, "использование NormalizeDomain вне прямого вызова")
					return true
				}
				r.ProducerCalls++
				if errorDiscarded(stack, call) {
					add(g.pos(call.Pos()), DomainErrorDiscarded, "ошибка NormalizeDomain отброшена — нулевой Domain уходит дальше")
				}
			}
			return true
		})
	}
	sortFindings(r.Findings)
	return r, nil
}

// calleeCall — вызов, в котором идентификатор на вершине стека стоит
// позицией вызываемого (прямо либо селектором пакета).
func calleeCall(stack []ast.Node) (*ast.CallExpr, bool) {
	id := stack[len(stack)-1]
	fun := id
	i := len(stack) - 2
	if i >= 0 {
		if sel, ok := stack[i].(*ast.SelectorExpr); ok && sel.Sel == id {
			fun = sel
			i--
		}
	}
	if i < 0 {
		return nil, false
	}
	call, ok := stack[i].(*ast.CallExpr)
	if !ok || call.Fun != fun {
		return nil, false
	}
	return call, true
}

// errorDiscarded — второй результат вызова присвоен «_» либо результат
// вызова отброшен целиком.
func errorDiscarded(stack []ast.Node, call *ast.CallExpr) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] != call {
			continue
		}
		if i == 0 {
			return false
		}
		switch p := stack[i-1].(type) {
		case *ast.ExprStmt, *ast.GoStmt, *ast.DeferStmt:
			return true
		case *ast.AssignStmt:
			if len(p.Rhs) == 1 && p.Rhs[0] == call && len(p.Lhs) == 2 {
				if b, ok := p.Lhs[1].(*ast.Ident); ok && b.Name == "_" {
					return true
				}
			}
		case *ast.ValueSpec:
			if len(p.Values) == 1 && p.Values[0] == call && len(p.Names) == 2 && p.Names[1].Name == "_" {
				return true
			}
		}
		return false
	}
	return false
}
