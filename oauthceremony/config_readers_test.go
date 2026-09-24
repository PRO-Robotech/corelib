// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// config_readers_test.go — New не требует того, чего не читает ни один путь
// церемонии: у каждого поля Config, которое судит validateConfig, есть читатель
// вне validateConfig.
//
// Поле, которое проверка настроек требует, а читает одна она, служба была бы
// обязана назвать впустую — и названное значение ни на что бы не влияло.
// Проба разбора, а не отражения: чтение поля — свойство исходника, в собранном
// типе его нет.
//
// # Что разбор признаёт значением Config
//
// Две формы, обе по объявлению, а не по имени:
//   - параметр или приёмник функции (в том числе функционального литерала)
//     типа Config или *Config — по объекту, к которому разбор привязал имя, так
//     что затенённое имя параметром не считается;
//   - поле структуры пакета типа Config или *Config (`c.cfg`) — если имя такого
//     поля не объявлено ни в одной структуре пакета другим типом; иначе обход
//     отказывает судить.
//
// Чтение в иной форме — через локальную копию (`local := c.cfg`), через
// результат вызова — разбор не видит, и поле с таким единственным читателем
// становится находкой: цена упрощения — лишнее красное, а не молчание (это
// утверждает TestConfigReaderInAFormTheScanDoesNotKnowIsRedNotSilent).
//
// # Предпосылки, без которых зелёный не вердикт
//
// validateConfig объявлена ровно одна, свободной функцией, с параметром типа
// Config, и этот параметр она употребляет ТОЛЬКО выборкой поля: переданное
// дальше целиком значение прочитал бы кто-то, кого разбор судил бы как читателя,
// хотя он часть проверки. Поля Config по разбору совпадают с полями собранного
// типа — иначе прочитан не тот пакет.
package oauthceremony_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// configReaderCensus — исход обхода: перепись отдельно от находок.
type configReaderCensus struct {
	files, lines int
	// fields — поля типа настроек в порядке объявления.
	fields []string
	// judged — поля, которые судит валидатор, в порядке объявления.
	judged []string
	// readers — координаты чтения каждого поля вне валидатора.
	readers map[string][]string
	// refusal — почему обход не вердикт; пусто — вердикт есть.
	refusal string
}

// orphans — поля, которые валидатор судит, а не читает никто другой.
func (c configReaderCensus) orphans() []string {
	var out []string
	for _, f := range c.judged {
		if len(c.readers[f]) == 0 {
			out = append(out, f)
		}
	}
	return out
}

// isConfigType — выражение типа называет тип настроек либо указатель на него.
func isConfigType(expr ast.Expr, typeName string) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == typeName
}

