// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// imports_injection_test.go — доказательство способности разбора заключения
// импортов УПАСТЬ и СМОЛЧАТЬ.
//
// Каждый мир отличается от своего законного близнеца ОДНИМ фактом — местом
// файла, одним импортом, одним ограничением сборки. Иначе неизвестно, какой из
// фактов дал красное, и вердикт недействителен, хотя выглядит обычным.
//
// Пути набора собираются из ЧАСТЕЙ: написанные целиком, они сделали бы этот
// файл предметом гейта фундамента, который судит все отслеживаемые файлы `.go`
// дерева, — строки он не судит, но читатель не обязан это помнить.
package treehygiene_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

var (
	joseV3   = "github.com/go-jose/" + "go-jose/v3"
	joseV4JW = "github.com/go-jose/" + "go-jose/v4/jwt"
	joseOld  = "gopkg.in/square/" + "go-jose.v2"
	ourJWT   = "github.com/golang-jwt/" + "jwt/v5"
)

// testRule — правило в форме фундамента: набор законен под `engine/`.
func testRule() treehygiene.ImportConfinement {
	return treehygiene.ImportConfinement{
		Name:            "JOSE-библиотека движка",
		Set:             []string{"github.com/go-jose/" + "go-jose", "gopkg.in/square/" + "go-jose"},
		Homes:           []string{"engine"},
		ForbiddenAtHome: []string{"github.com/golang-jwt/" + "jwt"},
	}
}

func goFile(pkg string, imports ...string) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\n")
	for _, imp := range imports {
		b.WriteString("import _ \"" + imp + "\"\n")
	}
	return b.String()
}

func auditImports(t *testing.T, root string, rule treehygiene.ImportConfinement) (
	[]treehygiene.ImportFinding, treehygiene.ImportCensus) {
	t.Helper()
	findings, census, err := treehygiene.AuditImportConfinement(root, rule)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: обход синтетического дерева отказал: %v", err)
	}
	t.Log(census)
	return findings, census
}

// requireOneFinding — ровно одна находка, и она называет файл и путь.
func requireOneFinding(t *testing.T, findings []treehygiene.ImportFinding, file, path string) {
	t.Helper()
	if len(findings) != 1 {
		t.Fatalf("находок %d шт, ожидалась 1: %v", len(findings), findings)
	}
	text := findings[0].String()
	if !strings.HasPrefix(findings[0].Position, file+":") || findings[0].Path != path ||
		!strings.Contains(text, file) || !strings.Contains(text, path) {
		t.Fatalf("находка не называет %s и %q: %s", file, path, text)
	}
}

// TestImportConfinementControlIsSilent — контроль: набор импортируется дома,
// снаружи — нет. Ноль находок, и перепись видит импортёра дома.
func TestImportConfinementControlIsSilent(t *testing.T) {
	root := synthTree(t, map[string]string{
		"engine/sign.go":      goFile("engine", joseV3),
		"surface/api.go":      goFile("surface", "context"),
		"surface/api_test.go": goFile("surface", "testing"),
	})
	findings, census := auditImports(t, root, testRule())
	if len(findings) != 0 {
		t.Fatalf("контроль дал находки: %v", findings)
	}
	if census.GoFiles != 3 || census.HomeImporters != 1 {
		t.Fatalf("перепись не та: %v (ожидалось файлов .go 3, импортёров дома 1)", census)
	}
}

// TestImportConfinementTestFileOutsideHomeIsFound — проба вне дома,
// импортирующая набор, — находка; тот же файл дома — молчание (отличие —
// одно место).
func TestImportConfinementTestFileOutsideHomeIsFound(t *testing.T) {
	outside := synthTree(t, map[string]string{
		"engine/sign.go":         goFile("engine", joseV3),
		"surface/verify_test.go": goFile("surface", joseV3),
	})
	findings, _ := auditImports(t, outside, testRule())
	requireOneFinding(t, findings, "surface/verify_test.go", joseV3)

	twin := synthTree(t, map[string]string{
		"engine/sign.go":        goFile("engine", joseV3),
		"engine/verify_test.go": goFile("engine", joseV3),
	})
	if findings, census := auditImports(t, twin, testRule()); len(findings) != 0 || census.HomeImporters != 2 {
		t.Fatalf("близнец дома: находки %v, перепись %v", findings, census)
	}
}

