// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// searchpath_test.go — приведение схемы дописывается ТЕМ, КТО ВЫДАЁТ БАЗУ.
//
// # Предмет
//
// Схема у каждого сервиса своя (`kaname`, `kacho_vpc`, …), а DSN, который
// отдаёт `NewDB`, объявлял только базу. Всякий, кто писал запрос без имени
// схемы, обязан был приписать приведение сам — и приписывал: 29 файлов в 25
// пакетах, каждый своей копией.
//
// Цена не в трёх строках, а в том, КАК ВЫГЛЯДИТ ИХ ОТСУТСТВИЕ: запрос уходит в
// `public`, сервер отвечает `relation "roles" does not exist`, и отказ
// неотличим ни от непринятых миграций, ни от неверного имени таблицы в
// продукте. Пропуск наказывается сообщением, посылающим читателя не туда.
//
// Предмет принадлежит выдающему базу: кто её выдал, тот и знает, что у неё за
// схема. Пакет объявляет приведение ОДИН раз — в `Config`, рядом с `Migrate`,
// который эту схему и создаёт.
package pgtest

import (
	"net/url"
	"strings"
	"testing"
)

func TestWithSearchPath(t *testing.T) {
	const base = "postgres://u:p@127.0.0.1:5432/kacho_iam_t0001"

	cases := []struct {
		name       string
		dsn        string
		searchPath string
		want       string
		why        string
	}{
		{
			name: "DSN без параметров получает `?`",
			dsn:  base, searchPath: "kaname,public",
			want: base + "?options=-c%20search_path%3Dkaname%2Cpublic",
			why:  "разделитель выбирается по наличию `?`, а не ставится всегда одинаковым",
		},
		{
			name: "DSN с параметрами получает `&`",
			dsn:  base + "?sslmode=disable", searchPath: "kaname,public",
			want: base + "?sslmode=disable&options=-c%20search_path%3Dkaname%2Cpublic",
			why:  "ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к предыдущему: один факт — наличие `?`",
		},
		{
			name: "приведение не объявлено — DSN не трогается",
			dsn:  base, searchPath: "",
			want: base,
			why:  "пакет вправе не объявлять схему; тогда поведение прежнее",
		},
		{
			name: "клауза уже есть — не удваивается",
			dsn:  base + "?options=-c%20search_path%3Dkacho_probe", searchPath: "kaname,public",
			want: base + "?options=-c%20search_path%3Dkacho_probe",
			why:  "две клаузы `options` в одном DSN — вторая молча замещает первую либо отвергается драйвером",
		},
		{
			name: "значение экранируется",
			dsn:  base, searchPath: "kaname,public",
			want: base + "?options=-c%20search_path%3Dkaname%2Cpublic",
			why:  "запятая обязана уехать как %2C, иначе она разделяет параметры DSN",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithSearchPath(c.dsn, c.searchPath); got != c.want {
				t.Fatalf("получено %q, ожидалось %q (%s)", got, c.want, c.why)
			}
		})
	}
	t.Logf("случаев проверено %d", len(cases))
}

// TestWithSearchPathSurvivesParsing — результат обязан оставаться
// разбираемым DSN, и клауза обязана дойти до сервера ДОСЛОВНО.
//
// Проба заведена отдельно, потому что предыдущая сверяет БАЙТЫ, а байты можно
// подогнать: строка, собранная неверным экранированием, совпала бы с ожиданием
// и при этом развалилась бы у драйвера. Здесь спрашивается разбор.
func TestWithSearchPathSurvivesParsing(t *testing.T) {
	got := WithSearchPath("postgres://u:p@h:5432/db?sslmode=disable", "kaname,public")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("результат не разбирается как URL: %v", err)
	}
	q := u.Query()
	if want := "-c search_path=kaname,public"; q.Get("options") != want {
		t.Fatalf("после разбора options = %q, ожидалось %q", q.Get("options"), want)
	}
	if q.Get("sslmode") != "disable" {
		t.Fatalf("прежний параметр потерян: sslmode = %q", q.Get("sslmode"))
	}
	if strings.Count(got, "options=") != 1 {
		t.Fatalf("клауза options встречается %d раз(а): %s", strings.Count(got, "options="), got)
	}
	t.Logf("разобрано: options=%q, sslmode=%q", q.Get("options"), q.Get("sslmode"))
}
