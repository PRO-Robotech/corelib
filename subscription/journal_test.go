// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/subscription"
)

// vpcLikeJournal — журнал формы vpc/compute/geo: проектной колонки НЕТ, якорь
// даёт отображение. Именно эта форма у трёх владельцев из четырёх.
func vpcLikeJournal() subscription.Journal {
	return subscription.Journal{
		Channel: "vpc_outbox",
		Storage: subscription.Storage{
			Table:          "kacho_vpc.vpc_outbox",
			PositionColumn: "sequence_no",
			KindColumn:     "resource_kind",
			IDColumn:       "resource_id",
			ChangeColumn:   "event_type",
			PayloadColumn:  "payload",
			Project:        subscription.ProjectFromMapping,
			Retention:      subscription.RetainsEverything,
		},
		Mapping: subscription.Mapping{
			Kinds: map[string]subscription.Kind{
				"Network": {ObjectType: "vpc_network", Action: "vpc.networks.get"},
			},
			Changes: map[string]subscriptionv1.SubscriptionEvent_Change{
				"CREATED": subscriptionv1.SubscriptionEvent_CREATED,
				"UPDATED": subscriptionv1.SubscriptionEvent_UPDATED,
				"DELETED": subscriptionv1.SubscriptionEvent_DELETED,
			},
			Anchor: func(subscription.Row) (string, error) { return "prj-1", nil },
			State: func(subscription.Row) (*anypb.Any, subscription.StateAbsence, error) {
				return &anypb.Any{}, subscription.StateAbsenceUnnamed, nil
			},
		},
	}
}

// nlbLikeJournal — журнал с проектной колонкой: ось отбирается в SQL.
func nlbLikeJournal() subscription.Journal {
	j := vpcLikeJournal()
	j.Channel = "nlb_outbox"
	j.Storage.Table = "kacho_nlb.nlb_outbox"
	j.Storage.ChangeColumn = "action"
	j.Storage.ProjectColumn = "project_id"
	j.Storage.Project = subscription.ProjectInColumn
	j.Mapping.Anchor = nil
	return j
}

// TestJournalAcceptsBothMeasuredShapes — положительный контроль. Без него всякое
// отрицание ниже зеленело бы на проверке, отвергающей всё.
func TestJournalAcceptsBothMeasuredShapes(t *testing.T) {
	for name, j := range map[string]subscription.Journal{
		"якорь из отображения": vpcLikeJournal(),
		"якорь колонкой":       nlbLikeJournal(),
	} {
		if err := j.Validate(); err != nil {
			t.Errorf("%s: журнал отвергнут: %v", name, err)
		}
	}
}

// TestJournalRefusesUndeclaredAxes — незаявленная ось не доживает до
// обслуживания запроса: нулевое значение перечислителя объявлением НЕ является.
func TestJournalRefusesUndeclaredAxes(t *testing.T) {
	t.Run("проектное измерение не объявлено", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Storage.Project = subscription.ProjectDimensionUnset
		requireRefused(t, j, "Storage.Project")
	})
	t.Run("удержание журнала не объявлено", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Storage.Retention = subscription.RetentionUnset
		requireRefused(t, j, "Storage.Retention")
	})
}

// TestJournalRefusesInconsistentProjectDeclaration — заявление про якорь обязано
// сходиться с тем, чем якорь реально добывается. Разойдясь, они дали бы либо
// SQL по несуществующей колонке, либо тихо пустой якорь на каждой строке.
func TestJournalRefusesInconsistentProjectDeclaration(t *testing.T) {
	t.Run("колонка объявлена, а её имени нет", func(t *testing.T) {
		j := nlbLikeJournal()
		j.Storage.ProjectColumn = ""
		requireRefused(t, j, "Storage.Project")
	})
	t.Run("якорь из отображения, а отображение его не даёт", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Mapping.Anchor = nil
		requireRefused(t, j, "Anchor")
	})
	t.Run("якоря нет вовсе, а отображение его даёт", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Storage.Project = subscription.ProjectAbsent
		requireRefused(t, j, "Anchor")
	})
	t.Run("колонка и отображение сразу", func(t *testing.T) {
		j := nlbLikeJournal()
		j.Mapping.Anchor = func(subscription.Row) (string, error) { return "", nil }
		requireRefused(t, j, "Anchor")
	})
}

// TestJournalRefusesEmptyDictionaries — словарь видов и словарь рода изменения
// закрыты; пустой означал бы поток, из которого ничего не доставляется никогда.
func TestJournalRefusesEmptyDictionaries(t *testing.T) {
	t.Run("виды", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Mapping.Kinds = nil
		requireRefused(t, j, "Kinds")
	})
	t.Run("род изменения", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Mapping.Changes = nil
		requireRefused(t, j, "Changes")
	})
	t.Run("вид без типа объекта модели прав", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Mapping.Kinds = map[string]subscription.Kind{"Network": {Action: "vpc.networks.get"}}
		requireRefused(t, j, "ObjectType")
	})
	t.Run("род изменения, отображённый в неназванный", func(t *testing.T) {
		j := vpcLikeJournal()
		j.Mapping.Changes = map[string]subscriptionv1.SubscriptionEvent_Change{
			"CREATED": subscriptionv1.SubscriptionEvent_CHANGE_UNSPECIFIED,
		}
		requireRefused(t, j, "CHANGE_UNSPECIFIED")
	})
}

// TestJournalRefusesUnsafeIdentifiers — имена таблицы и колонок уезжают в текст
// запроса. Отвергать их надо У КОНСТРУКТОРА, а не экранировать на месте: тогда
// негодного имени не существует ни на одном пути.
func TestJournalRefusesUnsafeIdentifiers(t *testing.T) {
	bad := []string{
		`vpc_outbox"; DROP TABLE x; --`,
		"vpc outbox",
		"kacho_vpc.vpc_outbox.extra",
		"1outbox",
		"",
	}
	for _, name := range bad {
		j := vpcLikeJournal()
		j.Storage.Table = name
		if err := j.Validate(); err == nil {
			t.Errorf("имя таблицы %q принято", name)
		}
	}
	for _, name := range []string{`seq"; --`, "seq no", ""} {
		j := vpcLikeJournal()
		j.Storage.PositionColumn = name
		if err := j.Validate(); err == nil {
			t.Errorf("имя колонки %q принято", name)
		}
	}
	// Положительный контроль: законное имя без схемы и со схемой проходит.
	for _, name := range []string{"compute_outbox", "kacho_vpc.vpc_outbox"} {
		j := vpcLikeJournal()
		j.Storage.Table = name
		if err := j.Validate(); err != nil {
			t.Errorf("законное имя %q отвергнуто: %v", name, err)
		}
	}
}

// TestJournalRefusesEmptyChannel — канал пробуждения обязан быть назван: без
// него поток живёт одним лишь холостым перепросом и выглядит исправным.
func TestJournalRefusesEmptyChannel(t *testing.T) {
	j := vpcLikeJournal()
	j.Channel = ""
	requireRefused(t, j, "Channel")
}

func requireRefused(t *testing.T, j subscription.Journal, mustName string) {
	t.Helper()
	err := j.Validate()
	if err == nil {
		t.Fatalf("журнал принят, хотя обязан быть отвергнут (ожидалось упоминание %q)", mustName)
	}
	if !strings.Contains(err.Error(), mustName) {
		t.Fatalf("отказ не называет %q: %v", mustName, err)
	}
	if errors.Is(err, errNever) {
		t.Fatal("недостижимо")
	}
}

var errNever = errors.New("недостижимо")
