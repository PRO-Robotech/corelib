// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quota

// authority_test.go — объявление домена величин у потребителя.
//
// Предмет: ручка, у которой РОВНО ДВА законных значения — адрес домена величин
// либо явное «не развёрнут», — и незаданное значение среди них не значится.
//
// Почему незаданное обязано быть отказом, а не умолчанием: умолчание здесь
// означает выбор за оператора между «потолки действуют» и «потолков нет», и
// выбор этот невидим. Оба возможных умолчания названы и отвергнуты в приёмке
// KAN-QUOTA-1 §3.3.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	knob          = "quota.authority"
	transportKnob = "mtls.quota_authority"
)

// TestResolveAuthority_KAN_Q1_02_UnsetIsRefused — незаданное объявление отвергается,
// и текст отказа называет ручку И ОБА законных значения.
//
// Без имени ручки в тексте стенд не поднять — это одно из трёх мест, прямо
// выведенных из-под запрета `security.md` §«Публичные артефакты».
func TestResolveAuthority_KAN_Q1_02_UnsetIsRefused(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		_, err := ResolveAuthority(Declaration{
			Knob: knob, Value: raw,
			TransportKnob: transportKnob, TransportDeclared: true,
		})
		require.Error(t, err, "незаданное объявление %q обязано отвергаться", raw)
		require.Contains(t, err.Error(), knob, "текст отказа обязан называть ручку")
		require.Contains(t, err.Error(), NotDeployed,
			"текст отказа обязан называть значение «не развёрнут» как законное")
	}
}

// TestResolveAuthority_KAN_Q1_06_NotDeployedIsAccepted — «не развёрнут» есть
// ЗАКОННОЕ значение, а не отказ.
//
// Положительный близнец к отказам старта: без него утверждения «процесс не
// поднимается» зеленели бы на объявлении, которое не принимается никогда.
func TestResolveAuthority_KAN_Q1_06_NotDeployedIsAccepted(t *testing.T) {
	a, err := ResolveAuthority(Declaration{
		Knob: knob, Value: NotDeployed,
		TransportKnob: transportKnob, TransportDeclared: false,
	})
	require.NoError(t, err, "«не развёрнут» — законное объявление")
	require.False(t, a.Deployed())
	require.Empty(t, a.Endpoint(), "у неразвёрнутого домена адреса нет вовсе")
	require.Equal(t, AuthorityAbsent, a.State())
}

// TestResolveAuthority_AddressIsAccepted — адрес принимается и доезжает дословно.
func TestResolveAuthority_AddressIsAccepted(t *testing.T) {
	a, err := ResolveAuthority(Declaration{
		Knob: knob, Value: " kaname-internal.kacho.svc:9091 ",
		TransportKnob: transportKnob, TransportDeclared: true,
	})
	require.NoError(t, err)
	require.True(t, a.Deployed())
	require.Equal(t, "kaname-internal.kacho.svc:9091", a.Endpoint())
	require.Equal(t, AuthorityPresent, a.State())
}

// TestResolveAuthority_KAN_Q1_05_HalfAPairIsRefused — адрес задан, удостоверение нет.
//
// Класс `security.md` §«Контроль, у которого нет МЕХАНИЗМА исполниться»:
// половина пары хуже отсутствия обеих, потому что выглядит настроенной.
func TestResolveAuthority_KAN_Q1_05_HalfAPairIsRefused(t *testing.T) {
	_, err := ResolveAuthority(Declaration{
		Knob: knob, Value: "kaname-internal.kacho.svc:9091",
		TransportKnob: transportKnob, TransportRequired: true, TransportDeclared: false,
	})
	require.Error(t, err, "адрес без удостоверения обязан отвергаться")
	require.Contains(t, err.Error(), transportKnob,
		"текст отказа обязан называть НЕДОСТАЮЩУЮ половину пары")
}

// TestResolveAuthority_HalfAPairSilentOnAbsent — зеркало предыдущего.
//
// Удостоверение не требуется там, где обращаться не к кому: требование его на
// «не развёрнут» сделало бы законную посадку неподнимаемой.
func TestResolveAuthority_HalfAPairSilentOnAbsent(t *testing.T) {
	_, err := ResolveAuthority(Declaration{
		Knob: knob, Value: NotDeployed,
		TransportKnob: transportKnob, TransportRequired: true, TransportDeclared: false,
	})
	require.NoError(t, err)
}

// TestResolveAuthority_HalfAPairSilentWhereTransportIsNotRequired — второе
// зеркало: посадка, где проверяемый транспорт не требуется ни одному ребру.
//
// Без него отказ по паре был бы СТРОЖЕ, чем у всех остальных рёбер службы, и
// первым следствием стал бы неподнимаемый локальный стенд.
func TestResolveAuthority_HalfAPairSilentWhereTransportIsNotRequired(t *testing.T) {
	a, err := ResolveAuthority(Declaration{
		Knob: knob, Value: "kaname-internal.kacho.svc:9091",
		TransportKnob: transportKnob, TransportRequired: false, TransportDeclared: false,
	})
	require.NoError(t, err)
	require.True(t, a.Deployed())
}

// TestResolveAuthority_UnsetIsRefusedRegardlessOfPosture — незаданное объявление
// режимом НЕ смягчается.
//
// Отдельная проба, потому что смягчение по посадке — ровно та ошибка, которую
// легко сделать заодно: транспорт от режима зависит, выбор оператора — нет.
func TestResolveAuthority_UnsetIsRefusedRegardlessOfPosture(t *testing.T) {
	for _, required := range []bool{true, false} {
		_, err := ResolveAuthority(Declaration{
			Knob: knob, TransportKnob: transportKnob, TransportRequired: required,
		})
		require.Error(t, err, "незаданное объявление отвергается и при TransportRequired=%v", required)
	}
}

// TestResolveAuthority_TypoDoesNotBecomeAnAddress — опечатка в значении-слове
// отвергается СТАРТОМ, а не превращается в адрес.
//
// Иначе близкое-но-не-то написание уезжает в дозвон и приезжает арендатору
// отказом UNAVAILABLE, обещающим временность, — то есть «повтори позже» на
// посадку, которая не станет рабочей никогда.
//
// Входы — настоящие промахи оператора: разделитель не тот, регистр не тот,
// слово из чужого словаря. Орфографическая опечатка в этот перечень не годится:
// её ловит линтер дерева, и проба падала бы на своих же данных.
func TestResolveAuthority_TypoDoesNotBecomeAnAddress(t *testing.T) {
	for _, raw := range []string{"not_deployed", "NOT-DEPLOYED", "notdeployed", "none", "off", "false"} {
		_, err := ResolveAuthority(Declaration{
			Knob: knob, Value: raw,
			TransportKnob: transportKnob, TransportDeclared: true,
		})
		require.Error(t, err, "значение %q не адрес и не «не развёрнут»", raw)
		require.Contains(t, err.Error(), knob)
	}
}

// TestResolveAuthority_RefusalNamesBothLegalValues — контроль текста отказа.
//
// Отказ обязан восстанавливать следующий шаг оператора (ban #18): назвать, что
// именно вписать.
func TestResolveAuthority_RefusalNamesBothLegalValues(t *testing.T) {
	_, err := ResolveAuthority(Declaration{Knob: knob, TransportKnob: transportKnob})
	require.Error(t, err)
	msg := err.Error()
	require.True(t, strings.Contains(msg, "host:port"),
		"отказ обязан назвать форму адреса, получено: %s", msg)
	require.Contains(t, msg, NotDeployed)
}