// scanConfigReaders обходит не-тестовые .go каталога dir.
func scanConfigReaders(dir, typeName, validator string) configReaderCensus {
	census := configReaderCensus{readers: map[string][]string{}}
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		census.refusal = err.Error()
		return census
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			census.refusal = err.Error()
			return census
		}
		file, err := parser.ParseFile(fset, p, src, 0)
		if err != nil {
			census.refusal = "файл не разобран: " + err.Error()
			return census
		}
		files = append(files, file)
		census.files++
		census.lines += strings.Count(string(src), "\n")
	}

	// Тип настроек, поля-носители и имена полей всех структур пакета.
	declared := 0
	carriers := map[string]bool{}
	otherTyped := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			if spec.Name.Name == typeName {
				declared++
				for _, fld := range st.Fields.List {
					for _, name := range fld.Names {
						census.fields = append(census.fields, name.Name)
					}
				}
			}
			for _, fld := range st.Fields.List {
				for _, name := range fld.Names {
					if isConfigType(fld.Type, typeName) {
						carriers[name.Name] = true
					} else {
						otherTyped[name.Name] = true
					}
				}
			}
			return true
		})
	}
	if declared != 1 || len(census.fields) == 0 {
		census.refusal = fmt.Sprintf("тип %s объявлен %d раз, полей %d — судить нечего",
			typeName, declared, len(census.fields))
		return census
	}
	for name := range carriers {
		if otherTyped[name] {
			census.refusal = fmt.Sprintf("поле-носитель %q объявлено и другим типом — "+
				"выборку через него не отличить от чтения чужого поля", name)
			return census
		}
	}
	isField := map[string]bool{}
	for _, f := range census.fields {
		isField[f] = true
	}

	// isConfigValue — выражение есть значение настроек в одной из двух форм.
	isConfigValue := func(x ast.Expr) bool {
		for {
			switch v := x.(type) {
			case *ast.ParenExpr:
				x = v.X
				continue
			case *ast.StarExpr:
				x = v.X
				continue
			case *ast.Ident:
				if v.Obj == nil || v.Obj.Kind != ast.Var {
					return false
				}
				fld, ok := v.Obj.Decl.(*ast.Field)
				return ok && isConfigType(fld.Type, typeName)
			case *ast.SelectorExpr:
				return carriers[v.Sel.Name]
			}
			return false
		}
	}

	// Обход — каждое объявление верхнего уровня, а не только тела функций:
	// функциональный литерал в инициализаторе переменной пакета — такой же путь.
	validators := 0
	judged := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			isValidator := isFunc && fn.Recv == nil && fn.Name.Name == validator && fn.Body != nil
			selected := map[*ast.Ident]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !isField[sel.Sel.Name] || !isConfigValue(sel.X) {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok {
					selected[id] = true
				}
				if isValidator {
					judged[sel.Sel.Name] = true
				} else {
					pos := fset.Position(sel.Pos())
					at := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
					census.readers[sel.Sel.Name] = append(census.readers[sel.Sel.Name], at)
				}
				return true
			})
			if !isValidator {
				continue
			}
			validators++
			params := 0
			for _, fld := range fn.Type.Params.List {
				if isConfigType(fld.Type, typeName) {
					params += len(fld.Names)
				}
			}
			if params == 0 {
				census.refusal = fmt.Sprintf("%s не принимает %s — судить нечего", validator, typeName)
				return census
			}
			var escaped []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && !selected[id] && isConfigValue(id) {
					pos := fset.Position(id.Pos())
					escaped = append(escaped, fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line))
				}
				return true
			})
			if len(escaped) != 0 {
				census.refusal = fmt.Sprintf("%s употребляет настройки не выборкой поля (%s) — "+
					"значение уходит дальше, и его читатель был бы частью проверки",
					validator, strings.Join(escaped, ", "))
				return census
			}
		}
	}
	if validators != 1 {
		census.refusal = fmt.Sprintf("свободная функция %s объявлена %d раз", validator, validators)
		return census
	}
	for _, f := range census.fields {
		if judged[f] {
			census.judged = append(census.judged, f)
		}
	}
	if len(census.judged) == 0 {
		census.refusal = validator + " не судит ни одного поля — судить нечего"
	}
	return census
}

// TestEveryConfigFieldNewJudgesHasAReader — проба по дереву пакета.
func TestEveryConfigFieldNewJudgesHasAReader(t *testing.T) {
	census := scanConfigReaders(".", "Config", "validateConfig")
	if census.refusal != "" {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s (файлов %d, строк %d)", census.refusal, census.files, census.lines)
	}
	var built []string
	for f := range reflect.TypeFor[oauthceremony.Config]().Fields() {
		built = append(built, f.Name)
	}
	if !slices.Equal(built, census.fields) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: поля Config по разбору %v, у собранного типа %v — прочитан не тот пакет",
			census.fields, built)
	}
	for _, f := range census.orphans() {
		t.Errorf("поле Config.%s судит validateConfig, а не читает ни один путь церемонии — "+
			"New требует его впустую", f)
	}
	for _, f := range census.judged {
		t.Logf("Config.%s: читателей %d — %s", f, len(census.readers[f]), strings.Join(census.readers[f], ", "))
	}
	t.Logf("осмотрено: файлов %d, строк %d; полей Config %d, из них судит validateConfig %d",
		census.files, census.lines, len(census.fields), len(census.judged))
}

// syntheticConfigPackage — пакет из одного файла во временном каталоге.
func syntheticConfigPackage(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: синтетика не записана: %v", err)
	}
	return dir
}

// syntheticConfigBase — годный пакет: поле A судит валидатор и читает метод
// через поле-носитель, поле B читает свободная функция через параметр.
const syntheticConfigBase = `package p

type Config struct {
	A int
	B int
	Orphan int
}

type Ceremony struct{ cfg Config }

func New(cfg Config) (*Ceremony, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return &Ceremony{cfg: cfg}, nil
}

func validateConfig(cfg Config) error {
	if cfg.A == 0 || cfg.B == 0 || cfg.Orphan == 0 {
		return errBad
	}
	return nil
}

func (c *Ceremony) run() int { return c.cfg.A + width(c.cfg) }

func width(cfg Config) int { return cfg.B }

var errBad error
`

