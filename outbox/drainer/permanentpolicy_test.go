// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Травление покупает РОВНО ОДНО — разблокировку партиции. Там, где партиции
// нет, оно не покупает ничего и теряет намерение.
//
// Эти пробы закрепляют решение по kacho#455 на уровне ЕДИНСТВЕННОЙ точки
// принятия решения, а не на уровне метки строки в базе: место, где строка
// помечается, требует транзакции и поэтому проверяется интеграционно, а вопрос
// «что вообще полагается сделать» — чистый и обязан быть отвечен без базы.

// TestDecideDispositionByClassAndPolicy — таблица решений целиком.
//
// Отрицание стоит в паре с положительным контролем НАМЕРЕННО: без строки, где
// постоянный отказ ОТРАВЛЯЕТ, проба «не отравляет» зеленела бы на реализации,
// которая не отравляет никогда.
func TestDecideDispositionByClassAndPolicy(t *testing.T) {
	cases := []struct {
		name   string
		class  Class
		policy PermanentPolicy
		want   Disposition
	}{
		{"успех доставляется при любой политике", ClassSuccess, PoisonPermanent, DispositionDeliver},
		{"успех доставляется и при повторе", ClassSuccess, RetryPermanent, DispositionDeliver},
		{"уже применено — доставлено", ClassAlreadyApplied, PoisonPermanent, DispositionDeliver},
		{"уже применено — доставлено и при повторе", ClassAlreadyApplied, RetryPermanent, DispositionDeliver},
		{"временный отказ повторяется", ClassTransient, PoisonPermanent, DispositionRetry},
		{"временный отказ повторяется и при повторе", ClassTransient, RetryPermanent, DispositionRetry},

		// Положительный контроль: умолчание не изменено. Очередь с партицией
		// обязана травить — иначе постоянный отказ заклинивает партицию навсегда,
		// и это ровно тот инцидент, из которого выведена сегодняшняя
		// классификация.
		{"постоянный отказ травит при умолчании", ClassPermanent, PoisonPermanent, DispositionPoison},

		// Собственно предмет kacho#455.
		{"постоянный отказ повторяется при объявленной политике", ClassPermanent, RetryPermanent, DispositionRetry},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.class, tc.policy); got != tc.want {
				t.Errorf("Decide(%v, %v) = %v, ожидалось %v",
					tc.class, tc.policy, got, tc.want)
			}
		})
	}
}

// TestZeroValuePolicyPoisons — нулевое значение политики обязано означать
// СЕГОДНЯШНЕЕ поведение.
//
// Проба существует потому, что иначе введение поля молча сменило бы поведение
// всех уже провязанных очередей: ни одна из них политики не объявляет, и
// нулевое значение — единственное, что они получат.
func TestZeroValuePolicyPoisons(t *testing.T) {
	var unset PermanentPolicy
	if got := Decide(ClassPermanent, unset); got != DispositionPoison {
		t.Fatalf("нулевая политика дала %v — умолчание сменилось молча, "+
			"уже провязанные очереди перестали травить", got)
	}
}

// TestRetryPermanentRejectedWithPartition — политика повтора и ключ порядка
// вместе НЕ СОБИРАЮТСЯ.
//
// Обоснование политики целиком опирается на отсутствие партиции: у
// коммутативного потока заклинивать нечего. С ключом порядка постоянный отказ
// заклинил бы свою партицию навсегда — то есть ровно тот исход, который
// травление и предотвращает. Собранная так пара не «менее удачна», она
// НЕВЕРНА, и отвергается при сборке, а не оговаривается в комментарии.
func TestRetryPermanentRejectedWithPartition(t *testing.T) {
	base := Config{Table: "kacho_x.some_outbox", Channel: "ch"}

	t.Run("повтор с ключом порядка отвергается", func(t *testing.T) {
		cfg := base
		cfg.PartitionColumn = "resource_id"
		cfg.PermanentPolicy = RetryPermanent
		err := cfg.Validate()
		if err == nil {
			t.Fatal("пара «повтор постоянного отказа + ключ порядка» принята — " +
				"постоянный отказ заклинит свою партицию навсегда")
		}
		if !containsAll(err.Error(), "PermanentPolicy", "PartitionColumn") {
			t.Errorf("отказ не называет обе половины пары: %v", err)
		}
	})

	// Три законных близнеца той же формы — без них проба ловила бы форму, а не
	// существо, и покраснела бы на любой сборке.
	t.Run("повтор без ключа порядка законен", func(t *testing.T) {
		cfg := base
		cfg.PermanentPolicy = RetryPermanent
		if err := cfg.Validate(); err != nil {
			t.Fatalf("коммутативная очередь с повтором отвергнута: %v", err)
		}
	})
	t.Run("травление с ключом порядка законно", func(t *testing.T) {
		cfg := base
		cfg.PartitionColumn = "resource_id"
		if err := cfg.Validate(); err != nil {
			t.Fatalf("упорядоченная очередь с травлением отвергнута: %v", err)
		}
	})
	t.Run("травление без ключа порядка законно", func(t *testing.T) {
		cfg := base
		if err := cfg.Validate(); err != nil {
			t.Fatalf("коммутативная очередь с травлением отвергнута: %v", err)
		}
	})
}

