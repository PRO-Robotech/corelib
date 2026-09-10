// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package observability_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/PRO-Robotech/corelib/observability"
)

// TestLogBootPosture_ContractShape локает ХАРД-КОНТРАКТ boot-строки: сообщение и
// имена полей парсит production-posture гейт (jq), поэтому любое переименование
// ключа/сообщения ломает гейт молча. Тест держит форму на уровне НАБЛЮДАЕМОГО
// JSON-вывода, а не на уровне вызова.
func TestLogBootPosture_ContractShape(t *testing.T) {
	var buf bytes.Buffer
	observability.LogBootPosture(observability.NewSlogger(&buf), observability.BootPosture{
		Service:           "vpc",
		AuthMode:          "production",
		DBSSLMode:         "require",
		PublicMTLS:        true,
		InternalMTLS:      observability.InternalMTLSEnabled,
		AuthZCheck:        true,
		TrustedForwarders: true,
	})

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("boot posture line is not one JSON object: %v (raw=%q)", err, buf.String())
	}
	want := map[string]any{
		"msg":                "boot security posture",
		"service":            "vpc",
		"auth_mode":          "production",
		"db_sslmode":         "require",
		"public_mtls":        true,
		"internal_mtls":      "true",
		"authz_check":        true,
		"trusted_forwarders": true,
	}
	for k, v := range want {
		got, ok := line[k]
		if !ok {
			t.Fatalf("field %q missing from boot posture line: %v", k, line)
		}
		if got != v {
			t.Fatalf("field %q = %v (%T), want %v (%T)", k, got, got, v, v)
		}
	}
}

// TestLogBootPosture_FalseFlagsAreEmitted — false обязан ПРИСУТСТВОВАТЬ в строке
// (а не отсутствовать): гейт отличает «выключено» от «поле не эмитится».
//
// Сервис здесь назван vpc, а не api-gateway, хотя измерение db_sslmode="n/a"
// сегодня есть только у края: у края с задачи #1024 нет ВНУТРЕННЕГО ЛИСТЕНЕРА, и
// сочетание «нет базы + internal_mtls выключен» стало непроизводимым ни одним
// процессом дерева. Фикстура, описывающая невозможную посадку, — утверждение,
// пережившее свой предмет; предмет же этой пробы (нулевые величины эмитятся, а не
// пропадают) от имени сервиса не зависит.
func TestLogBootPosture_FalseFlagsAreEmitted(t *testing.T) {
	var buf bytes.Buffer
	observability.LogBootPosture(observability.NewSlogger(&buf), observability.BootPosture{
		Service:      "vpc",
		AuthMode:     "dev",
		DBSSLMode:    observability.DBSSLModeNotApplicable,
		InternalMTLS: observability.InternalMTLSDisabled,
	})

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("unmarshal: %v (raw=%q)", err, buf.String())
	}
	for _, k := range []string{"public_mtls", "authz_check", "trusted_forwarders"} {
		v, ok := line[k]
		if !ok {
			t.Fatalf("field %q must be emitted even when false: %v", k, line)
		}
		if v != false {
			t.Fatalf("field %q = %v, want false", k, v)
		}
	}
	// internal_mtls — строковое измерение (три состояния), поэтому «выключено»
	// у него не булево false, а объявленная величина.
	if line["internal_mtls"] != "false" {
		t.Fatalf("internal_mtls = %#v, want the declared literal %q",
			line["internal_mtls"], observability.InternalMTLSDisabled)
	}
	if line["db_sslmode"] != "n/a" {
		t.Fatalf("db_sslmode = %v, want the literal \"n/a\" for a database-less service", line["db_sslmode"])
	}
}

// TestBootPostureMsg_IsTheParsedContract — сообщение экспортировано константой,
// чтобы сервис-тесты и гейт ссылались на один литерал.
func TestBootPostureMsg_IsTheParsedContract(t *testing.T) {
	if observability.BootPostureMsg != "boot security posture" {
		t.Fatalf("BootPostureMsg = %q — контракт гейта требует %q",
			observability.BootPostureMsg, "boot security posture")
	}
}

