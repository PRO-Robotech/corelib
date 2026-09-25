// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// withdrawn_external_test.go — посадка `external` снята со словаря (corelib#30).
//
// Три свойства, и все держатся здесь:
//
//   - ЗНАЧЕНИЯ в словаре больше нет: разбор отвергает его с именем настройки,
//     перечни его не производят. Каждое отрицание стоит в паре с живым
//     контролем — законный близнец `own` отличается ровно одним фактом
//     (значением) и разбирается, иначе «отвергнуто» неотличимо от «разбор
//     отвергает всё»;
//   - значение ДОСТИЖИМО мимо Parse — числом, которое декодер кладёт прямо в
//     целое поле, — и проверка старта Validate отвергает его наравне с любым
//     другим числом вне словаря, отдельным от «не объявлено» текстом;
//   - ИМЯ осталось: `External` — устаревшая константа прежнего типа и прежнего
//     значения, с абзацем `Deprecated:`. Путь модуля без суффикса мажорной
//     версии, и снятое экспортированное имя в минорном выпуске v1 ломало бы
//     сборку потребителя, поднявшего пин.
package identityposture_test

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/go-viper/mapstructure/v2"

	"github.com/PRO-Robotech/corelib/identityposture"
)

// setting — имя ручки вызывающего. Нарочно не FieldName: отказ обязан называть
// ту ручку, которую ему передали, а не каноническое имя поля.
const setting = "identity.provider-under-test"

// TestWithdrawnExternalIsRefusedAndOwnParses — разбор: `external` отвергается,
// законный близнец `own` разбирается.
func TestWithdrawnExternalIsRefusedAndOwnParses(t *testing.T) {
	got, err := identityposture.Parse(setting, "own")
	if err != nil || got != identityposture.Own {
		t.Fatalf("законный близнец: Parse(%q) = %v, %v; want own, nil — разбор сломан целиком, отказ ниже ничего бы не значил",
			"own", got, err)
	}

	got, err = identityposture.Parse(setting, "external")
	if err == nil {
		t.Fatalf("Parse(%q) = %v, nil — снятая посадка разобрана как законная", "external", got)
	}
	if got != identityposture.Unset {
		t.Errorf("Parse(%q) вернул %v вместе с отказом; want Unset — ответа нет, а не молча выбранная полоса",
			"external", got)
	}
	if !strings.Contains(err.Error(), setting) {
		t.Errorf("отказ не называет настройку %q: %v", setting, err)
	}
	wantAllowed := "(allowed, verbatim: own)"
	if !strings.Contains(err.Error(), wantAllowed) {
		t.Errorf("отказ не называет законный перечень %s: %v", wantAllowed, err)
	}
}

// TestWithdrawnExternalIsRefusedByJSON — второй вход того же разбора.
func TestWithdrawnExternalIsRefusedByJSON(t *testing.T) {
	var twin identityposture.Provider
	if err := json.Unmarshal([]byte(`"own"`), &twin); err != nil || twin != identityposture.Own {
		t.Fatalf("законный близнец: json %q → %v, %v; want own, nil", "own", twin, err)
	}

	var p identityposture.Provider
	err := json.Unmarshal([]byte(`"external"`), &p)
	if err == nil {
		t.Fatalf("json %q → %v, nil — снятая посадка разобрана как законная", "external", p)
	}
	if p != identityposture.Unset {
		t.Errorf("json %q записал %v в приёмник вместе с отказом; want Unset", "external", p)
	}
	if !strings.Contains(err.Error(), identityposture.FieldName) {
		t.Errorf("отказ не называет поле %q: %v", identityposture.FieldName, err)
	}
}

// TestDictionaryListsOnlyOwn — перечни выводятся из словаря и `external` не
// производят. Равенство целиком: оно и отрицание, и живой контроль сразу —
// пустой перечень его не проходит.
func TestDictionaryListsOnlyOwn(t *testing.T) {
	if got, want := identityposture.Names(), []string{"own"}; !slices.Equal(got, want) {
		t.Errorf("Names() = %q; want %q", got, want)
	}
	if got, want := identityposture.Values(), []identityposture.Provider{identityposture.Own}; !slices.Equal(got, want) {
		t.Errorf("Values() = %v; want %v", got, want)
	}
}

