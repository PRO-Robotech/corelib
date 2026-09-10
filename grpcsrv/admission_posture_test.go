// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// admission_posture_test.go — пол платформы и ручки посадки: одно место, где
// написаны числа, и один предикат, отличающий «посадка молчит» от «посадка
// сказала половину».
//
// # Почему пол вообще существует
//
// Величины допуска — ОПУБЛИКОВАННЫЙ ПОЛ продукта, а не настройка каждого
// сервиса по вкусу. Пока их не было в фундаменте, единственный сервис,
// провязавший ограничитель, нёс числа в своём чарте, и следующие шесть завели бы
// свои — шесть мест об одном предмете, расходящихся молча. Здесь они названы
// один раз, и всякий, кто их не переопределяет, получает ИХ, а не ноль.
//
// # Почему ноль не может означать «не ограничиваем»
//
// Ровно потому, что так он и читается механизмом: `AdmissionLimits.IsDeclared`
// ложен на нуле, ограничитель не навешивается, и слушатель выглядит защищённым,
// ни разу не отказав. Поэтому у ручек посадки три состояния, а не два:
// молчание (берётся пол), полный набор (берётся он) и ЧАСТЬ набора — отказ,
// потому что оператор, задавший темп и забывший одновременность, считает предел
// выставленным.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// TestPlatformFloorIsUsableOnBothListeners — пол обязан быть исполнимым набором.
//
// Положительный контроль всей пары проб ниже: если бы пол сам был негоден,
// «посадка молчит → берём пол» означало бы «посадка молчит → ограничителя нет»,
// и отрицания зеленели бы на сломанном.
func TestPlatformFloorIsUsableOnBothListeners(t *testing.T) {
	for name, l := range map[string]grpcsrv.AdmissionLimits{
		"public":   grpcsrv.PlatformPublicAdmission(),
		"internal": grpcsrv.PlatformInternalAdmission(),
	} {
		require.Truef(t, l.IsDeclared(),
			"пол листенера %s обязан быть полным набором: %s", name, l)
		require.Emptyf(t, l.Unusable(),
			"пол листенера %s обязан быть исполнимым: %v", name, l.Unusable())
	}
}

// TestInternalFloorIsAboveThePublicOne — внутренний пол выше публичного.
//
// Не косметика: ограничитель, задушивший наш собственный поток намерения,
// воспроизводит заклинивание головы очереди — класс, при котором работа
// перестаёт доезжать БЕЗ ЕДИНОГО ВИДИМОГО СИМПТОМА. Проба утверждает отношение,
// а не числа: числа посадка вправе двигать, отношение — нет.
func TestInternalFloorIsAboveThePublicOne(t *testing.T) {
	pub, in := grpcsrv.PlatformPublicAdmission(), grpcsrv.PlatformInternalAdmission()
	assert.Greater(t, in.ReadPerSec, pub.ReadPerSec, "темп чтений внутреннего листенера")
	assert.Greater(t, in.MutationPerSec, pub.MutationPerSec, "темп мутаций внутреннего листенера")
	assert.Greater(t, in.InFlight, pub.InFlight, "одновременность внутреннего листенера")
}

// TestSilentPostureTakesTheFloor — посадка не сказала ничего: берётся пол.
func TestSilentPostureTakesTheFloor(t *testing.T) {
	var k grpcsrv.AdmissionKnobs
	require.True(t, k.IsSilent(), "нулевые ручки обязаны читаться как «посадка молчит»")

	got, err := k.Resolve(grpcsrv.PlatformPublicAdmission())
	require.NoError(t, err)
	assert.Equal(t, grpcsrv.PlatformPublicAdmission(), got)
}

// TestDeclaredPostureWins — посадка назвала все четыре оси: берутся её величины.
func TestDeclaredPostureWins(t *testing.T) {
	k := grpcsrv.AdmissionKnobs{ReadPerSec: 7, MutationPerSec: 3, BurstFactor: 2, InFlight: 5}
	require.False(t, k.IsSilent())

	got, err := k.Resolve(grpcsrv.PlatformPublicAdmission())
	require.NoError(t, err)
	assert.Equal(t, grpcsrv.AdmissionLimits{ReadPerSec: 7, MutationPerSec: 3, BurstFactor: 2, InFlight: 5}, got)
}

// TestPartialPostureIsRefused — посадка назвала ЧАСТЬ осей.
//
// Самый опасный вход: он выглядит как настройка и не ограничивает по
// незаполненной оси. Отказ обязан называть ось, иначе оператор ищет причину в
// файле, где всё написано верно.
func TestPartialPostureIsRefused(t *testing.T) {
	cases := map[string]grpcsrv.AdmissionKnobs{
		"без темпа чтений":  {MutationPerSec: 3, BurstFactor: 2, InFlight: 5},
		"без темпа мутаций": {ReadPerSec: 7, BurstFactor: 2, InFlight: 5},
		"без всплеска":      {ReadPerSec: 7, MutationPerSec: 3, InFlight: 5},
		"без одновременных": {ReadPerSec: 7, MutationPerSec: 3, BurstFactor: 2},
	}
	for name, k := range cases {
		t.Run(name, func(t *testing.T) {
			require.False(t, k.IsSilent(), "частичное объявление молчанием НЕ является")
			_, err := k.Resolve(grpcsrv.PlatformPublicAdmission())
			require.Error(t, err, "частичный набор обязан быть отвергнут, а не дополнен полом")
			assert.Contains(t, err.Error(), "ЧАСТЬ осей",
				"отказ обязан называть предмет, а не быть безымянным")
		})
	}
}

// TestSelfContradictingBurstIsRefused — всплеск ниже устойчивого темпа.
//
// Отвергается В ЛЮБОМ режиме: это не вопрос посадки, а негодность сама по себе —
// ведро не наполняется до одного токена, и отвергается даже законный поток.
func TestSelfContradictingBurstIsRefused(t *testing.T) {
	k := grpcsrv.AdmissionKnobs{ReadPerSec: 7, MutationPerSec: 3, BurstFactor: 0.5, InFlight: 5}
	_, err := k.Resolve(grpcsrv.PlatformPublicAdmission())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "всплеск")
}

// TestFloorSurvivesAResolveRoundTrip — пол, поданный ручками, даёт сам себя.
//
// Закрывает тихий разъезд: ручки читаются посадкой, пол — кодом, и если бы они
// расходились в порядке полей, посадка, дословно повторившая пол, получила бы
// другой набор.
func TestFloorSurvivesAResolveRoundTrip(t *testing.T) {
	floor := grpcsrv.PlatformInternalAdmission()
	k := grpcsrv.AdmissionKnobs{
		ReadPerSec:     floor.ReadPerSec,
		MutationPerSec: floor.MutationPerSec,
		BurstFactor:    floor.BurstFactor,
		InFlight:       floor.InFlight,
	}
	got, err := k.Resolve(grpcsrv.PlatformPublicAdmission())
	require.NoError(t, err)
	assert.Equal(t, floor, got)
}