// TestLogBootPosture_InternalMTLSHasThreeDeclaredStates — поле internal_mtls
// обязано выражать ТРИ объявленные величины, а не две.
//
// Предмет. У края внутреннего gRPC-слушателя больше нет вовсе (задача #1024),
// и он обязан отчитаться об этом отличимо от «слушатель есть и не защищён».
// Булево поле такого сказать не может: у него два состояния, и нулевое немо —
// процесс, забывший заполнить структуру, неотличим от процесса, честно
// доложившего «нет». Поэтому величина строковая, ровно как у соседних
// db_sslmode и identity_provider в этом же самоотчёте.
//
// Четвёртое состояние — ПУСТАЯ строка — объявленным НЕ является и обязано
// доезжать до гейта как есть: гейт посадки судит его отказом. Здесь
// утверждается только то, что сериализация его не подменяет и не прячет.
func TestLogBootPosture_InternalMTLSHasThreeDeclaredStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		give string
		want string
	}{
		{"слушатель есть, mTLS включён", observability.InternalMTLSEnabled, "true"},
		{"слушатель есть, mTLS выключен", observability.InternalMTLSDisabled, "false"},
		{"слушателя нет вовсе", observability.InternalMTLSNotApplicable, "n/a"},
		{"незаполненное поле доезжает как есть", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			observability.LogBootPosture(observability.NewSlogger(&buf), observability.BootPosture{
				Service:      "api-gateway",
				AuthMode:     "production",
				DBSSLMode:    observability.DBSSLModeNotApplicable,
				InternalMTLS: tc.give,
			})
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("строка самоотчёта не разобралась: %v (raw=%q)", err, buf.String())
			}
			got, ok := line["internal_mtls"]
			if !ok {
				t.Fatalf("ключ internal_mtls не эмитится вовсе: %v", line)
			}
			if got != tc.want {
				t.Fatalf("internal_mtls = %#v (%T), ждали %q — величина обязана доезжать "+
					"до гейта дословно, без подмены", got, got, tc.want)
			}
		})
	}
}

// TestInternalMTLSConstants_AreTheParsedContract — величины парсит гейт посадки
// (deploy/scripts/assert-production-posture.sh), поэтому их литералы — контракт,
// а не удобство. Переименование молча ослепляет гейт.
func TestInternalMTLSConstants_AreTheParsedContract(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{observability.InternalMTLSEnabled, "true"},
		{observability.InternalMTLSDisabled, "false"},
		{observability.InternalMTLSNotApplicable, "n/a"},
	} {
		if tc.got != tc.want {
			t.Fatalf("величина internal_mtls = %q, контракт гейта требует %q", tc.got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// СОБСТВЕННЫЕ REST-ФРОНТЫ ПРОЦЕССА (задача #2108)
// ─────────────────────────────────────────────────────────────────────────────

// ownRESTFrontStub — объявление поверхности, поданное пробой напрямую.
//
// Узкий интерфейс самоотчёта позволяет подать посадку, не собирая ни слушателя,
// ни удостоверения: проба меняет РОВНО ОДИН факт против своего положительного
// близнеца.
type ownRESTFrontStub struct{ raised, tls bool }

func (s ownRESTFrontStub) Enabled() bool  { return s.raised }
func (s ownRESTFrontStub) UnderTLS() bool { return s.tls }

// TestLogBootPosture_OwnRESTFrontsHaveThreeDeclaredStates — оба фронта обязаны
// выражать ТРИ объявленные величины, а не две.
//
// Предмет тот же, что у internal_mtls: «фронта нет вовсе» обязано быть отличимо
// от «фронт поднят и работает открытым текстом». Схлопнуть их значило бы
// разрешить открытый текст молчанием.
//
// Четвёртое состояние — ПУСТАЯ строка — объявленным не является и обязано
// доезжать до гейта как есть: гейт посадки судит его отказом.
func TestLogBootPosture_OwnRESTFrontsHaveThreeDeclaredStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		give string
		want string
	}{
		{"фронт поднят, провод под транспортом", observability.OwnRESTFrontTLS, "true"},
		{"фронт поднят, провод открытым текстом", observability.OwnRESTFrontPlaintext, "false"},
		{"фронта нет вовсе", observability.OwnRESTFrontNotRaised, "n/a"},
		{"незаполненное поле доезжает как есть", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			observability.LogBootPosture(observability.NewSlogger(&buf), observability.BootPosture{
				Service:            "iam",
				AuthMode:           "production",
				DBSSLMode:          "require",
				InternalMTLS:       observability.InternalMTLSEnabled,
				OwnRESTPublicTLS:   tc.give,
				OwnRESTInternalTLS: tc.give,
			})
			var line map[string]any
			if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
				t.Fatalf("строка самоотчёта не разобралась: %v (raw=%q)", err, buf.String())
			}
			for _, key := range []string{"own_rest_public_tls", "own_rest_internal_tls"} {
				got, ok := line[key]
				if !ok {
					t.Fatalf("ключ %s не эмитится вовсе: %v", key, line)
				}
				if got != tc.want {
					t.Fatalf("%s = %#v (%T), ждали %q — величина обязана доезжать до гейта "+
						"дословно, без подмены", key, got, got, tc.want)
				}
			}
		})
	}
}

