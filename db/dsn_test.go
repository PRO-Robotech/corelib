// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db_test

import (
	"testing"

	coredb "github.com/PRO-Robotech/corelib/db"
)

// TestSSLModeFromDSN — эффективный sslmode берётся из ТОЙ строки, что уходит в
// pgxpool (а не из сырого config-поля): часть сервисов держит sslmode прямо в
// URL, часть — деривит его в composeDSN/baseDSN.
func TestSSLModeFromDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{"url form", "postgres://u:p@h:5432/d?sslmode=require&pool_max_conns=10", "require"},
		{"url form verify-full", "postgres://u:p@h/d?sslmode=verify-full", "verify-full"},
		{"url form disable", "postgres://u:p@h/d?sslmode=disable&options=-c%20search_path%3Dkacho_vpc", "disable"},
		{"keyword form", "host=h port=5432 user=u sslmode=verify-ca dbname=d", "verify-ca"},
		{"case is normalised", "postgres://u:p@h/d?sslmode=REQUIRE", "require"},
		// Отсутствующий sslmode — libpq/pgx-дефолт `prefer` (plaintext-fallback),
		// а НЕ «secure»: гейт обязан видеть реальную деградацию.
		{"absent → libpq default", "postgres://u:p@h/d", coredb.DefaultSSLMode},
		{"absent keyword form", "host=h user=u dbname=d", coredb.DefaultSSLMode},
		// Пароль со спецсимволом ломает url.Parse — fallback-скан обязан всё
		// равно достать реальный sslmode (иначе гейт увидит ложный `prefer`).
		{"unparseable url falls back to scan", "postgres://u:p@ss w@h/d?sslmode=require", "require"},
		{"empty dsn", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := coredb.SSLModeFromDSN(tc.dsn); got != tc.want {
				t.Fatalf("SSLModeFromDSN(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// TestSSLModeFromDSN_NeverLeaksCredentials — возвращаем ТОЛЬКО режим: строка
// уходит в лог, а DSN несёт пароль.
func TestSSLModeFromDSN_NeverLeaksCredentials(t *testing.T) {
	got := coredb.SSLModeFromDSN("postgres://kacho:s3cr3t@h/d?sslmode=require")
	if got != "require" {
		t.Fatalf("got %q, want %q", got, "require")
	}
}