// TestImportConfinementBuildConstrainedFileIsRead — файл под ограничением
// сборки читается: импорт набора в нём — находка; тот же файл без импорта —
// молчание (отличие — один импорт).
func TestImportConfinementBuildConstrainedFileIsRead(t *testing.T) {
	constrained := func(imports ...string) string {
		return "//go:build integration\n\n" + goFile("surface", imports...)
	}
	defect := synthTree(t, map[string]string{
		"engine/sign.go":              goFile("engine", joseV3),
		"surface/integration_test.go": constrained(joseV4JW),
	})
	findings, _ := auditImports(t, defect, testRule())
	requireOneFinding(t, findings, "surface/integration_test.go", joseV4JW)

	twin := synthTree(t, map[string]string{
		"engine/sign.go":              goFile("engine", joseV3),
		"surface/integration_test.go": constrained("context"),
	})
	if findings, census := auditImports(t, twin, testRule()); len(findings) != 0 || census.GoFiles != 2 {
		t.Fatalf("близнец без импорта: находки %v, перепись %v", findings, census)
	}
}

// TestImportConfinementKnowsEveryLawfulFormOfTheSet — каждая законная форма
// пути набора узнаётся, а соседний модуль с похожим именем — нет.
//
// Форма в другом регистре — тоже законная форма: хостинг кода не различает
// регистра организации и репозитория, и прокси модулей отдаёт модуль под
// `github.com/Go-Jose/go-jose` так же, как под `github.com/go-jose/go-jose`.
// Чужой модуль в другом регистре остаётся чужим.
func TestImportConfinementKnowsEveryLawfulFormOfTheSet(t *testing.T) {
	set := testRule().Set
	for _, path := range []string{
		"github.com/go-jose/" + "go-jose",
		joseV3,
		joseV4JW,
		joseOld,
		joseOld + "/jwt",
		"github.com/Go-Jose/" + "go-jose/v3",
		"github.com/go-jose/" + "Go-Jose",
		"gopkg.in/Square/" + "go-jose.v2",
	} {
		if !treehygiene.InImportSet(path, set) {
			t.Errorf("форма %q набора не узнана", path)
		}
	}
	for _, path := range []string{
		"github.com/go-jose/" + "go-josex",
		"github.com/go-jose/" + "other",
		ourJWT,
		"github.com/Go-Jose/" + "Go-Josex",
		"github.com/Golang-JWT/" + "jwt/v5",
	} {
		if treehygiene.InImportSet(path, set) {
			t.Errorf("чужой модуль %q назван набором", path)
		}
	}

	oldForm := synthTree(t, map[string]string{
		"engine/sign.go": goFile("engine", joseV3),
		"surface/old.go": goFile("surface", joseOld),
	})
	findings, _ := auditImports(t, oldForm, testRule())
	requireOneFinding(t, findings, "surface/old.go", joseOld)
}

// TestImportConfinementReadsDeclarationsNotText — путь набора в комментарии и
// в строке — не импорт.
func TestImportConfinementReadsDeclarationsNotText(t *testing.T) {
	root := synthTree(t, map[string]string{
		"engine/sign.go": goFile("engine", joseV3),
		"surface/doc.go": "// Здесь упомянут " + joseV3 + ", но не импортирован.\npackage surface\n\n" +
			"const where = \"" + joseV3 + "\"\n",
	})
	if findings, _ := auditImports(t, root, testRule()); len(findings) != 0 {
		t.Fatalf("упоминание в тексте названо импортом: %v", findings)
	}
}

