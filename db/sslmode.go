// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db

import "strings"

// Режим шифрования соединения с собственной базой: ЕДИНСТВЕННОЕ в дереве
// объявление перечня и предикатов над ним.
//
// # Почему это один дом, а не по копии на сервис
//
// Перечень безопасных значений — правило БЕЗОПАСНОСТИ: страж старта отказывает
// в пуске, когда до базы идёт открытый канал (`security.md` §Production-mode,
// CWE-319). Пока перечень объявлен у каждого сервиса своими руками, «эту ось
// здесь забыли» неотличимо от «эту ось здесь решили не судить», а копии
// расходятся МОЛЧА: сойтись им нечем — они не собираются вместе и не читают
// друг друга.
//
// Замер, из которого дом заведён (задача продукта #1464): живых боевых проверок
// с собственным перечнем было ЧЕТЫРЕ — vpc, nlb, compute, registry, — и две из
// них лежали вне `services/*/internal`, то есть за границей предиката, которым
// класс искали. Плюс два словаря принимаемых настройкой значений (vpc, iam) и
// собственный набор центрального дескриптора посадки.
//
// # Почему перечисляются ДОПУСТИМЫЕ, а не запрещённые
//
// Перечень запретов пропускает всякое значение, которого в нём нет, — то есть
// любую опечатку. `sslmode=requrie` не значится в запретах и потому прошёл бы
// как безопасный, а libpq на нём откажет уже в рантайме.
//
// # Про `allow` и `prefer`
//
// Оба допускают plaintext-fallback: `prefer` — умолчание libpq, когда `sslmode`
// в строке не задан вовсе. Поэтому ОТСУТСТВИЕ параметра защищённым каналом не
// является, и `SSLModeFromDSN` возвращает на такой строке [DefaultSSLMode], а не
// пустоту.
//
// Настройка их не принимает: держать в конфигурации значение, которое молча
// деградирует до открытого канала, значит предлагать ловушку. Ни один профиль
// развёртывания их не задаёт, и словарь принимаемых значений их не несёт.

// DefaultSSLMode — режим, который применяет libpq/pgx, когда DSN вообще не несёт
// `sslmode`.
const DefaultSSLMode = "prefer"

// sslModeRule — одна строка перечня. Оси разведены намеренно: «безопасен» и
// «принимается настройкой» — разные вопросы, и `disable` отвечает на них
// по-разному (принимается, но небезопасен).
type sslModeRule struct {
	name string
	// secure — канал до базы шифруется. Боевая посадка допускает только такие.
	secure bool
	// configurable — значение принимается настройкой сервиса. `allow`/`prefer`
	// не принимаются: они молча деградируют до открытого канала.
	configurable bool
}

// sslModeRules — перечень. ПОРЯДОК ЗНАЧИМ: из него собираются тексты отказов,
// которые видит оператор, и они — часть контракта (`api-conventions.md`
// §Error-format: тон сообщений стабилен и меняется осознанно).
var sslModeRules = []sslModeRule{
	{name: "disable", configurable: true},
	{name: "allow"},
	{name: DefaultSSLMode},
	{name: "require", secure: true, configurable: true},
	{name: "verify-ca", secure: true, configurable: true},
	{name: "verify-full", secure: true, configurable: true},
}

// normalizeSSLMode — ОДНА нормализация на весь дом.
//
// Прежде её делал каждый вызывающий сам, и делал по-разному: часть приводила
// регистр, часть нет, пробелы по краям не снимал никто. Значение приходит из
// переменной окружения и из строки подключения — оба источника пробел допускают.
func normalizeSSLMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

// SSLModes — весь словарь режимов. Нужен распознавателям (гейт дерева
// `internal/repohygiene`), чтобы искать перечисления по ЖИВОМУ перечню, а не по
// своей копии: копия у гейта была бы тем же классом, который гейт ловит.
func SSLModes() []string {
	out := make([]string, 0, len(sslModeRules))
	for _, r := range sslModeRules {
		out = append(out, r.name)
	}
	return out
}

// SecureSSLModes — режимы, допустимые на БОЕВОЙ посадке, в порядке объявления.
// Возвращается копия: перечень — правило безопасности, и вызывающий не должен
// иметь возможности его переписать.
func SecureSSLModes() []string {
	var out []string
	for _, r := range sslModeRules {
		if r.secure {
			out = append(out, r.name)
		}
	}
	return out
}

// ConfigurableSSLModes — режимы, принимаемые настройкой сервиса, в порядке
// объявления. Шире [SecureSSLModes] ровно на `disable`: вне боевого режима
// открытый канал до локальной базы законен.
func ConfigurableSSLModes() []string {
	var out []string
	for _, r := range sslModeRules {
		if r.configurable {
			out = append(out, r.name)
		}
	}
	return out
}

// SSLModeSecure — канал до базы шифруется при этом режиме.
//
// Пустая строка НЕ безопасна и это не придирка: пустое поле деривится сервисами
// в `disable`, а пустой `sslmode` в самой строке подключения означает
// [DefaultSSLMode] — оба допускают открытый канал.
func SSLModeSecure(mode string) bool {
	m := normalizeSSLMode(mode)
	for _, r := range sslModeRules {
		if r.name == m {
			return r.secure
		}
	}
	return false
}

// SSLModeConfigurable — значение принимается настройкой сервиса.
//
// Пустую строку не принимает: «не задано» — отдельный исход, и решает про него
// вызывающий (часть сервисов деривит пустое в `disable`, часть берёт режим из
// сырого URL). Предикат, схлопнувший «не задано» в «принято», лишил бы их этого
// различения.
func SSLModeConfigurable(mode string) bool {
	m := normalizeSSLMode(mode)
	for _, r := range sslModeRules {
		if r.name == m {
			return r.configurable
		}
	}
	return false
}
