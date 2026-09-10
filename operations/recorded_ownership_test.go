// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Решение о владении по ПРОЧИТАННОЙ строке — правила закреплены поимённо.
//
// Каждое правило утверждается ПАРОЙ: вход, на котором оно срабатывает, и
// соседний вход, на котором не срабатывает. Одностороннее утверждение зеленеет
// на предикате, отвергающем всё, а этот предикат — решение о доступе, поэтому
// «отвергает всё» здесь неотличимо от «работает» ровно до первого арендатора.
func TestCheckRecordedOwnership(t *testing.T) {
	tenant := Principal{Type: "user", ID: "usr-1"}

	cases := []struct {
		name     string
		caller   Principal
		recorded Principal
		allow    bool
	}{
		{
			name:     "владелец читает свою",
			caller:   tenant,
			recorded: tenant,
			allow:    true,
		},
		{
			name:     "чужая арендаторская не читается",
			caller:   tenant,
			recorded: Principal{Type: "user", ID: "usr-2"},
		},
		{
			// Совпадения ОДНОГО id мало: пространства id разных видов принципала
			// пересекаться не обязаны, и равенство по одному полю сделало бы
			// служебную учётку владельцем пользовательской операции.
			name:     "совпал id, разошёлся вид принципала — не читается",
			caller:   Principal{Type: "service_account", ID: "usr-1"},
			recorded: tenant,
		},
		{
			name:   "безымянный вызывающий: пары нет",
			caller: Principal{}, recorded: tenant,
		},
		{
			// Именованная анонимность непуста, поэтому без отдельного условия она
			// проходит гейт «личность извлеклась» и дальше совпадает САМА С СОБОЙ.
			// Ключ у безымянных общий по построению — «свой» означало бы «любой».
			name:     "безымянный вызывающий: имя есть, личности нет",
			caller:   Principal{Type: "system", ID: AnonymousPrincipalID},
			recorded: Principal{Type: "system", ID: AnonymousPrincipalID},
		},
		{
			// Отличие от полосы владельца, где обхода нет и быть не должно.
			// Здесь это второй слой: строку уже отдал владелец, применивший своё
			// сужение, поэтому пропуск ничего не расширяет.
			name:     "внутренняя личность читает чужую",
			caller:   SystemPrincipal(),
			recorded: tenant,
			allow:    true,
		},
		{
			name:     "арендатор не читает строку внутренней личности",
			caller:   tenant,
			recorded: SystemPrincipal(),
		},
		{
			// Строка, записанная до появления учёта владельцев либо безымянным
			// запросом: настоящий владелец неизвестен, а «неизвестен» не значит
			// «ничей». Внутренняя личность её по-прежнему читает (условие выше).
			name:     "арендатор не читает строку без именуемого владельца",
			caller:   tenant,
			recorded: Principal{},
		},
		{
			name:     "внутренняя личность читает строку без владельца",
			caller:   SystemPrincipal(),
			recorded: Principal{},
			allow:    true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckRecordedOwnership(c.caller, c.recorded)
			if c.allow {
				if err != nil {
					t.Fatalf("доступ отвергнут там, где он законен: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("доступ разрешён там, где он запрещён")
			}
			// Отказ обязан быть ОДНИМ на все причины: различать в нём «твоя
			// личность не извлеклась», «у строки нет владельца» и «владелец
			// другой» значило бы рассказывать вызывающему о чужой строке.
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.PermissionDenied {
				t.Fatalf("форма отказа полосы изменилась: %v", err)
			}
			if st.Message() != "permission denied" {
				t.Fatalf("текст отказа полосы изменился: %q", st.Message())
			}
		})
	}
}

// Порядок условий несущий: личность вызывающего судится ДО полей строки.
//
// Иначе операция без именуемого владельца стала бы читаемой всем — безымянный
// вызывающий дошёл бы до условия «у строки нет владельца» и получил бы отказ по
// нему же, но условие «внутренняя личность проходит» стоит РАНЬШЕ, и при
// перестановке безымянный с типом `system` прошёл бы гейт анонимности вовсе.
func TestCallerIsJudgedBeforeTheRecordedRow(t *testing.T) {
	// Безымянный вызывающий, у которого совпадает всё, что можно совпасть.
	anon := Principal{Type: "system", ID: AnonymousPrincipalID}
	if err := CheckRecordedOwnership(anon, anon); err == nil {
		t.Fatal("безымянный вызывающий признан владельцем строки, записанной безымянным " +
			"запросом: ключ у них общий по построению, и «свой» означает «любой»")
	}
	// Положительный контроль: тот же порядок пропускает законного владельца.
	tenant := Principal{Type: "user", ID: "usr-1"}
	if err := CheckRecordedOwnership(tenant, tenant); err != nil {
		t.Fatalf("порядок условий отвергает законного владельца: %v", err)
	}
}

// Внутренняя личность узнаётся ПО SystemPrincipal(), а не по двум словам.
func TestIsSystemReadsTheDeclaredIdentity(t *testing.T) {
	if !SystemPrincipal().IsSystem() {
		t.Fatal("объявленная внутренняя личность себя не узнаёт")
	}
	// Косметическое поле решением о личности не является.
	bare := Principal{Type: SystemPrincipal().Type, ID: SystemPrincipal().ID}
	if !bare.IsSystem() {
		t.Fatal("личность перестала узнаваться без косметического имени")
	}
	for _, p := range []Principal{
		{Type: "user", ID: "usr-1"},
		{Type: "system", ID: AnonymousPrincipalID},
		{Type: "system", ID: ""},
		{Type: "", ID: "bootstrap"},
	} {
		if p.IsSystem() {
			t.Fatalf("внутренней личностью признан посторонний принципал: %+v", p)
		}
	}
	// Аноним и внутренняя личность — РАЗНЫЕ предикаты, и путать их нельзя:
	// первый не называет никого, вторая называет вполне определённого
	// вызывающего и остаётся владельцем своих операций.
	if SystemPrincipal().IsAnonymous() {
		t.Fatal("объявленная внутренняя личность признана анонимной — она владеет своими операциями")
	}
}