// TestConfigFieldJudgedButNotReadIsFoundAndItsTwinIsSilent — инъекция в обе
// стороны: дефект и близнец различаются ровно одним читателем поля Orphan.
func TestConfigFieldJudgedButNotReadIsFoundAndItsTwinIsSilent(t *testing.T) {
	defect := scanConfigReaders(syntheticConfigPackage(t, syntheticConfigBase), "Config", "validateConfig")
	if defect.refusal != "" {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s", defect.refusal)
	}
	if got := defect.orphans(); !slices.Equal(got, []string{"Orphan"}) {
		t.Errorf("поле без читателя не найдено: находки %v, ожидалось [Orphan]", got)
	}

	twins := map[string]string{
		"читатель через поле-носитель":       "\nfunc (c *Ceremony) spend() int { return c.cfg.Orphan }\n",
		"читатель через параметр":            "\nfunc spend(cfg *Config) int { return cfg.Orphan }\n",
		"читатель в функциональном литерале": "\nvar spend = func(cfg Config) int { return cfg.Orphan }\n",
	}
	for name, reader := range twins {
		t.Run(name, func(t *testing.T) {
			twin := scanConfigReaders(syntheticConfigPackage(t, syntheticConfigBase+reader), "Config", "validateConfig")
			if twin.refusal != "" {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s", twin.refusal)
			}
			if got := twin.orphans(); len(got) != 0 {
				t.Errorf("законный близнец объявлен находкой: %v", got)
			}
			if len(twin.readers["Orphan"]) != 1 {
				t.Errorf("читатель Orphan не сосчитан: %v", twin.readers["Orphan"])
			}
		})
	}
}

// TestConfigReaderInAFormTheScanDoesNotKnowIsRedNotSilent — чтение через
// локальную копию и затенённое имя разбор читателем не признаёт: поле остаётся
// находкой, а не проходит молча.
func TestConfigReaderInAFormTheScanDoesNotKnowIsRedNotSilent(t *testing.T) {
	forms := map[string]string{
		"локальная копия": "\nfunc (c *Ceremony) spend() int { local := c.cfg; return local.Orphan }\n",
		"затенённое имя":  "\ntype other struct{ Orphan int }\n\nfunc spend(cfg Config) int { { cfg := other{}; return cfg.Orphan } }\n",
	}
	for name, reader := range forms {
		t.Run(name, func(t *testing.T) {
			census := scanConfigReaders(syntheticConfigPackage(t, syntheticConfigBase+reader), "Config", "validateConfig")
			if census.refusal != "" {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s", census.refusal)
			}
			if got := census.orphans(); !slices.Equal(got, []string{"Orphan"}) {
				t.Errorf("форма, которой разбор не знает, прошла за читателя: находки %v", got)
			}
		})
	}
}

// TestConfigReaderScanRefusesWithoutItsPremises — нарушенная предпосылка даёт
// отказ судить, а не зелёный.
func TestConfigReaderScanRefusesWithoutItsPremises(t *testing.T) {
	worlds := map[string]struct {
		src  string
		want string
	}{
		"валидатора нет": {
			src:  strings.Replace(syntheticConfigBase, "func validateConfig(", "func checkConfig(", 1),
			want: "объявлена 0 раз",
		},
		"валидатор отдаёт настройки дальше целиком": {
			src:  strings.Replace(syntheticConfigBase, "\treturn nil\n}\n\nfunc (c", "\treturn deeper(cfg)\n}\n\nfunc deeper(Config) error { return nil }\n\nfunc (c", 1),
			want: "не выборкой поля",
		},
		"имя поля-носителя занято другим типом": {
			src:  syntheticConfigBase + "\ntype strategy struct{ cfg int }\n",
			want: "объявлено и другим типом",
		},
		"типа настроек нет": {
			src:  strings.Replace(syntheticConfigBase, "type Config struct", "type Settings struct", 1),
			want: "объявлен 0 раз",
		},
	}
	for name, w := range worlds {
		t.Run(name, func(t *testing.T) {
			census := scanConfigReaders(syntheticConfigPackage(t, w.src), "Config", "validateConfig")
			if !strings.Contains(census.refusal, w.want) {
				t.Errorf("отказ %q, ожидался содержащий %q", census.refusal, w.want)
			}
		})
	}
}
