// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// forms_test.go — правило ЭТОГО дерева знает КАЖДУЮ опубликованную форму пути
// стека JOSE движка.
//
// Форма, которой правило не знает, — не находка и не чистота, а невидимость:
// подписант на такой форме вне поддерева собирается, а гейт молчит. Так и было
// с двумя формами go-jose — `gopkg.in/go-jose/go-jose.vN` (путь после переезда
// организации) и `github.com/square/go-jose` (исходный путь): правило знало
// `github.com/go-jose/go-jose` и `gopkg.in/square/go-jose`, и импорт двух
// других вне поддерева проходил.
//
// Перечень форм ВЫВЕДЕН ОПРОСОМ ПРОКСИ МОДУЛЕЙ, а не записан по памяти:
// `curl https://proxy.golang.org/<путь>/@v/list` по каждому пути истории
// библиотеки (снято 2026-09-23). Отвечают версиями: `github.com/go-jose/go-jose`
// (и `/v3`, `/v4`), `gopkg.in/go-jose/go-jose.v1`, `.v2`, `.v3`,
// `gopkg.in/square/go-jose.v1`, `.v2`, `github.com/square/go-jose` (и `/v3`
// псевдоверсией), `github.com/cristalhq/jwt` (и `/v3`, `/v4`, `/v5`). Путь
// `github.com/square/go-jose/v2` прокси не знает — его в перечне нет.
//
// # Регистр пути — не граница набора
//
// Прокси модулей отдаёт ту же библиотеку и под путём в ДРУГОМ регистре:
// организация и репозиторий на хостинге кода не различают регистра, и
// `github.com/Square/go-jose` собирается ровно как `github.com/square/go-jose`.
// Опрошено тем же способом (путь в регистровой кодировке прокси, `!s` вместо
// `S`; снято 2026-09-23): отвечают версиями `github.com/Square/go-jose`,
// `github.com/Go-Jose/go-jose` (и `/v3`, `/v4`), `github.com/go-jose/Go-Jose`,
// `gopkg.in/Square/go-jose.v2`, `gopkg.in/Go-Jose/go-jose.v2`,
// `gopkg.in/go-jose/Go-Jose.v2`, `github.com/Cristalhq/jwt` (и `/v5`),
// `github.com/Golang-JWT/jwt/v5`. Хост и суффикс `.vN` в другом регистре прокси
// отвергает как неверно составленный путь — такой формы у импорта нет. Правило,
// сравнивающее путь с учётом регистра, пропускало каждую из них.
//
// Пути собраны из частей по той же причине, что в confinement_test.go.
package engineconfinement_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/gitenv"
	"github.com/PRO-Robotech/corelib/treehygiene"
)

// publishedStackForms — по одному пакету на каждую опубликованную форму пути.
var publishedStackForms = []string{
	"github.com/go-jose/" + "go-jose",
	"github.com/go-jose/" + "go-jose/v3",
	"github.com/go-jose/" + "go-jose/v4/jwt",
	"gopkg.in/go-jose/" + "go-jose.v1",
	"gopkg.in/go-jose/" + "go-jose.v2",
	"gopkg.in/go-jose/" + "go-jose.v2/jwt",
	"gopkg.in/go-jose/" + "go-jose.v3",
	"gopkg.in/square/" + "go-jose.v1",
	"gopkg.in/square/" + "go-jose.v2",
	"github.com/square/" + "go-jose",
	"github.com/square/" + "go-jose/jwt",
	"github.com/square/" + "go-jose/v3",
	"github.com/cristalhq/" + "jwt",
	"github.com/cristalhq/" + "jwt/v5",
}

// caseVariantStackForms — те же пути в другом регистре, каждый из них прокси
// модулей отдаёт версиями (см. шапку файла).
var caseVariantStackForms = []string{
	"github.com/Square/" + "go-jose",
	"github.com/Go-Jose/" + "go-jose",
	"github.com/go-jose/" + "Go-Jose",
	"github.com/Go-Jose/" + "go-jose/v3",
	"github.com/Go-Jose/" + "go-jose/v4/jwt",
	"gopkg.in/Square/" + "go-jose.v2",
	"gopkg.in/Go-Jose/" + "go-jose.v2",
	"gopkg.in/go-jose/" + "Go-Jose.v2/jwt",
	"github.com/Cristalhq/" + "jwt",
	"github.com/Cristalhq/" + "jwt/v5",
}

// synthTree — отслеживаемое дерево во временном каталоге: обход берёт состав у
// индекса git, и дерево без индекса дало бы «прочитано 0».
func synthTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet", "-b", "main"}, {"add", "-A"}} {
		if out, err := gitenv.Command(root, args...).CombinedOutput(); err != nil {
			t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func goFile(pkg string, imports ...string) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\n")
	for _, imp := range imports {
		b.WriteString("import _ \"" + imp + "\"\n")
	}
	return b.String()
}

