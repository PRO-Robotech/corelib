// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/pagetoken"
	"github.com/PRO-Robotech/corelib/subscription"
)

func requireCode(t *testing.T, err error, want codes.Code, mustName string) {
	t.Helper()
	if err == nil {
		t.Fatalf("вход принят, ожидался %s с упоминанием %q", want, mustName)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("отказ не является статусом gRPC: %v", err)
	}
	if st.Code() != want {
		t.Fatalf("код %s, ожидался %s: %v", st.Code(), want, err)
	}
	if mustName != "" && !contains(st.Message(), mustName) {
		t.Fatalf("отказ не называет %q: %s", mustName, st.Message())
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestAcceptsSubscriptionWithoutAnyAxis — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, и он первый.
// Незаданная ось не сужает ничем; подписка без осей законна (WATCH-1-02).
func TestAcceptsSubscriptionWithoutAnyAxis(t *testing.T) {
	f, err := vpcLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{})
	if err != nil {
		t.Fatalf("подписка без осей отвергнута: %v", err)
	}
	if len(f.Kinds) != 0 || f.ProjectID != "" || len(f.IDs) != 0 {
		t.Fatalf("незаданные оси превратились в сужение: %+v", f)
	}
	if len(f.Honored) != 0 {
		t.Fatalf("перечень честно отобранных осей непуст, хотя ни одна не задана: %v", f.Honored)
	}
}

// TestKindOutsideTheOwnersDictionaryIsRefused — вид вне закрытого словаря
// владельца есть ОТКАЗ, а не пустой поток (WATCH-1-03).
func TestKindOutsideTheOwnersDictionaryIsRefused(t *testing.T) {
	_, err := vpcLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{
		Kinds: []string{"vpc_network", "Черепаха"},
	})
	requireCode(t, err, codes.InvalidArgument, "kinds")
	if st, _ := status.FromError(err); !contains(st.Message(), "Черепаха") {
		t.Fatalf("отказ не называет отвергнутое значение: %s", st.Message())
	}
	// Положительный контроль: вид ИЗ словаря проходит. Словарь — типы объекта
	// (`vpc_network`), а `Network` есть слово ХРАНИЛИЩА этого владельца, и
	// клиенту оно не адресовано вовсе.
	f, err := vpcLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{Kinds: []string{"vpc_network"}})
	if err != nil {
		t.Fatalf("вид из словаря отвергнут: %v", err)
	}
	if !hasAxis(f.Honored, "kinds") {
		t.Fatalf("отобранная ось не названа честной: %v", f.Honored)
	}
}

// TestNameInIdsIsRefused — главная строка WATCH-1-04: `ids` — единственная ось,
// принимающая произвольную строку, и потому единственный путь протащить
// мутабельную адресацию через иммутабельную.
func TestNameInIdsIsRefused(t *testing.T) {
	_, err := vpcLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{
		Ids: []string{"test-vm-01"},
	})
	requireCode(t, err, codes.InvalidArgument, "ids")

	t.Run("пустая строка в ids", func(t *testing.T) {
		_, err := vpcLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{""}})
		requireCode(t, err, codes.InvalidArgument, "ids")
	})

	t.Run("годная форма, которой не отвечает ни один предмет, отказом НЕ является",
		func(t *testing.T) {
			f, err := vpcLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{
				Ids: []string{"net00000000000000000"},
			})
			if err != nil {
				t.Fatalf("годный идентификатор отвергнут: %v", err)
			}
			if !hasAxis(f.Honored, "ids") {
				t.Fatalf("ось ids не названа честной: %v", f.Honored)
			}
		})
}

// TestProjectAxisAgainstAnOwnerWithoutProjectDimension — вторая отказная сторона
// WATCH-1-32: у ЯКОРНОЙ оси исхода «названа, но не применена» не существует.
// Владелец, не умеющий по ней отобрать, ОТВЕРГАЕТ подписку.
func TestProjectAxisAgainstAnOwnerWithoutProjectDimension(t *testing.T) {
	j := vpcLikeJournal()
	j.Storage.Project = subscription.ProjectAbsent
	j.Mapping.Anchor = nil

	_, err := j.Accept(&subscriptionv1.SubscriptionRequest{ProjectId: "prj-1"})
	requireCode(t, err, codes.InvalidArgument, "project_id")

	// Положительный контроль в ДВЕ стороны: тот же владелец без этой оси
	// подписку открывает, а владелец с измерением — принимает ось и называет её
	// честно отобранной.
	if _, err := j.Accept(&subscriptionv1.SubscriptionRequest{}); err != nil {
		t.Fatalf("подписка без проектной оси отвергнута: %v", err)
	}
	for name, with := range map[string]subscription.Journal{
		"колонкой":       nlbLikeJournal(),
		"из отображения": vpcLikeJournal(),
	} {
		f, err := with.Accept(&subscriptionv1.SubscriptionRequest{ProjectId: "prj-1"})
		if err != nil {
			t.Fatalf("%s: проектная ось отвергнута: %v", name, err)
		}
		if !hasAxis(f.Honored, "project_id") {
			t.Fatalf("%s: якорная ось не названа честно отобранной: %v", name, f.Honored)
		}
	}
}

