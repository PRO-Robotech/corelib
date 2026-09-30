// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec_test

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

// validator — то, что гейт корпуса судит: чтение каталога шаблонов.
type validator func(fsys fs.FS, root string) (spec.Catalog, spec.Census, error)

// corpusReport — исход гейта: версий, фикстур и находки «фикстура — версия —
// причина».
type corpusReport struct {
	versions, fixtures int
	findings           []string
}

// runCorpus гоняет валидатор по замороженному корпусу corpus/<версия>/<фикстура>/
// — каталогу шаблонов, принятому выпущенной версией формата. Формат только
// расширяется: каждая фикстура каждой версии обязана приниматься.
func runCorpus(t *testing.T, v validator) corpusReport {
	t.Helper()
	var rep corpusReport
	versions, err := os.ReadDir("corpus")
	require.NoError(t, err)
	for _, ver := range versions {
		if !ver.IsDir() {
			continue
		}
		rep.versions++
		fixtures, err := os.ReadDir(path.Join("corpus", ver.Name()))
		require.NoError(t, err)
		for _, fx := range fixtures {
			if !fx.IsDir() {
				continue
			}
			rep.fixtures++
			_, census, err := v(os.DirFS(path.Join("corpus", ver.Name(), fx.Name())), ".")
			switch {
			case err != nil:
				rep.findings = append(rep.findings, fmt.Sprintf("%s/%s: %v", ver.Name(), fx.Name(), err))
			case census.Templates == 0:
				rep.findings = append(rep.findings, fmt.Sprintf("%s/%s: шаблонов 0 — фикстура ничего не держит", ver.Name(), fx.Name()))
			}
		}
	}
	sort.Strings(rep.findings)
	return rep
}

// NTF1-A08: корпус принятых фикстур всех выпущенных версий — принимается.
// Близнец расширяющей правки — текущий валидатор: зелёный с числом фикстур.
func TestA08FrozenCorpusIsAccepted(t *testing.T) {
	rep := runCorpus(t, spec.LoadFS)
	t.Logf("версий формата %d, фикстур %d", rep.versions, rep.fixtures)
	require.Positive(t, rep.versions, "корпус пуст — это не зелёный")
	require.GreaterOrEqual(t, rep.fixtures, 4)
	require.Empty(t, rep.findings)
}

// Инъекция сужающей правки: ранее законный блок warning отвергается — гейт
// красный и называет фикстуру и версию.
func TestA08InjectionNarrowingIsFound(t *testing.T) {
	narrowed := func(fsys fs.FS, root string) (spec.Catalog, spec.Census, error) {
		cat, census, err := spec.LoadFS(fsys, root)
		if err != nil {
			return cat, census, err
		}
		for _, tpl := range cat.Templates {
			for _, b := range tpl.Bodies {
				for _, blk := range b.Blocks {
					if blk.Kind == spec.BlockWarning {
						return spec.Catalog{}, census, spec.Findings{{File: tpl.Dir + "/body." + b.Locale + ".yaml", Block: blk.Index, Rule: spec.RuleBlockKind}}
					}
				}
			}
		}
		return cat, census, nil
	}
	rep := runCorpus(t, narrowed)
	require.Len(t, rep.findings, 1, "сужение задевает ровно фикстуру с блоком warning")
	for _, f := range rep.findings {
		t.Logf("находка: %s", f)
	}
	require.Contains(t, rep.findings[0], "v1/", "находка называет версию")
	require.Contains(t, rep.findings[0], "blocks-all", "находка называет фикстуру")
	require.Contains(t, rep.findings[0], spec.RuleBlockKind, "находка называет причину, а не пустую фикстуру")
}
