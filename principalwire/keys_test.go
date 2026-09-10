// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package principalwire_test

// keys_test.go — три поверхностные формы одного ключа держатся ВМЕСТЕ, а
// каталог называет РЕАЛЬНЫЕ константы этого пакета.
//
// Обе пробы существуют затем, что сведение объявления в одно место само по себе
// расхождения не запрещает: внутри одного файла заголовок и метаданное можно
// разъехать так же молча, как прежде разъезжались два файла.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/principalwire"
)

// TestKeyFormsAreOneKeyInThreeSurfaces — метаданное есть нижний регистр своего
// заголовка, а заголовок канонический (`http.Header` приводит именно к нему).
func TestKeyFormsAreOneKeyInThreeSurfaces(t *testing.T) {
	keys := principalwire.Keys()
	require.NotEmpty(t, keys, "каталог пуст — проба сказана ни о чём")

	var paired int
	for _, k := range keys {
		if k.Header != "" {
			require.Equal(t, k.Header, http.CanonicalHeaderKey(k.Header),
				"%s: заголовок %q не канонический — http.Header приведёт входящее имя к другому, "+
					"и производитель с потребителем разойдутся на регистре", k.Name, k.Header)
		}
		if k.Meta == "" || k.Header == "" {
			continue
		}
		paired++
		require.Equal(t, strings.ToLower(k.Header), k.Meta,
			"%s: метаданное %q не есть нижний регистр заголовка %q — одно логическое имя "+
				"поехало двумя", k.Name, k.Meta, k.Header)
		require.True(t, strings.HasPrefix(k.Meta, principalwire.Namespace),
			"%s: ключ %q вне пространства имён %q", k.Name, k.Meta, principalwire.Namespace)
	}
	t.Logf("перепись: записей каталога %d, из них с обеими формами %d", len(keys), paired)
	require.NotZero(t, paired, "ни одной записи с обеими формами — сверять было нечего")
}

// TestCatalogueIdentsNameRealConstants — имя константы, записанное в каталоге,
// принадлежит РЕАЛЬНОЙ константе этого пакета и несёт то же значение.
//
// Проба разбирает исходник пакета: соответствие «данные ↔ объявление» иначе
// держалось бы соглашением, а читателя ключа проверки дерева опознают именно по
// имени константы — ошибка в нём дала бы молчание вместо находки.
func TestCatalogueIdentsNameRealConstants(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "keys.go", nil, 0)
	require.NoError(t, err, "исходник пакета не разобран — сверять нечего")

	consts := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != len(vs.Values) {
			return true
		}
		for i, name := range vs.Names {
			lit, ok := vs.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
				consts[name.Name] = v
			}
		}
		return true
	})
	require.NotEmpty(t, consts, "в исходнике не найдено ни одной строковой константы — "+
		"разбор сломан, и «сошлось» значило бы «не прочитано»")

	var checked int
	for _, k := range principalwire.Keys() {
		if k.Ident == "" {
			require.Empty(t, k.Meta, "%s: форма Meta есть, а имени константы нет — "+
				"читателя этого ключа проверки дерева опознать не смогут", k.Name)
			continue
		}
		got, ok := consts[k.Ident]
		require.True(t, ok, "%s: каталог называет константу %q, которой в пакете нет",
			k.Name, k.Ident)
		require.Equal(t, k.Meta, got,
			"%s: константа %q несёт %q, а каталог — %q", k.Name, k.Ident, got, k.Meta)
		checked++
	}
	t.Logf("перепись: строковых констант в исходнике %d, записей каталога с именем константы %d",
		len(consts), checked)
	require.NotZero(t, checked, "ни одна запись каталога не назвала константы — проверять было нечего")
}

// TestIdentityShapeSeesAForeignNamespaceAndSparesNeighbours — форма ключа
// личности опознаётся НЕЗАВИСИМО от пространства имён, а служебная разметка
// соседей по проводу под неё не подпадает.
//
// Отрицания и утверждения стоят парой: признак, отвечающий «да» всему, был бы
// отказом каждому запросу, а отвечающий «нет» всему — молчанием на самом
// рассинхроне.
func TestIdentityShapeSeesAForeignNamespaceAndSparesNeighbours(t *testing.T) {
	shaped := []string{
		"x-kacho-principal-id",
		"x-kaname-principal-id",
		"x-kaname-principal-display-name",
		"x-kaname-token-acr",
	}
	for _, k := range shaped {
		require.True(t, principalwire.IsIdentityShaped(k),
			"%q — форма ключа личности; не опознав её, приёмник прочитал бы пересланную "+
				"личность как её отсутствие", k)
	}
	plain := []string{
		"x-request-id", "x-forwarded-for", "x-envoy-original-path",
		"authorization", "x-kacho-admin", "x-kacho-project-id",
		"x-principal-id", "x-kaname-principal-", "x-kaname-token-",
	}
	for _, k := range plain {
		require.False(t, principalwire.IsIdentityShaped(k),
			"%q формой ключа личности не является — отказ на нём остановил бы законный вызов", k)
	}

	require.True(t, principalwire.IsOurs("x-kacho-principal-id"))
	require.False(t, principalwire.IsOurs("x-kaname-principal-id"))
	require.Equal(t, "x-kacho-principal-id", principalwire.Bare("Grpc-Metadata-X-Kacho-Principal-Id"),
		"мостовая приставка снимается сама: обе поверхностные формы обязаны сводиться к одному имени")
}
