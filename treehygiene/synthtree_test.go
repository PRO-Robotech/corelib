// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// synthtree_test.go — синтетическое ОТСЛЕЖИВАЕМОЕ дерево для проб пакета.
package treehygiene_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/gitenv"
	"github.com/PRO-Robotech/corelib/treehygiene"
)

// synthTree — дерево с индексом git во временном каталоге.
//
// Отслеживаемое, а не просто разложенное по диску: область обхода разбор берёт
// у индекса git, и дерево без индекса дало бы «прочитано 0» — то есть зелёное
// по отсутствию предмета вместо вердикта.
//
// Подкаталоги НЕ засеиваются: корни выводятся из индекса, поэтому корни
// синтетики — ровно те, которые написала сама проба. Иначе проба про восьмой
// корень была бы неотличима от пробы про первый.
func synthTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := gitenv.Command(root, args...).CombinedOutput(); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
	}
	run("init", "--quiet", "-b", "main")
	// Дерево обязано быть непустым: обход берёт состав у индекса, и «смотреть
	// не на что» есть отказ, а не успех.
	write("README.md", "# синтетическое дерево пробы\n")
	for rel, body := range files {
		write(rel, body)
	}
	run("add", "-A")
	return root
}

// testCorpusSkip — вид вычитания, которым пробы изображают перечень
// потребителя. Здесь он ФИКСТУРА, а не поставляемое значение: готовых видов
// пакет не раздаёт (см. шапку пакета), и подавать его пробам из прод-кода
// значило бы проверять разбор тем же перечнем, который он сам и объявил.
func testCorpusSkip() treehygiene.DeferralSkip {
	return treehygiene.DeferralSkip{
		Name:  "тестовый корпус",
		Why:   "фикстура гейта обязана уметь написать форму дефекта",
		Match: func(s string) bool { return strings.HasSuffix(s, "_test.go") },
	}
}
