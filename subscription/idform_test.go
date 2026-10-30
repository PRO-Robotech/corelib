// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/corelib/subscription"
	"github.com/PRO-Robotech/corelib/subscription"
)

// Пробы оси `ids` против формы идентификатора, объявленной владельцем
// ([subscription.Kind.IDForm]). Предмет — стадия S0 приёмки kaname#1052
// (AID-31): идентификатор аккаунта, заданный клиентом строкой (`godjee`), не
// имеет формы маршрутизатора, и до объявления формы ось отвергала его как
// «invalid resource id».

// accountLikeForm — форма идентификатора аккаунта по Р2 приёмки kaname#1052:
// 3–30 знаков [a-z0-9-], первый — буква, последний — буква или цифра, двух
// дефисов подряд нет. Резерв владельца проба не воспроизводит: распознаватель
// здесь — подставной, его предмет — то, что сервер его СПРАШИВАЕТ.
func accountLikeForm(id string) bool {
	if len(id) < 3 || len(id) > 30 || strings.Contains(id, "--") {
		return false
	}
	for i, r := range id {
		letter := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		switch {
		case i == 0 && !letter:
			return false
		case i == len(id)-1 && !letter && !digit:
			return false
		case !letter && !digit && r != '-':
			return false
		}
	}
	return true
}

// accountOwnerJournal — владелец с двумя видами: аккаунт объявил свою форму,
// сеть — нет. Второй вид нужен близнецу «объявление чужого вида не влияет».
func accountOwnerJournal() subscription.Journal {
	j := vpcLikeJournal()
	j.Mapping.Kinds = map[string]subscription.Kind{
		"Network": {ObjectType: "vpc_network", Action: "vpc.networks.get"},
		"account": {ObjectType: "account", Action: "iam.accounts.list", IDForm: accountLikeForm},
	}
	return j
}

func requireExactRefusal(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("вход принят, ожидался INVALID_ARGUMENT %q", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("отказ не является статусом gRPC: %v", err)
	}
	if st.Code() != codes.InvalidArgument || st.Message() != want {
		t.Fatalf("отказ %s %q, ожидался INVALID_ARGUMENT %q", st.Code(), st.Message(), want)
	}
}

func requireIDsHonored(t *testing.T, f subscription.Filter, err error, want ...string) {
	t.Helper()
	if err != nil {
		t.Fatalf("подписка отвергнута: %v", err)
	}
	if !hasAxis(f.Honored, "ids") {
		t.Fatalf("ось ids не названа честной: %v", f.Honored)
	}
	if strings.Join(f.IDs, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("принятое сужение %q, ожидалось %q", f.IDs, want)
	}
}

// TestAccountID31_DeclaredFormOpensTheIdsAxis — AID-31 на уровне фундамента:
// владелец объявил форму вида, и идентификатор этой формы открывает подписку.
func TestAccountID31_DeclaredFormOpensTheIdsAxis(t *testing.T) {
	f, err := accountOwnerJournal().Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{"godjee"}})
	requireIDsHonored(t, f, err, "godjee")

	t.Run("форма маршрутизатора у того же владельца по-прежнему годна", func(t *testing.T) {
		// Объявление ДОПОЛНЯЕТ форму маршрутизатора: выданные платформой
		// идентификаторы не теряют подписки.
		f, err := accountOwnerJournal().Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{"net00000000000000000"}})
		requireIDsHonored(t, f, err, "net00000000000000000")
	})

	t.Run("строка вне формы любого вида отвергается прежним текстом", func(t *testing.T) {
		_, err := accountOwnerJournal().Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{"God Jee"}})
		requireExactRefusal(t, err, "ids: invalid resource id 'God Jee'")
	})

	t.Run("пустая строка отвергается прежним текстом", func(t *testing.T) {
		_, err := accountOwnerJournal().Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{""}})
		requireExactRefusal(t, err, "ids: empty identifier")
	})

	t.Run("одна строка вне формы отвергает весь перечень", func(t *testing.T) {
		_, err := accountOwnerJournal().Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{"godjee", "God Jee"}})
		requireExactRefusal(t, err, "ids: invalid resource id 'God Jee'")
	})
}

// TestAccountID31_OwnerWithoutDeclaredFormKeepsTheRouterRefusal — близнец:
// тот же вход у владельца, формы не объявившего, отвергается ровно как прежде.
// Объявление формы не превращается в «принять всё» по умолчанию.
func TestAccountID31_OwnerWithoutDeclaredFormKeepsTheRouterRefusal(t *testing.T) {
	_, err := vpcLikeJournal().Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{"godjee"}})
	requireExactRefusal(t, err, "ids: invalid resource id 'godjee'")
}

// TestAccountID31_FormOfAnotherKindDoesNotApply — форма принадлежит ВИДУ:
// подписка, сузившая виды до тех, что формы не объявили, судит идентификатор
// только формой маршрутизатора.
func TestAccountID31_FormOfAnotherKindDoesNotApply(t *testing.T) {
	_, err := accountOwnerJournal().Accept(&subscriptionv1.SubscriptionRequest{
		Kinds: []string{"vpc_network"},
		Ids:   []string{"godjee"},
	})
	requireExactRefusal(t, err, "ids: invalid resource id 'godjee'")

	t.Run("вид, объявивший форму, назван — идентификатор принят", func(t *testing.T) {
		f, err := accountOwnerJournal().Accept(&subscriptionv1.SubscriptionRequest{
			Kinds: []string{"vpc_network", "account"},
			Ids:   []string{"godjee"},
		})
		requireIDsHonored(t, f, err, "godjee")
	})
}

// TestDeclaredFormIsNeverAskedAboutTheEmptyString — пустая строка отвергается
// раньше формы владельца: даже форма, принимающая всё, её не пропускает.
func TestDeclaredFormIsNeverAskedAboutTheEmptyString(t *testing.T) {
	asked := 0
	j := vpcLikeJournal()
	j.Mapping.Kinds = map[string]subscription.Kind{
		"Network": {ObjectType: "vpc_network", Action: "vpc.networks.get", IDForm: func(string) bool {
			asked++
			return true
		}},
	}
	_, err := j.Accept(&subscriptionv1.SubscriptionRequest{Ids: []string{""}})
	requireExactRefusal(t, err, "ids: empty identifier")
	if asked != 0 {
		t.Fatalf("форму владельца спросили о пустой строке %d раз", asked)
	}
}
