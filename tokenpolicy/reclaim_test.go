// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// reclaim_test.go — отсрочка снятия истёкшего удостоверения ВЫЧИСЛЯЕТСЯ, а не
// выбирается (задача #1264, сценарий CRED-RCL-14).

package tokenpolicy_test

import (
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

// defaultRegistryTokenTTL — умолчание `api-server.registry-token.ttl`.
//
// Величина ЖИВЁТ в конфигурации iam и приходит в расчёт аргументом; здесь она
// нужна лишь как вход пробы, и это сказано, чтобы число не читалось вторым
// объявлением.
const defaultRegistryTokenTTL = 5 * time.Minute

// CRED-RCL-14 — нижняя граница ВЫЧИСЛЯЕТСЯ из слагаемых, а не написана числом.
//
// Проба складывает слагаемые САМА и сверяет с ответом функции. Смена любого из
// них без пересмотра отсрочки роняет её и называет оба числа — то есть предикат
// существует затем, чтобы следующий, кто захочет снимать «сразу», упёрся в часы,
// а не в чужое мнение.
func TestCredRcl14_ReclaimFloorIsComputedFromItsTerms(t *testing.T) {
	want := tokenpolicy.ClockSkew + defaultRegistryTokenTTL + tokenpolicy.RemovalSlack
	got := tokenpolicy.MinExpiredCredentialReclaimDelay(defaultRegistryTokenTTL)
	if got != want {
		t.Fatalf("нижняя граница отсрочки: получено %v, ожидалось %v = допуск часов %v + срок докерного токена %v + запас %v",
			got, want, tokenpolicy.ClockSkew, defaultRegistryTokenTTL, tokenpolicy.RemovalSlack)
	}

	// Слагаемое СРОКА ДОКЕРНОГО ТОКЕНА участвует, и это утверждается отдельно:
	// без него граница не двигалась бы от настройки, и поднятый срок молча
	// вывел бы отсрочку из-под её же основания.
	raised := tokenpolicy.MinExpiredCredentialReclaimDelay(defaultRegistryTokenTTL + time.Hour)
	if raised != got+time.Hour {
		t.Fatalf("граница обязана двигаться со сроком докерного токена: %v против %v", raised, got)
	}

	// MaxTokenTTL в сумму НЕ входит — это решение, а не пропуск: на полосе
	// ключевой пары токен не переживает клиента, и запас взят самим урезанием.
	if got >= want+tokenpolicy.MaxTokenTTL {
		t.Fatalf("MaxTokenTTL попал в сумму: %v", got)
	}

	t.Logf("перепись слагаемых: допуск часов %v · срок докерного токена %v · запас %v ⇒ пол %v",
		tokenpolicy.ClockSkew, defaultRegistryTokenTTL, tokenpolicy.RemovalSlack, got)
}

// CRED-RCL-14 (вторая половина) — предикат связывает ДВЕ величины, и он
// проверяется в ОБЕ стороны.
//
// Одностороннее «объявленная отсрочка покрывает пол» зеленело бы на предикате,
// возвращающем истину всегда.
func TestCredRcl14_GracePredicateHoldsBothWays(t *testing.T) {
	floor := tokenpolicy.MinExpiredCredentialReclaimDelay(defaultRegistryTokenTTL)

	if !tokenpolicy.ReclaimGraceCoversLiveTokens(tokenpolicy.ExpiredCredentialReclaimGrace, defaultRegistryTokenTTL) {
		t.Fatalf("объявленная отсрочка %v не покрывает пол %v — снятие обгоняло бы живые токены",
			tokenpolicy.ExpiredCredentialReclaimGrace, floor)
	}
	// Отрицание: величина ПОД полом обязана быть отвергнута. Без этой половины
	// предикат мог бы возвращать истину на любом входе.
	if tokenpolicy.ReclaimGraceCoversLiveTokens(floor-time.Second, defaultRegistryTokenTTL) {
		t.Fatalf("предикат принял отсрочку под полом (%v < %v) — он не сужает ничего", floor-time.Second, floor)
	}
}

// CRED-RCL-04 (модульная половина) — отсрочка СВЯЗАНА со сроком самой строки.
//
// Таблица утверждает ВСЕ ТРИ области, включая ту, где строка лежит дольше, чем
// жила: это свойство технического пола, а не дефект уборки, и молчать о нём
// нельзя — иначе следующий читатель примет его за ошибку и «починит».
func TestCredRcl04_GraceIsTiedToTheRowsOwnLifetime(t *testing.T) {
	const (
		ceiling = 24 * time.Hour
		floor   = 21 * time.Minute
	)
	cases := []struct {
		name     string
		lifetime time.Duration
		want     time.Duration
	}{
		{"умолчание секрета — общий случай не меняется", 30 * 24 * time.Hour, ceiling},
		{"потолок секрета и умолчание ключа машины", 90 * 24 * time.Hour, ceiling},
		{"часовое удостоверение: окно памяти не длиннее жизни вещи", time.Hour, time.Hour},
		{"пятиминутное: упирается в технический пол", 5 * time.Minute, floor},
		{"ровно пол", floor, floor},
	}
	for _, c := range cases {
		got := tokenpolicy.ExpiredCredentialGraceFor(c.lifetime, ceiling, floor)
		if got != c.want {
			t.Errorf("%s: срок %v ⇒ отсрочка %v, ожидалось %v", c.name, c.lifetime, got, c.want)
		}
	}

	// Отсрочка НИКОГДА не опускается под пол: там ещё живут отчеканенные токены.
	if got := tokenpolicy.ExpiredCredentialGraceFor(time.Second, ceiling, floor); got < floor {
		t.Fatalf("отсрочка ушла под пол: %v < %v", got, floor)
	}
	// И никогда не поднимается над потолком.
	if got := tokenpolicy.ExpiredCredentialGraceFor(365*24*time.Hour, ceiling, floor); got > ceiling {
		t.Fatalf("отсрочка ушла над потолком: %v > %v", got, ceiling)
	}
	t.Logf("перепись областей: рассмотрено %d сроков плюс обе границы", len(cases))
}
