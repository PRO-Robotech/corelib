// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package observability_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/observability"
)

// Предмет: витрина `*_build_info` обязана РАЗЛИЧАТЬ «версия такая-то» и «сборка
// величину не проставила». Пустая метка читается как «версии нет», правдоподобное
// `dev` — как имя ветки; оба неотличимы от «величину не измеряли».
//
// Проба парная по каждой оси: одна сторона утверждает подстановку слова, другая —
// что законная величина проходит НЕТРОНУТОЙ. Без второй половины годным выглядел
// бы нормализатор, затирающий всё подряд.

func TestBuildStampUnstampedWordIsSubstitutedForEmpty(t *testing.T) {
	t.Parallel()
	v, c := observability.NormalizeBuildStamp("", "")
	require.Equalf(t, observability.BuildStampUnstamped, v, "пустая версия обязана стать "+
		"СЛОВОМ: пустая метка на витрине неотличима от «величину не измеряли»")
	require.Equal(t, observability.BuildStampUnstamped, c)
}

func TestBuildStampRealValuesPassThroughUntouched(t *testing.T) {
	t.Parallel()
	v, c := observability.NormalizeBuildStamp("release/kaname-tail", "1eedc9e7ff")
	require.Equalf(t, "release/kaname-tail", v, "законный близнец: проставленную величину "+
		"нормализатор трогать не вправе — иначе витрина отвечала бы о себе, а не о сборке")
	require.Equal(t, "1eedc9e7ff", c)
}

func TestBuildStampGoDefaultsAreAlsoUnstamped(t *testing.T) {
	t.Parallel()
	// `dev`/`unknown` — умолчания ОБЪЯВЛЕНИЯ в композиционном корне: их видит
	// сборка без `-ldflags` (`go run`, `go test`, сборка руками). На витрине они
	// такой же не-ответ, как пустая строка, и обязаны читаться одинаково —
	// иначе у одного состояния будет три написания и три разных тревоги.
	v, c := observability.NormalizeBuildStamp("dev", "unknown")
	require.Equal(t, observability.BuildStampUnstamped, v)
	require.Equal(t, observability.BuildStampUnstamped, c)
}

func TestBuildStampWhitespaceIsNotAValue(t *testing.T) {
	t.Parallel()
	// Аргумент сборки, переданный пустым через оболочку, приезжает пробелом, а не
	// пустой строкой: без обрезки витрина показала бы ряд с меткой-пробелом —
	// формально непустой и потому не попавший бы ни под одну тревогу.
	v, _ := observability.NormalizeBuildStamp("  ", "\t")
	require.Equal(t, observability.BuildStampUnstamped, v)
}

func TestBuildStampSurroundingWhitespaceIsTrimmedFromRealValues(t *testing.T) {
	t.Parallel()
	v, c := observability.NormalizeBuildStamp(" main ", " abc123 ")
	require.Equalf(t, "main", v, "величина с краевым пробелом — та же величина: не обрезав "+
		"её, витрина дала бы ДВА ряда об одной сборке, и они не склеились бы")
	require.Equal(t, "abc123", c)
}
