// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicecontract_test

// admission_refusal_test.go — ось потолка темпа: отказы дескриптора.
//
// # Почему ось, а не поле с умолчанием
//
// Механизм потолка существовал в фундаменте и был провязан у ОДНОГО слушателя
// из десяти. Поле с умолчанием эту историю повторило бы: следующий сервис
// поднялся бы без потолка и выглядел бы точно так же, как с потолком, — а
// заметить это можно было бы только переписью, то есть помня о ней.
//
// Ось с отвергающим конструктором убирает «забыл» как исход: композиционный
// корень обязан сказать про потолок ХОТЬ ЧТО-ТО, и это «что-то» судится здесь.
//
// Каждая проба — ПАРА: инъекция краснеет и называет ось, законный близнец той
// же формы молчит.

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// admissionPair — законный набор обоих листенеров: пол платформы.
func admissionPair() servicecontract.Admission {
	return servicecontract.Admission{
		Public:   grpcsrv.PlatformPublicAdmission(),
		Internal: grpcsrv.PlatformInternalAdmission(),
	}
}

// TestAdmissionAxisUndeclaredRefusesStart — ось не объявлена вовсе.
//
// Это и есть предмет #771: молчание композиционного корня означало «потолка
// нет», и отличить его от «потолок есть» было нечем.
func TestAdmissionAxisUndeclaredRefusesStart(t *testing.T) {
	s := lawful()
	s.Admission = servicecontract.Axis[servicecontract.Admission]{}
	refuses(t, s, "Admission")
}

// TestAdmissionAxisDeclaredIsAccepted — законный близнец: объявленная ось
// принимается. Без него отрицание выше зеленело бы на конструкторе, который
// отвергает всё подряд.
func TestAdmissionAxisDeclaredIsAccepted(t *testing.T) {
	s := lawful()
	s.Admission = servicecontract.Value(admissionPair())
	if _, err := servicecontract.New(s); err != nil {
		t.Fatalf("объявленная ось потолка отвергнута: %v", err)
	}
}

// TestAdmissionValueMustNotBeEmpty — ось объявлена значением, а значение пусто.
//
// Отдельный исход от необъявленной оси: `Value(Admission{})` на месте вызова
// выглядит объявлением, а несёт нули — то самое «не ограничиваем», которое
// механизм читает как отсутствие ограничителя.
func TestAdmissionValueMustNotBeEmpty(t *testing.T) {
	s := lawful()
	s.Admission = servicecontract.Value(servicecontract.Admission{})
	refuses(t, s, "Admission")
}

// TestAdmissionRefusesEachListenerSeparately — потолок одного листенера пуст,
// второго объявлен.
//
// Самый тихий вход: половина процесса ограничена, половина нет, и в журнале
// стоит «ограничитель взведён» — про ту половину, которая взведена.
func TestAdmissionRefusesEachListenerSeparately(t *testing.T) {
	t.Run("публичный пуст", func(t *testing.T) {
		s := lawful()
		p := admissionPair()
		p.Public = grpcsrv.AdmissionLimits{}
		s.Admission = servicecontract.Value(p)
		refuses(t, s, "Admission", "public")
	})
	t.Run("внутренний пуст", func(t *testing.T) {
		s := lawful()
		p := admissionPair()
		p.Internal = grpcsrv.AdmissionLimits{}
		s.Admission = servicecontract.Value(p)
		refuses(t, s, "Admission", "internal")
	})
}

// TestAdmissionRefusesASelfContradictingSet — всплеск ниже устойчивого темпа.
//
// Негодность сама по себе, отвергаемая в ЛЮБОМ режиме: ведро не наполняется до
// одного токена, и отвергается даже законный поток.
func TestAdmissionRefusesASelfContradictingSet(t *testing.T) {
	s := lawful()
	s.Mode = servicecontract.ModeDev
	p := admissionPair()
	p.Public.BurstFactor = 0.5
	s.Admission = servicecontract.Value(p)
	refuses(t, s, "Admission", "всплеск")
}

