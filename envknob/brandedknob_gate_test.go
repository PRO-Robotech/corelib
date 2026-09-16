// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// brandedknob_gate_test.go — сам гейт: в прод-коде фундамента имя ручки с
// приставкой платформы остаётся ТОЛЬКО объявленным окном перехода.
//
// Корень обхода — корень МОДУЛЯ, и это не мелочь: проба Go исполняется с
// рабочим каталогом пакета, поэтому `..` — модуль, а `../..` — то, что лежит
// рядом с рабочей копией. Вердикт, снятый за пределами модуля, есть свойство
// соседства, а не коммита (`PRO-Robotech/corelib#3`).
package envknob_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/envknob"
	"github.com/PRO-Robotech/corelib/treecorpus"
)

// moduleRoot — корень модуля относительно каталога этого пакета.
const moduleRoot = ".."

func TestFoundationKnobsCarryNoPlatformBrandOutsideTheWindow(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(moduleRoot)
	if err != nil {
		t.Fatalf("корень модуля не разрешён: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(root, "go.mod")); serr != nil {
		t.Fatalf("корень обхода %q не несёт go.mod — обход вышел за модуль и судил бы "+
			"то, что лежит рядом с рабочей копией", root)
	}

	files, err := treecorpus.UnderWithSuffix(root, ".go")
	if err != nil {
		t.Fatalf("состав дерева: %v — гейт не может назвать дерево, о котором говорит", err)
	}

	var (
		parsed, literals int
		all              []envknob.BrandedKnob
	)
	for _, abs := range files {
		rel, rerr := filepath.Rel(root, abs)
		if rerr != nil || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		src, serr := os.ReadFile(abs) // #nosec G304 -- путь из индекса git под корнем модуля
		if serr != nil {
			t.Fatalf("файл %s не прочитан: %v — обход неполон", rel, serr)
		}
		found, lits, perr := envknob.ScanBrandedKnobs(rel, src)
		if perr != nil {
			continue // не-Go по расширению не бывает; неразбираемое — предмет компилятора
		}
		parsed++
		literals += lits
		all = append(all, found...)
	}

	inWindow := 0
	for _, k := range all {
		if k.InWindow {
			inWindow++
		}
	}

	t.Logf("перепись: непроверочных файлов Go разобрано %d; строковых литералов осмотрено %d; "+
		"с приставкой %q — %d, из них объявлены окном перехода %d",
		parsed, literals, envknob.BrandedPrefix, len(all), inWindow)

	if parsed == 0 || literals == 0 {
		t.Fatalf("обход пуст (файлов %d, литералов %d) — «ноль находок» здесь означало бы "+
			"«ноль прочитанного»", parsed, literals)
	}

	// ПРЕДПОСЫЛКА НАЗВАНА СОДЕРЖИМЫМ: окно сегодня непусто, и гейт обязан это
	// видеть. Сползёт перепись к нулю по обеим величинам — и «находок нет» станет
	// неотличимо от «распознаватель перестал называть свой предмет».
	if len(all) == 0 {
		t.Log("ПРЕДМЕТА БОЛЬШЕ НЕТ: ни одного имени с приставкой платформы. Это ЦЕЛЬ " +
			"перехода, а не поломка — окно можно закрывать: снимите константы Legacy… " +
			"и этот абзац")
	}

	if bad := envknob.UnwindowedBrandedKnobs(all); len(bad) > 0 {
		var b strings.Builder
		names := make([]string, 0, len(bad))
		for _, k := range bad {
			names = append(names, k.Name)
			b.WriteString("\n  " + k.File + ":" + itoa(k.Line) + " — " + k.Name)
			if k.Const == "" {
				b.WriteString(" (не константа вовсе)")
			} else {
				b.WriteString(" (константа " + k.Const + ")")
			}
		}
		sort.Strings(names)
		t.Errorf("%d имён ручек с приставкой платформы вне окна перехода:%s\n\n"+
			"Фундамент читают ОБА продукта, и оператор второго ставит его БЕЗ платформы: "+
			"приставку выбирает не фундамент. Исходов два — нейтральное имя с окном "+
			"(константа Legacy… рядом, см. envknob) либо снятие ручки вместе с её предметом.",
			len(bad), b.String())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
