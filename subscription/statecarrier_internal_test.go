// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// statecarrier_internal_test.go — проба ВНУТРИ пакета, и это её единственная
// причина здесь быть.
//
// Выбор носителя нагрузки не экспортируется: он есть частное решение сервера, у
// которого нет и не должно быть вызывающих снаружи. Открыть его ради пробы
// значило бы завести поверхность без единого потребителя, кроме неё.
package subscription

import (
	"errors"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
)

// TestStateCarrierTellsAJournalPropertyFromAFailure — ТАБЛИЦА ИЗ ЧЕТЫРЁХ ИСХОДОВ
// целиком, и каждая её строка утверждает ПАРУ: носитель и слово причины.
//
// # ПОЧЕМУ ЦЕЛИКОМ, А НЕ ОДНОЙ СТРОКОЙ
//
// Предмет пробы — РАЗЛИЧЕНИЕ, а не значение. Односторонняя проба («отсутствие даёт
// NOT_PRODUCED») зеленела бы на сервере, который называет NOT_PRODUCED ВСЕГДА, —
// то есть на подмене одной неразличимости другой. Полосы стоят рядом ровно
// потому, что до этой правки они были неразличимы: сервер сводил и отказ сборки,
// и честное «состояния не бывает» к «не удалось сериализовать».
//
// # ПОЧЕМУ БЕЗ БАЗЫ И БЕЗ ТРАНСПОРТА
//
// Утверждается свойство ОПЕРАТОРА ВЫБОРА, а не путь события. Путь события проверен
// там, где он и живёт, — интеграционными пробами владельцев; платить контейнером
// ещё и за таблицу значений было бы платой без предмета.
func TestStateCarrierTellsAJournalPropertyFromAFailure(t *testing.T) {
	packed, err := anypb.New(&subscriptionv1.SubscriptionOpened{Position: "p"})
	if err != nil {
		t.Fatalf("подготовка состояния: %v", err)
	}
	boom := errors.New("сборка не удалась")

	for _, c := range []struct {
		name string

		state   *anypb.Any
		absence StateAbsence
		err     error

		wantState  bool
		wantReason subscriptionv1.SubscriptionEvent_StateUnavailable_Reason
		wantNoisy  bool
	}{
		{
			name: "отказ сборки остаётся отказом сборки",
			err:  boom,
			// Состояние ЕСТЬ, собрать его не удалось. Разумное действие подписчика —
			// перечитать, и слово обязано его к этому звать.
			wantReason: subscriptionv1.SubscriptionEvent_StateUnavailable_NOT_SERIALIZABLE,
			wantNoisy:  true,
		},
		{
			name:      "собранное состояние едет как состояние",
			state:     packed,
			wantState: true,
		},
		{
			name:    "владелец не производит состояния — и это сказано своим словом",
			absence: StateNotProduced,
			// НЕ «не удалось сериализовать»: попытки не было, повтор ничего не
			// изменит, и подписчику надо идти за предметом, а не перечитывать поток.
			wantReason: subscriptionv1.SubscriptionEvent_StateUnavailable_NOT_PRODUCED,
		},
		{
			name:       "состояние больше не удерживается",
			absence:    StateNotRetained,
			wantReason: subscriptionv1.SubscriptionEvent_StateUnavailable_NOT_RETAINED,
		},
		{
			name:       "состояние есть, но не показывается",
			absence:    StateWithheld,
			wantReason: subscriptionv1.SubscriptionEvent_StateUnavailable_WITHHELD,
		},
		{
			name: "причина не названа — названа неназванной, а не подшита к соседней",
			// Ровно тот случай, ради которого контракт держит нулевое значение.
			// Подставить сюда любую содержательную причину значило бы завести
			// корзину «прочее» под чужим именем.
			wantReason: subscriptionv1.SubscriptionEvent_StateUnavailable_REASON_UNSPECIFIED,
			wantNoisy:  true,
		},
		{
			name:    "значение вне словаря отвечает как неназванное",
			absence: StateAbsence(200),
			// Сервер не вправе выбирать за владельца, какую из трёх содержательных
			// причин тот имел в виду.
			wantReason: subscriptionv1.SubscriptionEvent_StateUnavailable_REASON_UNSPECIFIED,
			wantNoisy:  true,
		},
		{
			name:      "отказ сильнее названной причины",
			absence:   StateNotProduced,
			err:       boom,
			wantState: false,
			// Отказ — наблюдение о случившемся, причина — объявление о задуманном.
			// Отдай причину — и единственный след поломки погас бы.
			wantReason: subscriptionv1.SubscriptionEvent_StateUnavailable_NOT_SERIALIZABLE,
			wantNoisy:  true,
		},
		{
			name:      "состояние сильнее названной причины, но противоречие ГРОМКО",
			state:     packed,
			absence:   StateNotProduced,
			wantState: true,
			wantNoisy: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			ev := &subscriptionv1.SubscriptionEvent{}
			complaint := setStateCarrier(ev, c.state, c.absence, c.err)

			if c.wantState {
				if ev.GetState() == nil {
					t.Fatalf("состояние не доехало (носитель: %v)", ev.GetStateUnavailable())
				}
			} else {
				if ev.GetState() != nil {
					t.Fatal("отдано состояние там, где его нет — подписчик вправе читать " +
						"непустую нагрузку как ПОЛНОЕ состояние предмета")
				}
				if ev.GetStateUnavailable() == nil {
					t.Fatal("носитель нагрузки не выбран вовсе — форма требует одну из двух ветвей")
				}
				if got := ev.GetStateUnavailable().GetReason(); got != c.wantReason {
					t.Fatalf("причина %v, ожидалась %v", got, c.wantReason)
				}
			}
			if (complaint != "") != c.wantNoisy {
				t.Fatalf("жалоба в журнал процесса = %q, ожидалась непустой: %v", complaint, c.wantNoisy)
			}
		})
	}
}

