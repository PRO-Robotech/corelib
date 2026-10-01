// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_idna.go — NTF1-B27 (гейт): нормализация адреса и её доменная часть
// ровно одни. Узел — не-тестовая ссылка на функцию или метод пакета
// golang.org/x/net/idna (вызов либо значение-функция), по идентичности из
// проверки типов. Допустимое место — только пакет corelib/notify/address.
// Понижение регистра прочих строк узлом не является.
package treehygiene

import (
	"fmt"
	"go/ast"
	"go/types"
)

// IDNAReport — исход гейта по одному дереву.
type IDNAReport struct {
	Census TreeCensus
	// Nodes — ссылок на функции idna, включая допустимое место.
	Nodes    int
	Findings []Finding
}

func (r IDNAReport) String() string {
	return fmt.Sprintf("IDNA: %s · узлов %d · находок %d", r.Census, r.Nodes, len(r.Findings))
}

// AuditIDNASingular — NTF1-B27 (гейт) по дереву root.
func AuditIDNASingular(root, stubDir string) (IDNAReport, error) {
	g, err := loadGoTree(root, stubDir)
	if err != nil {
		return IDNAReport{}, err
	}
	r := IDNAReport{Census: g.census()}
	for _, gf := range g.files {
		home := gf.pkg.PkgPath == addressPkg
		ast.Inspect(gf.file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			fn, ok := gf.pkg.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != idnaPkg {
				return true
			}
			r.Nodes++
			if !home {
				r.Findings = append(r.Findings, Finding{Position: g.pos(id.Pos()), Kind: "idna",
					Why: fmt.Sprintf("ссылка на %s вне %s — вторая нормализация домена", fn.FullName(), addressPkg)})
			}
			return true
		})
	}
	sortFindings(r.Findings)
	return r, nil
}
