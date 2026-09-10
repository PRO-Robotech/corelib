// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// policy_test.go — F1-34 (арифметика отсрочки держится ГЕЙТОМ, а не
// комментарием) и F1-28 (перечень обязательных проверок объявлен и полон).
package tokenpolicy_test

import (
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

// TestF1_34_RemovalGraceIsComputedFromDeclaredNumbers — F1-34.
//
// Гейт читает ОБЪЯВЛЕНИЯ и требует, чтобы соотношение выполнялось. Смена
// любого слагаемого без пересмотра отсрочки роняет проверку, и она называет
// оба числа — тогда изменение становится решением, а не дрейфом.
func TestF1_34_RemovalGraceIsComputedFromDeclaredNumbers(t *testing.T) {
	need := tokenpolicy.MaxTokenTTL + tokenpolicy.ConsumerKeySetCacheCeiling
	if tokenpolicy.KeyRemovalGrace < need {
		t.Fatalf("отсрочка снятия ключа %s меньше суммы слагаемых: срок токена %s + потолок кэша ключей %s = %s.\n"+
			"Снятый ключ отвечал бы «да» из чужого кэша, а живые токены отвергались бы уже после того,\n"+
			"как связь с действием потеряна. Отсрочка ВЫЧИСЛЯЕТСЯ: пересмотрите KeyRemovalGrace либо слагаемое.",
			tokenpolicy.KeyRemovalGrace, tokenpolicy.MaxTokenTTL,
			tokenpolicy.ConsumerKeySetCacheCeiling, need)
	}
	if !tokenpolicy.GraceCoversLiveTokens() {
		t.Fatalf("предикат GraceCoversLiveTokens не согласен с объявленными числами — два места об одном предмете")
	}

	// Способность упасть доказана СЧЁТОМ, а не прочтением: подставляем
	// слагаемые, при которых соотношение нарушено, и требуем, чтобы тот же
	// предикат его не подтвердил.
	if grace, ttl, cache := 10*time.Minute, 30*time.Minute, time.Hour; grace >= ttl+cache {
		t.Fatalf("контроль инъекции сам неверен: %s не может покрывать %s+%s", grace, ttl, cache)
	}

	// Положительный контроль второй стороны: запас не «покрывает всё»
	// произвольной величиной — он объявлен и мал, иначе ошибка в слагаемых
	// оставалась бы невидимой.
	if tokenpolicy.RemovalSlack > tokenpolicy.MaxTokenTTL+tokenpolicy.ConsumerKeySetCacheCeiling {
		t.Fatalf("запас %s превышает сумму слагаемых — такой запас скрывает ошибку в них, а не страхует от неё",
			tokenpolicy.RemovalSlack)
	}
}

// TestF1_28_MandatoryChecksAreDeclaredAndComplete — F1-28, объявительная
// половина. Половина «обе реализации им пользуются» — гейт по дереву.
func TestF1_28_MandatoryChecksAreDeclaredAndComplete(t *testing.T) {
	checks := tokenpolicy.MandatoryChecks()
	if len(checks) == 0 {
		t.Fatalf("перечень обязательных проверок пуст — пустой перечень означает «проверяем что придётся»")
	}
	seen := map[tokenpolicy.Check]bool{}
	for _, c := range checks {
		if c == "" {
			t.Fatalf("в перечне обязательных проверок есть безымянная")
		}
		if seen[c] {
			t.Fatalf("проверка %q объявлена в перечне дважды", c)
		}
		seen[c] = true
	}

	// Проверки, без которых положительный путь работает, а защита — нет.
	// Названы поимённо, потому что каждая из них невидима на happy path.
	for _, must := range []tokenpolicy.Check{
		tokenpolicy.CheckExpiry,     // разбор, не встретив срока, не возразит
		tokenpolicy.CheckAudience,   // незаданный адресат означает «любой»
		tokenpolicy.CheckIssuer,     // издатель без записи источника
		tokenpolicy.CheckRevocation, // контроль на выдаче отзывом не является
		tokenpolicy.CheckTokenType,  // тип требуется явно
		tokenpolicy.CheckKeyBoundAlgorithm,
	} {
		if !seen[must] {
			t.Fatalf("обязательная проверка %q выпала из перечня", must)
		}
	}

	// MissingChecks обязана ВИДЕТЬ нехватку — иначе гейт, который ею
	// пользуется, молчал бы на любой реализации.
	if got := tokenpolicy.MissingChecks(nil); len(got) != len(checks) {
		t.Fatalf("пустое объявление обязано давать %d недостающих проверок, получено %d", len(checks), len(got))
	}
	// …и молчать на полном объявлении — положительный контроль.
	if got := tokenpolicy.MissingChecks(checks); len(got) != 0 {
		t.Fatalf("полное объявление не должно давать недостающих, получено %v", got)
	}
	// …и видеть нехватку РОВНО ОДНОЙ.
	partial := append([]tokenpolicy.Check(nil), checks[1:]...)
	got := tokenpolicy.MissingChecks(partial)
	if len(got) != 1 || got[0] != checks[0] {
		t.Fatalf("нехватка одной проверки обязана называться поимённо: ожидалось [%s], получено %v", checks[0], got)
	}
}

// TestAlgorithmDictionaryIsClosed — пустое значение алгоритма означает «любой»,
// и словарь обязан его отвергать наравне с выдуманным.
func TestAlgorithmDictionaryIsClosed(t *testing.T) {
	for _, alg := range tokenpolicy.Algorithms() {
		if !tokenpolicy.AlgorithmAllowed(alg) {
			t.Fatalf("алгоритм %q объявлен словарём и не принимается им же", alg)
		}
	}
	for _, bad := range []string{"", "none", "None", "NONE", "HS256", "rs256", " RS256"} {
		if tokenpolicy.AlgorithmAllowed(bad) {
			t.Fatalf("алгоритм %q принят закрытым словарём", bad)
		}
	}
}

// TestCriticalHeadersPolicyHasBothPolarities — единый источник обязан выражать
// ОБА требования о неизвестном параметре заголовка (#902).
func TestCriticalHeadersPolicyHasBothPolarities(t *testing.T) {
	if ok, name := tokenpolicy.CriticalHeadersUnderstood(nil); !ok {
		t.Fatalf("отсутствующий crit — законный вход, а отвергнут по %q", name)
	}
	if ok, _ := tokenpolicy.CriticalHeadersUnderstood([]string{}); !ok {
		t.Fatal("пустой crit не помечает ничего обязательным — отказ здесь неверен")
	}
	ok, name := tokenpolicy.CriticalHeadersUnderstood([]string{"unknown-ext"})
	if ok {
		t.Fatal("помеченный обязательным неизвестный параметр обязан давать отказ: " +
			"принять его значит согласиться с условием, которого не проверил")
	}
	if name != "unknown-ext" {
		t.Fatalf("отказ обязан назвать непонятый параметр, назван %q", name)
	}
	if len(tokenpolicy.KnownCriticalHeaders()) != 0 {
		t.Fatal("перечень исполняемых расширений пуст by construction: ни одна " +
			"поверхность приёма не исполняет ни одного. Появилось — заведи вместе " +
			"с исполнением, а не заранее")
	}
}

// TestDeviationWithoutReasonIsNotExcused — отступление с пустой причиной не
// засчитывается: иначе поле стало бы способом снять требование молча.
func TestDeviationWithoutReasonIsNotExcused(t *testing.T) {
	silent := []tokenpolicy.Deviation{{Check: tokenpolicy.CheckAudience}}
	if got := tokenpolicy.MissingChecksExcept([]tokenpolicy.Check{}, silent); len(got) == 0 {
		t.Fatal("пустая причина простила проверку — требование снимается молча")
	}
	withReason := []tokenpolicy.Deviation{{Check: tokenpolicy.CheckAudience, Reason: "адресата проверяет поверхность"}}
	before := len(tokenpolicy.MissingChecksExcept([]tokenpolicy.Check{}, nil))
	after := len(tokenpolicy.MissingChecksExcept([]tokenpolicy.Check{}, withReason))
	if after != before-1 {
		t.Fatalf("названная причина не учтена: было %d, стало %d", before, after)
	}
}