// TestStartCheckRefusesEveryValueOutsideTheDictionary — проверка старта
// различает ТРИ исхода: законное значение принято; поле не объявлено — отказ
// NotDeclared; объявлено числом вне словаря — отдельный отказ, называющий
// ручку, число и законный перечень. Снятая посадка идёт третьим исходом наравне
// с любым другим числом вне словаря: «объявлено» не значит «законно».
func TestStartCheckRefusesEveryValueOutsideTheDictionary(t *testing.T) {
	if err := identityposture.Own.Validate(setting); err != nil {
		t.Fatalf("законный близнец: Own.Validate = %v; want nil — проверка отвергает всё, отказы ниже ничего бы не значили", err)
	}
	if !identityposture.Own.IsLegal() {
		t.Fatal("законный близнец: Own.IsLegal() = false")
	}

	err := identityposture.Unset.Validate(setting)
	if err == nil || err.Error() != identityposture.NotDeclared(setting).Error() {
		t.Errorf("Unset.Validate = %v; want ровно NotDeclared(%q) — незаданное поле отвергается тем же текстом, что прежде",
			err, setting)
	}
	if identityposture.Unset.IsLegal() {
		t.Error("Unset.IsLegal() = true — «не задано» законным значением не является")
	}

	for _, p := range []identityposture.Provider{identityposture.External, 7, -1} {
		if !p.IsSet() {
			t.Errorf("Provider(%d).IsSet() = false — поле объявлено, пусть и вне словаря", int(p))
		}
		if p.IsLegal() {
			t.Errorf("Provider(%d).IsLegal() = true — число вне словаря объявлено законным", int(p))
		}
		err := p.Validate(setting)
		if err == nil {
			t.Errorf("Provider(%d).Validate = nil — значение вне словаря прошло проверку старта", int(p))
			continue
		}
		want := fmt.Sprintf("%s=%d is outside the dictionary (allowed, verbatim: own)", setting, int(p))
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Provider(%d).Validate = %q; want начинающийся с %q", int(p), err, want)
		}
		if err.Error() == identityposture.NotDeclared(setting).Error() {
			t.Errorf("Provider(%d).Validate отвечает «не объявлено» — оператор правил бы не то", int(p))
		}
	}
}

// TestANumberDecodedPastParseReachesTheFieldAndIsRefusedAtStart — путь мимо
// Parse существует: декодер настройки, кладущий число прямо в целое поле, Parse
// не зовёт, и снятое значение доезжает до поля. Поэтому «разбор её не
// производит» не значит «значения не бывает», и отказ ему обязан дать проверка
// старта. Законный близнец отличается одним фактом — числом законного
// значения — и проверку проходит.
func TestANumberDecodedPastParseReachesTheFieldAndIsRefusedAtStart(t *testing.T) {
	decode := func(t *testing.T, n int) identityposture.Provider {
		t.Helper()
		var cfg struct {
			Provider identityposture.Provider `mapstructure:"identity-provider"`
		}
		if err := mapstructure.Decode(map[string]any{identityposture.FieldName: n}, &cfg); err != nil {
			t.Fatalf("NOT EXECUTED: декодер отверг число %d сам (%v) — пути мимо Parse в этой форме нет, "+
				"проба ничего не утверждает", n, err)
		}
		return cfg.Provider
	}

	twin := decode(t, int(identityposture.Own))
	if twin != identityposture.Own || twin.Validate(setting) != nil {
		t.Fatalf("законный близнец: число %d → %v, Validate = %v; want own, nil",
			int(identityposture.Own), twin, twin.Validate(setting))
	}

	got := decode(t, int(identityposture.External))
	if got != identityposture.External {
		t.Fatalf("число %d → %v; want External — декодер перестал класть число прямо, шапка пакета устарела",
			int(identityposture.External), got)
	}
	if !got.IsSet() {
		t.Error("снятое значение, доехавшее мимо Parse, IsSet считает необъявленным — отказ назвал бы не то")
	}
	if err := got.Validate(setting); err == nil {
		t.Error("снятое значение, доехавшее мимо Parse, прошло проверку старта")
	}
}

// Прежний тип — две строки, потому что форм отступления две. Присваивание не
// скомпилируется, если External объявят константой другого именованного типа,
// но нетипизированную константу пропустит: она присваивается любому целому
// типу. Её не пропустит взятие метода: у нетипизированной константы методов
// нет, а у константы Provider — есть.
var (
	_ identityposture.Provider = identityposture.External
	_                          = identityposture.External.IsSet
)