// TestImportConfinementOurStackAtHomeIsFound — обратная сторона: дом не берёт
// нашего стека. Тот же импорт снаружи — законен (отличие — одно место).
func TestImportConfinementOurStackAtHomeIsFound(t *testing.T) {
	defect := synthTree(t, map[string]string{
		"engine/sign.go":  goFile("engine", joseV3),
		"engine/mixed.go": goFile("engine", ourJWT),
	})
	findings, _ := auditImports(t, defect, testRule())
	requireOneFinding(t, findings, "engine/mixed.go", ourJWT)

	twin := synthTree(t, map[string]string{
		"engine/sign.go":    goFile("engine", joseV3),
		"surface/signer.go": goFile("surface", ourJWT),
	})
	if findings, _ := auditImports(t, twin, testRule()); len(findings) != 0 {
		t.Fatalf("наш стек снаружи дома назван находкой: %v", findings)
	}
}

// TestImportConfinementWithoutHomeForbidsEverywhere — правило потребителя:
// дома нет, набор не законен нигде.
func TestImportConfinementWithoutHomeForbidsEverywhere(t *testing.T) {
	rule := testRule()
	rule.Homes = nil
	root := synthTree(t, map[string]string{
		"engine/sign.go": goFile("engine", joseV3),
	})
	findings, census := auditImports(t, root, rule)
	requireOneFinding(t, findings, "engine/sign.go", joseV3)
	if census.HomeImporters != 0 {
		t.Fatalf("без дома перепись назвала импортёров дома: %v", census)
	}
}

// TestImportConfinementHomeImportersCountIsZeroWhenTheStackIsGone — дом без
// импортёров набора виден переписью: вердикт «предмет пропал» выносит
// вызывающий, и ему есть по чему.
func TestImportConfinementHomeImportersCountIsZeroWhenTheStackIsGone(t *testing.T) {
	root := synthTree(t, map[string]string{
		"engine/sign.go": goFile("engine", "crypto/ed25519"),
	})
	findings, census := auditImports(t, root, testRule())
	if len(findings) != 0 || census.HomeImporters != 0 || census.GoFiles != 1 {
		t.Fatalf("находки %v, перепись %v (ожидались 0 находок, 0 импортёров дома, 1 файл)", findings, census)
	}
}

// TestImportConfinementRefusesToJudgeWhatItCannotSee — три отказа обхода:
// пустой набор, дом, которого нет, неразбираемый файл. Каждый — отказ с
// именем предмета, а не «ноль находок».
func TestImportConfinementRefusesToJudgeWhatItCannotSee(t *testing.T) {
	root := synthTree(t, map[string]string{
		"engine/sign.go": goFile("engine", joseV3),
	})

	empty := testRule()
	empty.Set = nil
	if _, _, err := treehygiene.AuditImportConfinement(root, empty); err == nil {
		t.Error("пустой набор принят")
	}

	moved := testRule()
	moved.Homes = []string{"engine-moved"}
	if _, _, err := treehygiene.AuditImportConfinement(root, moved); err == nil ||
		!strings.Contains(err.Error(), "engine-moved") {
		t.Errorf("дом, которого нет, не назван отказом: %v", err)
	}

	whole := testRule()
	whole.Homes = []string{"."}
	if _, _, err := treehygiene.AuditImportConfinement(root, whole); err == nil {
		t.Error("дом «весь корень» принят")
	}

	broken := synthTree(t, map[string]string{
		"engine/sign.go":    goFile("engine", joseV3),
		"surface/broken.go": "package surface\n\nimport (\n",
	})
	if _, _, err := treehygiene.AuditImportConfinement(broken, testRule()); err == nil ||
		!strings.Contains(err.Error(), "surface/broken.go") {
		t.Errorf("неразбираемый файл не назван отказом: %v", err)
	}

	noGo := synthTree(t, map[string]string{"docs/readme.txt": "текст\n"})
	rule := testRule()
	rule.Homes = nil
	if _, _, err := treehygiene.AuditImportConfinement(noGo, rule); err == nil {
		t.Error("дерево без файлов .go дало вердикт")
	}
}
