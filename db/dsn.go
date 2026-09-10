// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"net/url"
	"strings"
)

// SSLModeFromDSN возвращает sslmode, который реально доезжает до пула.
//
// Читать сырое config-поле недостаточно: часть сервисов держит sslmode прямо в
// URL (`repository.postgres.url`), часть деривит его в composeDSN/baseDSN
// (пустое → `disable`), а третьи — комбинируют (raw-URL выигрывает у поля).
// Единственный честный источник — сама строка, отданная pgxpool.
//
// Возвращает НОРМАЛИЗОВАННЫЙ (lower-case) режим; пустой DSN → пустая строка.
// Никогда не возвращает ничего, кроме режима — DSN несёт пароль, а результат
// уходит в лог.
func SSLModeFromDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return ""
	}
	// URL-форма (`postgres://…?sslmode=…`) — точный разбор query.
	if u, err := url.Parse(dsn); err == nil && u.Query().Has("sslmode") {
		if mode := strings.TrimSpace(u.Query().Get("sslmode")); mode != "" {
			return strings.ToLower(mode)
		}
	}
	// Fallback-скан: keyword-форма (`host=… sslmode=require`) и URL, который
	// url.Parse не осилил (напр. спецсимвол в пароле) — иначе реальный
	// `require` был бы отрапортован как `prefer`.
	if i := strings.Index(strings.ToLower(dsn), "sslmode="); i >= 0 {
		rest := dsn[i+len("sslmode="):]
		if j := strings.IndexAny(rest, "& \t"); j >= 0 {
			rest = rest[:j]
		}
		if mode := strings.TrimSpace(rest); mode != "" {
			return strings.ToLower(mode)
		}
	}
	return DefaultSSLMode
}

// PoolParamPrefix — префикс ключей, которые понимает ТОЛЬКО pgxpool. Вне пула
// (`pgx.Connect`) такой ключ уезжает серверу как неизвестный runtime-параметр,
// и отказ наступает на ПОДКЛЮЧЕНИИ, а не на сборке строки.
const PoolParamPrefix = "pool_"

// PoolParamFromDSN возвращает ИМЯ пулового параметра, найденного в строке
// подключения, либо пустую строку.
//
// Возвращает ТОЛЬКО имя ключа — никогда саму строку и никакую её часть, кроме
// имени. Довод тот же, что у SSLModeFromDSN выше: строка собирается через
// url.UserPassword и НЕСЁТ ПАРОЛЬ БАЗЫ, а результат уходит в отказ старта, то
// есть в журнал и оператору. Имя ключа отвечает на вопрос «что править», и
// этого достаточно, чтобы поднять стенд.
//
// Читает КЛЮЧИ, а не подстроку. Подстрочная проверка совпадала бы и на пароле,
// и на имени базы, где такая последовательность законна, — то есть отказывала
// бы в старте по содержимому секрета.
//
// Разбирает обе формы, которыми строка приходит в это дерево: URL-форму
// (`postgres://…?pool_max_conns=4`) и keyword-форму (`host=… pool_max_conns=4`).
// Если URL не разбирается (спецсимвол в пароле), остаётся скан ключей по
// разделителям — он судит левую часть токена, поэтому значение под предикат
// по-прежнему не подпадает.
func PoolParamFromDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return ""
	}
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		for key := range u.Query() {
			if strings.HasPrefix(strings.ToLower(key), PoolParamPrefix) {
				return key
			}
		}
		return ""
	}
	for _, token := range strings.FieldsFunc(dsn, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '&' || r == '?'
	}) {
		eq := strings.IndexByte(token, '=')
		if eq <= 0 {
			continue
		}
		if key := token[:eq]; strings.HasPrefix(strings.ToLower(key), PoolParamPrefix) {
			return key
		}
	}
	return ""
}
