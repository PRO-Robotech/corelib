// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_valueerr.go — узел «ошибка Value() отброшена» (CX1-41 (б), УК46).
// Ловит результат-ошибку метода Value любого из пяти непрозрачных типов
// (form.Path, form.Token, form.HeaderText, address.Normalized,
// address.Domain), присвоенную «_» или не присвоенную, — по идентичности
// метода из проверки типов, вне пакетов этих типов. Значение-метод Value
// (селектор вне позиции вызываемого) — тоже узел: его ошибку вызов через
// значение уводит из-под надзора.
package treehygiene

import (
	"fmt"
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/ast/inspector"
)

// opaqueTypes — пять непрозрачных типов: пакет и имя.
var opaqueTypes = [][2]string{
	{formPkg, "Path"}, {formPkg, "Token"}, {formPkg, "HeaderText"},
	{addressPkg, "Normalized"}, {addressPkg, "Domain"},
}

func isOpaque(t types.Type) bool {
	n, ok := types.Unalias(derefType(t)).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	for _, o := range opaqueTypes {
		if n.Obj().Pkg().Path() == o[0] && n.Obj().Name() == o[1] {
			return true
		}
	}
	return false
}

// ValueErrReport — исход узла по одному дереву.
type ValueErrReport struct {
	Census TreeCensus
	// Calls — вызовов Value непрозрачных типов вне их пакетов.
	Calls    int
	Findings []Finding
}

func (r ValueErrReport) String() string {
	return fmt.Sprintf("ошибка Value(): %s · типов %d · вызовов %d · находок %d",
		r.Census, len(opaqueTypes), r.Calls, len(r.Findings))
}

// AuditValueErrorDiscard — узел CX1-41 по дереву root.
func AuditValueErrorDiscard(root, stubDir string) (ValueErrReport, error) {
	g, err := loadGoTree(root, stubDir)
	if err != nil {
		return ValueErrReport{}, err
	}
	r := ValueErrReport{Census: g.census()}
	for _, gf := range g.files {
		if gf.pkg.PkgPath == formPkg || gf.pkg.PkgPath == addressPkg {
			continue
		}
		info := gf.pkg.TypesInfo
		in := inspector.New([]*ast.File{gf.file})
		in.WithStack([]ast.Node{(*ast.SelectorExpr)(nil)}, func(n ast.Node, push bool, stack []ast.Node) bool {
			if !push {
				return true
			}
			sel := n.(*ast.SelectorExpr)
			s, ok := info.Selections[sel]
			if !ok || s.Kind() != types.MethodVal || s.Obj().Name() != "Value" || !isOpaque(s.Recv()) {
				return true
			}
			if len(stack) < 2 {
				return true
			}
			call, ok := stack[len(stack)-2].(*ast.CallExpr)
			if !ok || call.Fun != sel {
				r.Findings = append(r.Findings, Finding{Position: g.pos(sel.Pos()), Kind: "value-method-value",
					Why: "значение-метод Value непрозрачного типа — его ошибка уходит из-под надзора"})
				return true
			}
			r.Calls++
			if errorDiscarded(stack[:len(stack)-1], call) {
				r.Findings = append(r.Findings, Finding{Position: g.pos(call.Pos()), Kind: "value-error-discarded",
					Why: "ошибка Value() непрозрачного типа отброшена — нулевое значение уходит дальше"})
			}
			return true
		})
	}
	sortFindings(r.Findings)
	return r, nil
}