// TestAdmissionNotApplicableRefusedInProduction — изъятие на боевой посадке.
//
// «Потолка не надо» — законное заявление внутрипроцессной фикстуры и незаконное
// заявление боевого стенда: там оно означает «один арендатор вправе занять
// сервис чтением». Ось судится тем же режимом, что и остальная боевая строгость.
func TestAdmissionNotApplicableRefusedInProduction(t *testing.T) {
	s := lawful()
	s.Mode = servicecontract.ModeProduction
	s.Admission = servicecontract.NotApplicable[servicecontract.Admission](
		"фикстура в процессе: слушатели не выставлены наружу")
	refuses(t, s, "Admission")
}

// TestAdmissionNotApplicableAllowedOutsideProduction — тот же вход вне боевого
// режима принимается. Пара к пробе выше: без неё отказ выглядел бы запретом
// изъятия вообще, а он запрет ровно на боевой посадке.
func TestAdmissionNotApplicableAllowedOutsideProduction(t *testing.T) {
	s := lawful()
	s.Mode = servicecontract.ModeDev
	s.Admission = servicecontract.NotApplicable[servicecontract.Admission](
		"фикстура в процессе: слушатели не выставлены наружу")
	if _, err := servicecontract.New(s); err != nil {
		t.Fatalf("изъятие вне боевого режима отвергнуто: %v", err)
	}
}

// ── сборка оси из ручек посадки ─────────────────────────────────────────────

// TestPostureSilenceTakesTheFloorOnBothListeners — посадка молчит: оба
// слушателя получают СВОЙ пол, а не один общий.
//
// Утверждается пара, а не факт непустоты: набор, доехавший наполовину
// (публичный пол на оба слушателя), выглядит взведённым и душит наш собственный
// поток намерения.
func TestPostureSilenceTakesTheFloorOnBothListeners(t *testing.T) {
	got, err := servicecontract.AdmissionFromPosture(grpcsrv.AdmissionKnobs{}, grpcsrv.AdmissionKnobs{})
	if err != nil {
		t.Fatalf("молчание посадки отвергнуто: %v", err)
	}
	if got.Public != grpcsrv.PlatformPublicAdmission() {
		t.Fatalf("публичный слушатель получил %s, а пол платформы — %s",
			got.Public, grpcsrv.PlatformPublicAdmission())
	}
	if got.Internal != grpcsrv.PlatformInternalAdmission() {
		t.Fatalf("внутренний слушатель получил %s, а пол платформы — %s",
			got.Internal, grpcsrv.PlatformInternalAdmission())
	}
}

// TestPostureOverrideWinsPerListener — посадка переопределяет ОДИН слушатель,
// второй остаётся на полу. Пара к пробе выше.
func TestPostureOverrideWinsPerListener(t *testing.T) {
	own := grpcsrv.AdmissionKnobs{ReadPerSec: 9, MutationPerSec: 4, BurstFactor: 3, InFlight: 7}
	got, err := servicecontract.AdmissionFromPosture(own, grpcsrv.AdmissionKnobs{})
	if err != nil {
		t.Fatalf("объявление посадки отвергнуто: %v", err)
	}
	if got.Public.ReadPerSec != 9 || got.Public.InFlight != 7 {
		t.Fatalf("величины посадки не доехали: %s", got.Public)
	}
	if got.Internal != grpcsrv.PlatformInternalAdmission() {
		t.Fatalf("нетронутый слушатель сдвинулся с пола: %s", got.Internal)
	}
}

// TestPartialPostureNamesTheListener — частичный набор отвергается, и отказ
// называет СЛУШАТЕЛЯ: без имени оператор ищет ось в обоих наборах.
func TestPartialPostureNamesTheListener(t *testing.T) {
	partial := grpcsrv.AdmissionKnobs{ReadPerSec: 9}
	if _, err := servicecontract.AdmissionFromPosture(partial, grpcsrv.AdmissionKnobs{}); err == nil {
		t.Fatal("частичный набор публичного слушателя принят")
	} else if !strings.Contains(err.Error(), "публичный") {
		t.Fatalf("отказ не называет слушателя: %v", err)
	}
	if _, err := servicecontract.AdmissionFromPosture(grpcsrv.AdmissionKnobs{}, partial); err == nil {
		t.Fatal("частичный набор внутреннего слушателя принят")
	} else if !strings.Contains(err.Error(), "внутренний") {
		t.Fatalf("отказ не называет слушателя: %v", err)
	}
}
