// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/db"
)

// poolparam_test.go — прямое утверждение о ЗНАЧЕНИИ, которое уезжает в отказ
// старта: имя ключа и ничего больше.
//
// Свойство «отказ не печатает пароль» на месте вызова неразрешимо — там нужен
// поток данных. Здесь оно разрешимо и утверждается напрямую: у стража на руках
// только то, что вернула эта функция.

// dsnWithPassword — строка ровно той формы, какую собирает baseDSN сервисов:
// пароль внутри, через url.UserPassword.
func dsnWithPassword(t *testing.T, extraQuery string) (dsn, password string) {
	t.Helper()
	password = "s3cret-Пароль-pool_max_conns"
	q := url.Values{}
	q.Set("sslmode", "require")
	q.Set("options", "-c search_path=kacho_vpc")
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("kacho", password),
		Host:     "pg.kacho.svc:5432",
		Path:     "/kacho_vpc",
		RawQuery: q.Encode(),
	}
	dsn = u.String()
	if extraQuery != "" {
		dsn += "&" + extraQuery
	}
	return dsn, password
}

// TestPoolParamFromDSNNamesTheKnobAndNothingElse — ИСХОД: возвращается имя
// ключа; ни пароля, ни хоста, ни базы в ответе нет.
func TestPoolParamFromDSNNamesTheKnobAndNothingElse(t *testing.T) {
	dsn, password := dsnWithPassword(t, "pool_max_conns=4")
	got := db.PoolParamFromDSN(dsn)
	if got != "pool_max_conns" {
		t.Fatalf("страж получил %q, а обязан получить имя ключа %q", got, "pool_max_conns")
	}
	for _, secret := range []string{password, "pg.kacho.svc", "kacho_vpc", "postgres://"} {
		if strings.Contains(got, secret) {
			t.Errorf("ответ несёт %q — это уезжает в отказ старта, то есть в журнал и оператору", secret)
		}
	}
}

// TestPoolParamFromDSNIgnoresThePassword — контроль в обратную сторону:
// последовательность в ПАРОЛЕ ключом не является. Подстрочная проверка,
// стоявшая здесь прежде, отказывала бы в старте по содержимому секрета.
func TestPoolParamFromDSNIgnoresThePassword(t *testing.T) {
	dsn, password := dsnWithPassword(t, "")
	if !strings.Contains(password, db.PoolParamPrefix) {
		t.Fatalf("фикстура негодна: пароль обязан нести %q, иначе проба ничего не измеряет", db.PoolParamPrefix)
	}
	if got := db.PoolParamFromDSN(dsn); got != "" {
		t.Errorf("пуловый ключ найден в пароле (%q) — страж отказал бы в старте по содержимому секрета", got)
	}
}

// TestPoolParamFromDSNReadsBothWireForms — обе формы, которыми строка приходит
// в это дерево. Форма, которой предикат не знает, — не край, а слепая зона:
// всё записанное в ней остаётся вне наблюдения.
func TestPoolParamFromDSNReadsBothWireForms(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"URL-форма", "postgres://u:p@h:5432/d?sslmode=require&pool_max_conns=4", "pool_max_conns"},
		{"URL-форма, ключа нет", "postgres://u:p@h:5432/d?sslmode=require", ""},
		{"keyword-форма", "host=h port=5432 dbname=d pool_min_conns=2", "pool_min_conns"},
		{"keyword-форма, ключа нет", "host=h port=5432 dbname=d sslmode=require", ""},
		{"имя базы похоже на ключ", "postgres://u:p@h:5432/pool_metrics?sslmode=require", ""},
		{"пустая строка", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := db.PoolParamFromDSN(tc.dsn); got != tc.want {
				t.Errorf("получено %q, ожидалось %q", got, tc.want)
			}
		})
	}
	t.Logf("перепись: форм проводной записи осмотрено %d", len(cases))
}