// TestOwnRESTFrontConstants_AreTheParsedContract — величины парсит гейт посадки
// (deploy/scripts/assert-production-posture.sh), поэтому их литералы — контракт,
// а не удобство. Переименование молча ослепляет гейт.
func TestOwnRESTFrontConstants_AreTheParsedContract(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{observability.OwnRESTFrontTLS, "true"},
		{observability.OwnRESTFrontPlaintext, "false"},
		{observability.OwnRESTFrontNotRaised, "n/a"},
	} {
		if tc.got != tc.want {
			t.Fatalf("величина собственного REST-фронта = %q, контракт гейта требует %q",
				tc.got, tc.want)
		}
	}
}

// TestOwnRESTFrontFrom_DerivesFromTheDeclarationThatRaisesTheSurface — величина
// ВЫВОДИТСЯ из того же объявления поверхности, по которому она поднимается.
//
// Почему это важнее удобства: самоотчёт и доклад поверхности при подъёме — два
// утверждения об одном предмете. Выведенные из ОДНОГО объявления, разойтись они
// не могут by construction; вписанные порознь — разойдутся молча, и верным
// окажется одно (класс #2194).
//
// Случая «фронта нет вовсе» здесь НЕТ намеренно, ровно как у InternalMTLSFrom:
// он не выводится из объявления, потому что объявления у такого процесса не
// существует. Он называется константой прямо в композиционном корне — там, где
// видно, что поверхность не поднимается.
func TestOwnRESTFrontFrom_DerivesFromTheDeclarationThatRaisesTheSurface(t *testing.T) {
	for _, tc := range []struct {
		name string
		give ownRESTFrontStub
		want string
	}{
		{"поднят под транспортом", ownRESTFrontStub{raised: true, tls: true}, observability.OwnRESTFrontTLS},
		{"поднят открытым текстом", ownRESTFrontStub{raised: true, tls: false}, observability.OwnRESTFrontPlaintext},
		{"не поднят", ownRESTFrontStub{raised: false, tls: false}, observability.OwnRESTFrontNotRaised},
		// Транспорт объявлен, а поверхность не поднимается: величина обязана
		// говорить о ПОВЕРХНОСТИ, а не о ручке транспорта — иначе самоотчёт
		// сообщал бы о защите того, чего нет.
		{"не поднят, транспорт объявлен", ownRESTFrontStub{raised: false, tls: true}, observability.OwnRESTFrontNotRaised},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := observability.OwnRESTFrontFrom(tc.give); got != tc.want {
				t.Fatalf("OwnRESTFrontFrom(%+v) = %q, ждали %q", tc.give, got, tc.want)
			}
		})
	}
}
