// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Внешний тест-пакет (errors_test, не errors): внутри пакета неэкспортируемые
// поля видны, и проба закрытости, написанная там, утверждала бы обратное тому,
// что проверяет, — она бы КОМПИЛИРОВАЛАСЬ. Закрытость есть свойство ГРАНИЦЫ
// пакета, поэтому и проверяется только снаружи.
package errors_test

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	kerrors "github.com/PRO-Robotech/corelib/errors"
)

// Гейт «компилятор — на шестом токене».
//
// Что он утверждает: полосу с произвольным токеном НЕЛЬЗЯ собрать за пределами
// pkg/errors. Это и есть механизм закрытости словаря — не соглашение и не
// проверка в рантайме, а отказ сборки.
//
// Доказан инъекцией в ОБЕ стороны, и обе стороны исполняются здесь:
//   - отрицательная: попытка собрать шестой токен обязана НЕ компилироваться, и
//     сообщение обязано называть причину закрытости, а не постороннюю опечатку;
//   - положительная: законное использование той же формы (значение словаря +
//     конструктор) обязано компилироваться. Без неё гейт зеленел бы и на
//     пакете, который не компилируется вовсе.
//
// Предпосылка гейта проверяется отдельно: нет тулчейна — гейт говорит это вслух,
// а не выдаёт «ноль находок» за «запрет держится».
func TestSixthTokenDoesNotCompileOutsideThePackage(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("предпосылка гейта не выполнена: тулчейн go недоступен (%v)", err)
	}
	root := moduleRoot(t)
	probeImport := probeImportPath(t)

	cases := []struct {
		name        string
		body        string
		wantCompile bool
		wantMsg     string
	}{
		{
			name: "инъекция: шестой токен литералом структуры",
			body: `_ = kerrors.Reason{token: "SIXTH_TOKEN", code: codes.Internal}`,
			// Текст ЗАХВАЧЕН у компилятора, а не угадан: первая редакция ждала
			// «unknown field» и покраснела — go говорит «cannot refer to
			// unexported field». Гейт, ждущий выдуманного сообщения, зеленел бы
			// на любой посторонней ошибке сборки.
			wantMsg: "cannot refer to unexported field",
		},
		{
			name:    "инъекция: приведение строки к полосе",
			body:    `_ = kerrors.Reason("SIXTH_TOKEN")`,
			wantMsg: "cannot convert",
		},
		{
			name:        "законный близнец: значение словаря + конструктор",
			body:        `_, _ = kerrors.ReasonPeerResourceMissing.Errf(kerrors.PeerRef{Service: "vpc"}, "x"), codes.Internal`,
			wantCompile: true,
		},
	}

	var injections, twins int
	for _, tc := range cases {
		if tc.wantCompile {
			twins++
		} else {
			injections++
		}
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := "package main\n\nimport (\n\t\"google.golang.org/grpc/codes\"\n\n\tkerrors \"" +
				probeImport + "\"\n)\n\nfunc main() {\n\t" + tc.body + "\n}\n"
			probe := filepath.Join(dir, "probe.go")
			require.NoError(t, os.WriteFile(probe, []byte(src), 0o600))

			cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "out"), probe)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()

			if tc.wantCompile {
				require.NoErrorf(t, err, "законная форма обязана компилироваться, вывод:\n%s", out)
				return
			}
			require.Errorf(t, err, "шестой токен собрался — словарь НЕ закрыт; вывод:\n%s", out)
			require.Containsf(t, string(out), tc.wantMsg,
				"сборка упала не по той причине — гейт обязан ловить закрытость, а не опечатку; вывод:\n%s", out)
		})
	}

	// Объём осмотренного: «ноль находок» обязано быть отличимо от «ноль
	// прочитанного» (testing.md §Гейт на класс, п.3).
	t.Logf("проб компиляции осмотрено: %d (инъекций %d, законных близнецов %d); "+
		"путь импорта пакета выведен как %s",
		len(cases), injections, twins, probeImport)
}

// probeImportPath — путь импорта пакета, о котором говорит эта проба. Берётся у
// САМОГО пакета через его тип, а не выписывается литералом.
//
// Литерал был бы координатой ОДНОЙ раскладки. В монорепо путь начинается с
// модуля платформы; в отдельном репозитории фундамента у того же пакета путь
// другой, и синтетическая программа перестала бы собираться ВОВСЕ — а гейт
// закрытости словаря объявил бы это подтверждением запрета, потому что сборка
// упала. От этого стоит сверка с ЗАХВАЧЕННЫМ текстом компилятора — она и поймала
// подмену причины; но чинить надо не проверку, а происхождение пути.
//
// Соседняя функция moduleRoot уже держала это правило для КОРНЯ («путь к пакету
// в дереве не выписывается»), а путь импорта строкой рядом ему противоречил:
// два места об одном предмете, из которых верно было одно.
func probeImportPath(t *testing.T) string {
	t.Helper()
	p := reflect.TypeOf(kerrors.Reason{}).PkgPath()
	require.NotEmpty(t, p, "путь импорта пакета не выведен — синтетической программе нечего импортировать")
	require.Equal(t, "errors", path.Base(p),
		"выведен путь не того пакета (%s) — проба собрала бы программу про чужой словарь", p)
	return p
}

// moduleRoot — корень модуля, от которого исполняется проба сборки. Ищется по
// go.mod вверх от рабочего каталога: путь к пакету в дереве не выписывается,
// иначе проба сломалась бы от переноса каталога, и выглядело бы это как
// «запрет снят».
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("предпосылка гейта не выполнена: go.mod не найден вверх от %s", dir)
		}
		dir = parent
	}
}

// Словарь, прочитанный СНАРУЖИ пакета, — то есть ровно та поверхность, которую
// видит сервис и по которой ключуется клиент.
func TestDictionaryTokensAreTheContractFive(t *testing.T) {
	got := map[string]bool{}
	for _, r := range kerrors.AllReasons() {
		got[r.Token()] = true
	}
	for _, want := range []string{
		"INVALID_RESOURCE_ID", "RESOURCE_NOT_FOUND",
		"PEER_RESOURCE_MISSING", "PEER_RESOURCE_STATE", "PEER_UNAVAILABLE",
	} {
		require.Truef(t, got[want], "полоса %s пропала из словаря", want)
	}
	require.Len(t, got, 5, "словарь полос вырос или сжался — это правка контракта")
	t.Logf("полос словаря осмотрено: %d", len(got))
}
