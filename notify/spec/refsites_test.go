// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec_test

import (
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

// siteRow — строка таблицы пробы мест ссылок: блок вида, ссылающийся на
// атрибут optional в месте ссылки этого вида (yaml — без when), и место,
// которое обязана вернуть RefSites. У divider мест нет: yaml без ссылки,
// place пуст.
type siteRow struct {
	yaml  string
	place string
}

// siteTable — таблица по видам блока. Перечень видов проба берёт из
// spec.BlockKinds(), а не отсюда: вид без строки — находка (заказ CX1-51).
var siteTable = map[spec.BlockKind]siteRow{
	spec.BlockHeading: {`heading: "Для {{ opt_text }}"`, "text"},
	spec.BlockP:       {`p: "Для {{ opt_text }}"`, "text"},
	spec.BlockWarning: {`warning: "Для {{ opt_text }}"`, "text"},
	spec.BlockCode:    {`code: "{{ opt_text }}"`, "text"},
	spec.BlockButton:  {`button: {text: "Открыть", path: opt_path}`, "path"},
	spec.BlockList:    {`list: ["первый", "для {{ opt_text }}"]`, "items[2]"},
	spec.BlockKV:      {`kv: [{key: "Кто", value: "всё"}, {key: "Для", value: "{{ opt_text }}"}]`, "pairs[2].value"},
	spec.BlockDivider: {`divider: {}`, ""},
}

func siteCatalog(block string, when bool) fstest.MapFS {
	whenLine := ""
	if when {
		attr := "opt_text"
		if len(block) > 6 && block[:6] == "button" {
			attr = "opt_path"
		}
		whenLine = "\n    when: " + attr
	}
	return fstest.MapFS{
		"probe-site/notification.yaml": {Data: []byte(`name: probe-site
class: notice
ttl: 1h
attributes:
  opt_text: {type: text, presence: optional}
  opt_path: {type: path, presence: optional}
subject:
  ru: "Проба"
  en: "Probe"
`)},
		"probe-site/body.ru.yaml": {Data: []byte("blocks:\n  - " + block + whenLine + "\n")},
		// Локаль en — нейтральный блок без мест ссылок: предмет пробы — блок
		// тела ru, и его находки не удваиваются второй локалью набора.
		"probe-site/body.en.yaml": {Data: []byte("blocks:\n  - p: \"Probe\"\n")},
	}
}

// onlyBlock — единственный блок тела ru каталога пробы (тело en несёт
// нейтральный блок).
func onlyBlock(cat spec.Catalog) (spec.Block, bool) {
	if len(cat.Templates) != 1 {
		return spec.Block{}, false
	}
	for _, b := range cat.Templates[0].Bodies {
		if b.Locale == "ru" && len(b.Blocks) == 1 {
			return b.Blocks[0], true
		}
	}
	return spec.Block{}, false
}

// siteFindings — находки пробы по одному набору видов: вид без строки
// таблицы, место, которого RefSites не вернула, ссылка optional без when, не
// замеченная валидатором, и близнец с when, давший находку.
func siteFindings(t *testing.T, kinds []spec.BlockKind, table map[spec.BlockKind]siteRow) []string {
	t.Helper()
	var out []string
	for _, k := range kinds {
		row, ok := table[k]
		if !ok {
			out = append(out, fmt.Sprintf("%s: вида нет в таблице пробы мест ссылок", k))
			continue
		}
		cat, _, err := spec.LoadFS(siteCatalog(row.yaml, false), ".")
		var sites []spec.RefSite
		if row.place == "" {
			if err != nil {
				out = append(out, fmt.Sprintf("%s: блок без мест ссылок дал находку: %v", k, err))
				continue
			}
			blk, ok := onlyBlock(cat)
			if !ok {
				out = append(out, fmt.Sprintf("%s: каталог пробы прочитан без блока", k))
				continue
			}
			sites = spec.RefSites(blk)
			if len(sites) != 0 {
				out = append(out, fmt.Sprintf("%s: у вида без мест ссылок RefSites вернула %d", k, len(sites)))
			}
			continue
		}
		fs, _ := err.(spec.Findings)
		if len(fs) != 1 || fs[0].Rule != spec.RuleOptionalNeedsWhen {
			out = append(out, fmt.Sprintf("%s: optional без when в месте %s — ожидалась одна находка %q, получено %v", k, row.place, spec.RuleOptionalNeedsWhen, err))
		}
		twin, _, err := spec.LoadFS(siteCatalog(row.yaml, true), ".")
		if err != nil {
			out = append(out, fmt.Sprintf("%s: близнец с when дал находку: %v", k, err))
			continue
		}
		blk, ok := onlyBlock(twin)
		if !ok {
			out = append(out, fmt.Sprintf("%s: близнец прочитан без блока", k))
			continue
		}
		found := false
		for _, s := range spec.RefSites(blk) {
			found = found || (s.Place == row.place && len(s.Refs()) == 1)
		}
		if !found {
			out = append(out, fmt.Sprintf("%s: RefSites не вернула место %s со ссылкой", k, row.place))
		}
	}
	return out
}

// УК59, УК66, CX1-51: места ссылок — одна функция; проба по каждому виду из
// spec.BlockKinds().
func TestRefSitesCoverEveryBlockKind(t *testing.T) {
	kinds := spec.BlockKinds()
	require.Len(t, kinds, 8, "видов блока восемь")
	require.Empty(t, siteFindings(t, kinds, siteTable))
	t.Logf("видов блока сверено: %d", len(kinds))
}

// Инъекция: вид блока без строки таблицы делает пробу красной и называет вид.
func TestRefSitesInjectionKindWithoutRowIsNamed(t *testing.T) {
	table := map[spec.BlockKind]siteRow{}
	for k, v := range siteTable {
		if k != spec.BlockKV {
			table[k] = v
		}
	}
	out := siteFindings(t, spec.BlockKinds(), table)
	require.Len(t, out, 1)
	require.Contains(t, out[0], "kv")
}

// Две формы кнопки дают разные места: без token — path (атрибут), с token —
// token (атрибут), литерал пути местом ссылки не является.
func TestRefSitesButtonForms(t *testing.T) {
	sites := spec.RefSites(spec.Block{Kind: spec.BlockButton, Button: spec.Button{Text: "x", Path: "target"}})
	require.Len(t, sites, 1)
	require.Equal(t, "path", sites[0].Place)
	require.Equal(t, spec.SiteAttr, sites[0].Form)
	require.Equal(t, []string{"target"}, sites[0].Refs())

	sites = spec.RefSites(spec.Block{Kind: spec.BlockButton, Button: spec.Button{Text: "x", Path: "/a", Token: "token"}})
	require.Len(t, sites, 1)
	require.Equal(t, "token", sites[0].Place)
	require.Equal(t, []string{"token"}, sites[0].Refs())

	sites = spec.RefSites(spec.Block{Kind: spec.BlockP, Text: "{{ a }} и {{b}} и {{ a }}"})
	require.Len(t, sites, 1)
	require.Equal(t, spec.SiteText, sites[0].Form)
	require.Equal(t, []string{"a", "b", "a"}, sites[0].Refs())
}