// TestExternalKeepsItsV1Value — имя сохраняет значение выпусков v1.9.0 и
// v1.10.0-rc.2 и не совпадает ни с одним другим: псевдоним `Unset` сделал бы
// дублем ветку `case Unset` у потребителя, псевдоним `Own` молча перевёл бы
// его ветку «адреса поставщика обязательны» на свою посадку.
func TestExternalKeepsItsV1Value(t *testing.T) {
	if got := int(identityposture.External); got != 1 {
		t.Errorf("int(External) = %d; want 1 — значение константы в v1 не меняется", got)
	}
	if identityposture.External == identityposture.Unset || identityposture.External == identityposture.Own {
		t.Errorf("External совпадает с другим значением (%d)", int(identityposture.External))
	}
	if s := identityposture.External.String(); slices.Contains(identityposture.Names(), s) {
		t.Errorf("External печатается законным именем %q — вне словаря значение не должно выглядеть значением", s)
	}
}

// deprecationDefect судит РАЗОБРАННЫЙ исходник: константа name объявлена, и её
// документация несёт абзац, НАЧИНАЮЩИЙСЯ с "Deprecated: " — ту форму, которую
// читают go doc, gopls и staticcheck. Законных мест у документации константы
// два: у спецификации внутри группы и у объявления из одной спецификации.
// Пустая строка — дефекта нет.
func deprecationDefect(t *testing.T, filename string, src []byte, name string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("NOT EXECUTED: %s не разобран: %v", filename, err)
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || !slices.ContainsFunc(vs.Names, func(n *ast.Ident) bool { return n.Name == name }) {
				continue
			}
			if gd.Tok != token.CONST {
				return name + " объявлено через " + gd.Tok.String() + ", а не константой"
			}
			doc := vs.Doc
			if doc == nil && !gd.Lparen.IsValid() {
				doc = gd.Doc
			}
			for _, para := range strings.Split(doc.Text(), "\n\n") {
				if strings.HasPrefix(para, "Deprecated: ") {
					return ""
				}
			}
			return name + ": ни один абзац документации не начинается с \"Deprecated: \""
		}
	}
	return name + " не объявлено"
}

// TestExternalIsDeclaredDeprecated — сам пакет.
func TestExternalIsDeclaredDeprecated(t *testing.T) {
	src, err := os.ReadFile("provider.go")
	if err != nil {
		t.Fatalf("NOT EXECUTED: %v", err)
	}
	if d := deprecationDefect(t, "provider.go", src, "External"); d != "" {
		t.Error(d)
	}
	t.Logf("перепись: файлов разобрано 1 (provider.go, %d байт); устаревших имён судится 1 (External)", len(src))
}

// TestDeprecationDefectIsFoundAndItsTwinIsSilent — инъекция в обе стороны на
// синтетике; каждый дефект отличается от близнеца одним фактом. Синтетическое
// имя — Legacy, а не External: ни одна строка этого файла не читается как
// объявление настоящего имени.
func TestDeprecationDefectIsFoundAndItsTwinIsSilent(t *testing.T) {
	const para = "\t//\n\t// Deprecated: withdrawn.\n"
	const twin = "package p\n\ntype P int\n\nconst (\n\tUnset P = iota\n\t// Legacy — прежнее значение.\n" + para + "\tLegacy\n\tCurrent\n)\n"
	const single = "package p\n\n// Legacy — прежнее значение.\n//\n// Deprecated: withdrawn.\nconst Legacy = 1\n"
	lawful := []struct{ name, src string }{
		{"группа, абзац у спецификации", twin},
		{"объявление из одной спецификации, абзац у объявления", single},
	}
	for _, w := range lawful {
		if d := deprecationDefect(t, "p.go", []byte(w.src), "Legacy"); d != "" {
			t.Errorf("законная форма «%s» объявлена дефектом: %s", w.name, d)
		}
	}
	defects := []struct{ name, src, want string }{
		{"абзаца нет",
			strings.Replace(twin, para, "", 1), "ни один абзац"},
		{"слово Deprecated внутри абзаца, а не в его начале",
			strings.Replace(twin, para, "\t// Deprecated: withdrawn.\n", 1), "ни один абзац"},
		{"абзац у группы, а не у спецификации",
			strings.Replace(strings.Replace(twin, para, "", 1), "const (", "// Deprecated: withdrawn.\nconst (", 1), "ни один абзац"},
		{"переменная вместо константы",
			strings.Replace(single, "const Legacy", "var Legacy", 1), "а не константой"},
		{"имени нет",
			strings.Replace(strings.Replace(twin, para, "", 1), "\t// Legacy — прежнее значение.\n\tLegacy\n", "", 1), "не объявлено"},
	}
	for _, w := range defects {
		t.Run(w.name, func(t *testing.T) {
			if d := deprecationDefect(t, "p.go", []byte(w.src), "Legacy"); !strings.Contains(d, w.want) {
				t.Errorf("дефект = %q; want содержащий %q", d, w.want)
			}
		})
	}
}