// TestDecodeFailurePoisonsUnderEitherPolicy — отказ РАЗБОРА травит всегда.
//
// Политика повтора относится к отказу ПРИМЕНЕНИЯ — к тому, что сказал сосед.
// Отказ разбора говорит о самой строке: её тело не станет разбираемым ни от
// какого события, и повтор такой строки — вечная работа без единого шанса на
// успех. Поэтому она травится при любой политике, а «травиться нечему»
// достигается тем, что такую строку НЕЛЬЗЯ ЗАПИСАТЬ (ограничения схемы), а не
// тем, что её терпят.
func TestDecodeFailurePoisonsUnderEitherPolicy(t *testing.T) {
	decodeErr := errors.Join(ErrPermanent, errors.New("invalid json payload"))

	for _, policy := range []PermanentPolicy{PoisonPermanent, RetryPermanent} {
		if got := DecideOutcome(decodeErr, nil, policy); got != DispositionPoison {
			t.Errorf("политика %v: отказ разбора дал %v, ожидалось отравление",
				policy, got)
		}
	}

	// Законный близнец: без отказа разбора та же политика повтора НЕ травит.
	// Без него проба зеленела бы на реализации, травящей всегда.
	permanentApply := errors.Join(ErrPermanent, errors.New("target rejected"))
	if got := DecideOutcome(nil, permanentApply, RetryPermanent); got != DispositionRetry {
		t.Errorf("отказ ПРИМЕНЕНИЯ при политике повтора дал %v, ожидался повтор", got)
	}

	// И успех остаётся успехом при обоих nil.
	if got := DecideOutcome(nil, nil, RetryPermanent); got != DispositionDeliver {
		t.Errorf("успех дал %v", got)
	}
}

// TestPermissionDeniedRetriedUnderRetryPolicy — сквозь классификатор, а не мимо
// него.
//
// Проба идёт от НАСТОЯЩЕЙ ошибки соседа, а не от готового класса: без этого
// связка «классификация → решение» осталась бы непроверенной, и решение
// оказалось бы верным для класса, которого сосед не производит.
func TestPermissionDeniedRetriedUnderRetryPolicy(t *testing.T) {
	err := status.Error(codes.PermissionDenied, "forwarded identity not trusted")

	if got := Decide(Classify(err), PoisonPermanent); got != DispositionPoison {
		t.Fatalf("контроль: отказ в правах при умолчании дал %v, ожидалось отравление", got)
	}
	if got := Decide(Classify(err), RetryPermanent); got != DispositionRetry {
		t.Fatalf("отказ в правах у очереди без возврата дал %v — намерение потеряно "+
			"навсегда, хотя причина снимается перекатом", got)
	}
}

// TestErrPermanentRetriedUnderRetryPolicy — то же для явно обёрнутого
// постоянного отказа применения.
func TestErrPermanentRetriedUnderRetryPolicy(t *testing.T) {
	err := errors.Join(ErrPermanent, errors.New("target rejected the request"))

	if got := Decide(Classify(err), PoisonPermanent); got != DispositionPoison {
		t.Fatalf("контроль: ErrPermanent при умолчании дал %v", got)
	}
	if got := Decide(Classify(err), RetryPermanent); got != DispositionRetry {
		t.Fatalf("ErrPermanent у очереди без возврата дал %v", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