// TestHonoredFiltersNameFieldsOfTheContract — значения перечня суть ИМЕНА ПОЛЕЙ
// запроса, как они объявлены контрактом, а не второй словарь осей рядом.
func TestHonoredFiltersNameFieldsOfTheContract(t *testing.T) {
	f, err := nlbLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{
		Kinds:     []string{"vpc_network"},
		ProjectId: "prj-1",
		Ids:       []string{"net00000000000000000"},
	})
	if err != nil {
		t.Fatalf("подписка отвергнута: %v", err)
	}
	want := map[string]bool{"kinds": true, "project_id": true, "ids": true}
	if len(f.Honored) != len(want) {
		t.Fatalf("перечень честных осей = %v, ожидалось три", f.Honored)
	}
	for _, got := range f.Honored {
		if !want[got] {
			t.Fatalf("перечень называет %q — такого поля в контракте нет", got)
		}
	}
}

// TestStartHasThreeDistinguishableStates — WATCH-1-10: исход НЕЗАДАННОГО назван,
// а не подразумевается.
func TestStartHasThreeDistinguishableStates(t *testing.T) {
	t.Run("не задано означает текущий конец", func(t *testing.T) {
		s, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{})
		if err != nil {
			t.Fatalf("незаданное начало отвергнуто: %v", err)
		}
		if s.FromBeginning || s.Position != nil {
			t.Fatalf("незаданное начало разобрано не в текущий конец: %+v", s)
		}
	})
	t.Run("с начала журнала", func(t *testing.T) {
		s, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Anchor{
				Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
			},
		})
		if err != nil || !s.FromBeginning {
			t.Fatalf("«с начала» разобрано как %+v (%v)", s, err)
		}
	})
	t.Run("с текущего конца названо словом", func(t *testing.T) {
		s, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Anchor{
				Anchor: subscriptionv1.SubscriptionAnchor_CURRENT_END,
			},
		})
		if err != nil || s.FromBeginning || s.Position != nil {
			t.Fatalf("«с текущего конца» разобрано как %+v (%v)", s, err)
		}
	})
	t.Run("с позиции", func(t *testing.T) {
		token := pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: 9})
		s, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Position{Position: token},
		})
		if err != nil {
			t.Fatalf("позиция отвергнута: %v", err)
		}
		if s.Position == nil || s.Position.Settled != 9 {
			t.Fatalf("позиция разобрана как %+v", s.Position)
		}
	})
}

// TestStartRefusesUnnamedAndConstructedPositions — выбрав ветвь, вызывающий
// обязан её назвать; позиция, сконструированная клиентом, отвергается, а не
// принимается «как похожая» (WATCH-1-08).
func TestStartRefusesUnnamedAndConstructedPositions(t *testing.T) {
	t.Run("ветвь якоря выбрана, якорь не назван", func(t *testing.T) {
		_, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Anchor{
				Anchor: subscriptionv1.SubscriptionAnchor_SUBSCRIPTION_ANCHOR_UNSPECIFIED,
			},
		})
		requireCode(t, err, codes.InvalidArgument, "anchor")
	})
	t.Run("ветвь позиции выбрана, позиция пуста", func(t *testing.T) {
		_, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Position{Position: ""},
		})
		requireCode(t, err, codes.InvalidArgument, "position")
	})
	t.Run("позиция сконструирована клиентом", func(t *testing.T) {
		_, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Position{Position: "17"},
		})
		requireCode(t, err, codes.InvalidArgument, "position")
	})
	t.Run("чужая форма курсора того же пакета", func(t *testing.T) {
		_, err := subscription.AcceptStart(&subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Position{
				Position: pagetoken.Encode(pagetoken.Cursor{ID: "net-1"}),
			},
		})
		requireCode(t, err, codes.InvalidArgument, "position")
	})
}

func hasAxis(axes []string, name string) bool {
	for _, a := range axes {
		if a == name {
			return true
		}
	}
	return false
}
