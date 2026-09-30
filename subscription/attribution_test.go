// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"testing"

	"github.com/PRO-Robotech/corelib/subscription"
)

// TestJournalAcceptsAttributionDeclarations — положительный контроль: журнал,
// назвавший колонки инициатора и времени и объявивший у вида форму имени и
// якорь, проходит; так же проходит журнал, не назвавший ничего из этого (форма
// журналов до миграций NTF-3 S1-A1 и объявлений S1-A3).
func TestJournalAcceptsAttributionDeclarations(t *testing.T) {
	j := nlbLikeJournal()
	j.Storage.InitiatorColumn = "initiator"
	j.Storage.OccurredAtColumn = "created_at"
	j.Mapping.Kinds = map[string]subscription.Kind{
		"Network":    {ObjectType: "vpc_network", Action: "vpc.networks.get", NameForm: subscription.NameFormDNS, Scope: subscription.ScopeProject},
		"Repository": {ObjectType: "registry_repository", Action: "registry.repositories.get", NameForm: subscription.NameFormNone, Scope: subscription.ScopeProject},
		"Pool":       {ObjectType: "vpc_address_pool", Action: "vpc.addressPools.get", NameForm: subscription.NameFormDNS, Scope: subscription.ScopeCluster},
	}
	if err := j.Validate(); err != nil {
		t.Fatalf("объявление отвергнуто: %v", err)
	}
	if err := nlbLikeJournal().Validate(); err != nil {
		t.Fatalf("журнал без объявлений атрибуции отвергнут: %v", err)
	}
}

// TestJournalRefusesUnsafeAttributionColumns — имена колонок инициатора и
// времени уезжают в текст запроса и судятся у конструктора так же, как прочие.
func TestJournalRefusesUnsafeAttributionColumns(t *testing.T) {
	for _, name := range []string{`initiator"; --`, "init iator", "1initiator"} {
		j := nlbLikeJournal()
		j.Storage.InitiatorColumn = name
		if err := j.Validate(); err == nil {
			t.Errorf("имя колонки инициатора %q принято", name)
		}
		j = nlbLikeJournal()
		j.Storage.OccurredAtColumn = name
		if err := j.Validate(); err == nil {
			t.Errorf("имя колонки времени %q принято", name)
		}
	}
}

// TestJournalRefusesUndeclaredStatesOfAKind — значение формы имени и якоря вне
// словаря отвергается: «такого состояния нет» не читается как «не объявлено».
func TestJournalRefusesUndeclaredStatesOfAKind(t *testing.T) {
	j := nlbLikeJournal()
	j.Mapping.Kinds = map[string]subscription.Kind{
		"Network": {ObjectType: "vpc_network", Action: "a", NameForm: subscription.NameForm(9)},
	}
	if err := j.Validate(); err == nil {
		t.Error("NameForm вне словаря принят")
	}
	j.Mapping.Kinds = map[string]subscription.Kind{
		"Network": {ObjectType: "vpc_network", Action: "a", Scope: subscription.Scope(9)},
	}
	if err := j.Validate(); err == nil {
		t.Error("Scope вне словаря принят")
	}
}

// TestJournalRefusesAProjectKindInAJournalWithoutAProjectDimension — вид,
// объявленный проектным, в журнале без проектного измерения: якорь брать
// неоткуда, два объявления противоречат друг другу.
func TestJournalRefusesAProjectKindInAJournalWithoutAProjectDimension(t *testing.T) {
	j := nlbLikeJournal()
	j.Storage.Project = subscription.ProjectAbsent
	j.Storage.ProjectColumn = ""
	j.Mapping.Kinds = map[string]subscription.Kind{
		"Network": {ObjectType: "vpc_network", Action: "a", Scope: subscription.ScopeProject},
	}
	if err := j.Validate(); err == nil {
		t.Error("проектный вид в журнале без проектного измерения принят")
	}
	// Близнец: тот же журнал, вид уровня кластера.
	j.Mapping.Kinds = map[string]subscription.Kind{
		"Network": {ObjectType: "vpc_network", Action: "a", Scope: subscription.ScopeCluster},
	}
	if err := j.Validate(); err != nil {
		t.Errorf("вид уровня кластера в журнале без проектного измерения отвергнут: %v", err)
	}
}