// TestEveryContractReasonHasExactlyOneProducer — ПЕРЕПИСЬ ПО СЛОВАРЮ КОНТРАКТА,
// и перечень для неё ВЫВОДИТСЯ, а не выписывается.
//
// # ЧТО ИМЕННО УТВЕРЖДАЕТСЯ
//
// У каждого слова контракта ровно одна роль, и ролей три:
//
//   - НЕНАЗВАННОЕ (`REASON_UNSPECIFIED`) — владелец причину не назвал. Своего
//     значения [StateAbsence] у него нет и быть не должно: назвать «не названо»
//     значило бы сделать забывчивость выразимой как решение;
//   - СЛОВО СЕРВЕРА (`NOT_SERIALIZABLE`) — отказ сборки. Владелец им не
//     распоряжается: он сообщает отказ ошибкой, а слово ему даёт сервер;
//   - СЛОВО ВЛАДЕЛЬЦА — остальные. У каждого ровно одно значение [StateAbsence],
//     и у каждого значения ровно одно слово.
//
// # ПОЧЕМУ ПЕРЕЧЕНЬ ВЫВОДИТСЯ ИЗ ПОРОЖДЁННОЙ КАРТЫ
//
// Выписанный перечень не растёт вместе с контрактом: слово, добавленное завтра,
// в него не попадёт, проба останется зелёной, и владельцу будет НЕЧЕМ его
// назвать — тихо, потому что отсутствие возможности не наблюдаемо ничем. Карта
// имён порождается вместе со стабами, поэтому новое слово контракта роняет эту
// пробу в тот же коммит, в котором заведено.
func TestEveryContractReasonHasExactlyOneProducer(t *testing.T) {
	// Роли, распределённые НЕ владельцем. Обе названы поимённо, потому что обе
	// суть решения, а не пропуски.
	serverOwned := map[subscriptionv1.SubscriptionEvent_StateUnavailable_Reason]string{
		subscriptionv1.SubscriptionEvent_StateUnavailable_REASON_UNSPECIFIED: "владелец причину не назвал",
		subscriptionv1.SubscriptionEvent_StateUnavailable_NOT_SERIALIZABLE:   "отказ сборки; слово даёт сервер по ошибке владельца",
	}

	// Обход значений [StateAbsence] ограничен сверху с запасом: перечислимого
	// типа в Go нет, и перебор — единственный способ спросить «а есть ли ещё».
	// Запас намеренно велик относительно словаря (сегодня в нём четыре записи):
	// проба обязана увидеть значение, заведённое следующим, а не только уже
	// известные.
	// Счётчик объявлен СВОИМ типом, а не целым с приведением: приведение здесь
	// ничего не даёт, кроме находки анализатора и повода её подавить, — а
	// подавление в диалекте, которого никто не читает, дерево ловит своим гейтом.
	const absenceScanCeiling StateAbsence = 64
	byWord := make(map[subscriptionv1.SubscriptionEvent_StateUnavailable_Reason][]StateAbsence)
	for a := StateAbsence(0); a < absenceScanCeiling; a++ {
		if word, named := a.reason(); named {
			byWord[word] = append(byWord[word], a)
		}
	}

	names := subscriptionv1.SubscriptionEvent_StateUnavailable_Reason_name
	if len(names) == 0 {
		t.Fatal("словарь причин контракта пуст — перепись не состоялась, и «ноль находок» " +
			"здесь означало бы «ноль прочитанного»")
	}

	var ownerNameable int
	for num, word := range names {
		reason := subscriptionv1.SubscriptionEvent_StateUnavailable_Reason(num)
		if why, reserved := serverOwned[reason]; reserved {
			if producers := byWord[reason]; len(producers) != 0 {
				t.Errorf("слово %s (%s) назначено значению владельца %v — владелец получил бы "+
					"право объявить решением то, что решением не является", word, why, producers)
			}
			continue
		}
		ownerNameable++
		switch producers := byWord[reason]; len(producers) {
		case 1:
		case 0:
			t.Errorf("слову контракта %s не отвечает НИ ОДНО значение StateAbsence — владелец, "+
				"у которого этот исход наступил, назвать его не может, и подписчик прочтёт "+
				"«владелец забыл назвать»", word)
		default:
			t.Errorf("слову контракта %s отвечают значения %v — различение, ради которого "+
				"словарь объявлен закрытым, стёрто", word, producers)
		}
	}

	// Объём осмотренного — отдельным утверждением: без него «ни одной жалобы»
	// было бы неотличимо от «нечего было смотреть».
	t.Logf("слов контракта %d · из них за сервером %d · называемых владельцем %d · "+
		"значений StateAbsence со своим словом %d",
		len(names), len(serverOwned), ownerNameable, len(byWord))
	if ownerNameable == 0 {
		t.Fatal("называемых владельцем слов не осталось ни одного — назвать причину нечем")
	}
}
