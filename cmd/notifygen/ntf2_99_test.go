// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// ntf2_99_test.go — два правила генератора, которых требует NTF2-99 (приёмка
// NTF-2 kacho#2917 @d06cdcd2, Р3, NTF2-99; отчёт полосы RED D-F4F5 kaname,
// вопросы 1 и 2). Правила — целевые правила формата и генератора, а не
// подгонка приёмки:
//
//  1. Перечень обязательного класса лежит в каталоге шаблонов владельца:
//     <owner>/notifications/required-security.yaml рядом с каталогами
//     <owner>/notifications/<шаблон>/ — обе координаты приёмки
//     («notifications/required-security.yaml», «notifications/*/notification.yaml»)
//     в одном каталоге. Набор файлов каталога закрыт и расширяется одним
//     именем: required-security.yaml — не «файл вне раскладки шаблона».
//     Форма файла — последовательность YAML имён шаблонов; иная форма —
//     находка с именем файла. Смысл перечня (класс security, шаблон есть,
//     перечень непуст) судит гейт владельца (NTF2-99 (а), (в), (г)).
//  2. У класса security отписки нет (Д9; NTF2-99 (б)). Ссылка отписки в теле
//     — блок unsubscribe: формат знает этот вид блока только затем, чтобы
//     назвать нарушение, а не отвергать его общим «блок вне закрытого
//     набора». notifygen -check печатает число ссылок отписки; на дереве без
//     них — «ссылок отписки 0».
//
// Порядок каждой пробы несущий: сначала фикстура (дерево порождается и
// -check зелёный), затем проба возможности.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// requireGreenTree — посылка: дерево newTree порождается и сверяется зелёным.
func requireGreenTree(t *testing.T, tr *tree) result {
	t.Helper()
	tr.generate()
	r := tr.run("-check")
	require.Equal(t, 0, r.code, "ФИКСТУРА: -check на исходном дереве не зелёный: %s%s", r.stdout, r.stderr)
	return r
}

// NTF2-99, правило 1: перечень required-security.yaml в каталоге шаблонов
// владельца принят генератором. Близнец — то же дерево с файлом иного имени:
// набор файлов каталога закрыт, находка «файл вне раскладки шаблона»; факт
// один — имя файла.
func TestNTF2_99_RequiredSecurityListIsACatalogFile(t *testing.T) {
	tr := newTree(t)
	requireGreenTree(t, tr)

	tr.write("svc/notifications/required-securty.yaml", "- invite\n")
	r := tr.run("-check")
	require.Equal(t, 1, r.code, "близнец: файл иного имени в каталоге — находка: %s%s", r.stdout, r.stderr)
	require.Contains(t, r.stderr, "svc/notifications/required-securty.yaml")
	require.Contains(t, r.stderr, "файл вне раскладки шаблона")

	tr = newTree(t)
	requireGreenTree(t, tr)
	tr.write("svc/notifications/required-security.yaml", "- invite\n")
	g := tr.run()
	require.Equal(t, 0, g.code, "генерация с перечнем в каталоге: %s%s", g.stdout, g.stderr)
	r = tr.run("-check")
	require.Equal(t, 0, r.code, "-check с перечнем в каталоге: %s%s", r.stdout, r.stderr)
}

// NTF2-99, правило 1: форма перечня — последовательность имён. Перечень
// картой — находка с именем файла и не «файл вне раскладки» (файл узнан).
// Близнец — TestNTF2_99_RequiredSecurityListIsACatalogFile.
func TestNTF2_99_RequiredSecurityListFormIsChecked(t *testing.T) {
	tr := newTree(t)
	requireGreenTree(t, tr)
	tr.write("svc/notifications/required-security.yaml", "invite: true\n")
	r := tr.run("-check")
	require.Equal(t, 1, r.code, "перечень картой: %s%s", r.stdout, r.stderr)
	require.Contains(t, r.stderr, "svc/notifications/required-security.yaml")
	require.NotContains(t, r.stderr, "файл вне раскладки шаблона", "файл перечня не узнан")
}

// NTF2-99 (б): блок ссылки отписки в теле шаблона класса security —
// находка «отписка в классе security» с файлом и шаблоном, не общее «блок вне
// закрытого набора». Близнец — то же дерево без блока: -check зелёный и
// печатает «ссылок отписки 0»; факт один — блок в теле одной локали.
func TestNTF2_99b_UnsubscribeLinkInSecurityClassIsAFinding(t *testing.T) {
	tr := newTree(t)
	twin := requireGreenTree(t, tr)
	require.Contains(t, twin.stdout, "ссылок отписки 0", "близнец: -check печатает число ссылок отписки")

	body := tr.read("svc/notifications/invite/body.ru.yaml")
	tr.write("svc/notifications/invite/body.ru.yaml", strings.TrimRight(body, "\n")+"\n  - unsubscribe: \"Отписаться\"\n")
	r := tr.run("-check")
	require.Equal(t, 1, r.code, "отписка в классе security: %s%s", r.stdout, r.stderr)
	require.Contains(t, r.stderr, "svc/notifications/invite/body.ru.yaml")
	require.Contains(t, r.stderr, "отписка в классе security")
	require.NotContains(t, r.stderr, "блок вне закрытого набора", "нарушение не названо: общий отказ вида блока")
}