// requireFormConfined — импорт формы ВНЕ поддерева — ровно одна находка с
// координатой; тот же файл ВНУТРИ поддерева — молчание (отличие — одно место).
func requireFormConfined(t *testing.T, form string) {
	t.Helper()
	homeImport := "github.com/go-jose/" + "go-jose/v3"
	outside := synthTree(t, map[string]string{
		subtreeRoot + "/engine.go": goFile("oauth2", homeImport),
		"surface/sign.go":          goFile("surface", form),
	})
	findings, census, err := treehygiene.AuditImportConfinement(outside, engineStackRule())
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
	}
	if len(findings) != 1 || !strings.HasPrefix(findings[0].Position, "surface/sign.go:") || findings[0].Path != form {
		t.Fatalf("импорт %q вне поддерева не назван находкой с координатой: находки %v (%v)", form, findings, census)
	}

	inside := synthTree(t, map[string]string{
		subtreeRoot + "/engine.go": goFile("oauth2", homeImport),
		subtreeRoot + "/sign.go":   goFile("oauth2", form),
	})
	findings, census, err = treehygiene.AuditImportConfinement(inside, engineStackRule())
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
	}
	if len(findings) != 0 || census.HomeImporters != 2 {
		t.Fatalf("близнец внутри поддерева: находки %v, перепись %v (ожидалось 0 находок и 2 импортёра дома)", findings, census)
	}
}

// TestEngineRuleKnowsEveryPublishedFormOfTheStack — каждая опубликованная
// форма пути заключена в поддерево.
func TestEngineRuleKnowsEveryPublishedFormOfTheStack(t *testing.T) {
	for _, form := range publishedStackForms {
		t.Run(form, func(t *testing.T) { requireFormConfined(t, form) })
	}
}

// TestEngineRuleIgnoresTheCaseOfThePath — та же форма в другом регистре
// заключена так же: подписант на `github.com/Square/go-jose` вне поддерева —
// тот же второй путь подписи, что на `github.com/square/go-jose`.
func TestEngineRuleIgnoresTheCaseOfThePath(t *testing.T) {
	for _, form := range caseVariantStackForms {
		t.Run(form, func(t *testing.T) { requireFormConfined(t, form) })
	}
}

// TestEngineRuleForbidsOurStackAtHomeInAnyCase — обратная сторона правила
// судится тем же сравнением: стек подписанта платформы в другом регистре
// внутри поддерева — находка; тот же файл вне поддерева — молчание.
func TestEngineRuleForbidsOurStackAtHomeInAnyCase(t *testing.T) {
	homeImport := "github.com/go-jose/" + "go-jose/v3"
	ours := "github.com/Golang-JWT/" + "jwt/v5"
	inside := synthTree(t, map[string]string{
		subtreeRoot + "/engine.go": goFile("oauth2", homeImport),
		subtreeRoot + "/mixed.go":  goFile("oauth2", ours),
	})
	findings, census, err := treehygiene.AuditImportConfinement(inside, engineStackRule())
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
	}
	if len(findings) != 1 || !strings.HasPrefix(findings[0].Position, subtreeRoot+"/mixed.go:") || findings[0].Path != ours {
		t.Fatalf("импорт %q внутри поддерева не назван находкой с координатой: находки %v (%v)", ours, findings, census)
	}

	outside := synthTree(t, map[string]string{
		subtreeRoot + "/engine.go": goFile("oauth2", homeImport),
		"surface/mixed.go":         goFile("surface", ours),
	})
	findings, census, err = treehygiene.AuditImportConfinement(outside, engineStackRule())
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("близнец вне поддерева: находки %v, перепись %v (ожидалось 0 находок)", findings, census)
	}
}

// TestEngineRuleDoesNotJudgeLookalikeNames — законный близнец с другой
// стороны: модуль, чьё имя лишь начинается так же, набором не является. Без
// него пробы выше были бы зелёными и у правила «всё, где есть go-jose».
func TestEngineRuleDoesNotJudgeLookalikeNames(t *testing.T) {
	homeImport := "github.com/go-jose/" + "go-jose/v3"
	for _, lookalike := range []string{
		"github.com/go-jose/" + "go-josex",
		"github.com/square/" + "go-jose-util",
		"gopkg.in/go-jose/" + "go-josefork.v2",
		"github.com/Go-Jose/" + "Go-Josex",
		"github.com/Square/" + "go-jose-util",
	} {
		root := synthTree(t, map[string]string{
			subtreeRoot + "/engine.go": goFile("oauth2", homeImport),
			"surface/other.go":         goFile("surface", lookalike),
		})
		findings, _, err := treehygiene.AuditImportConfinement(root, engineStackRule())
		if err != nil {
			t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
		}
		if len(findings) != 0 {
			t.Errorf("чужой модуль %q назван находкой: %v", lookalike, findings)
		}
	}
}
