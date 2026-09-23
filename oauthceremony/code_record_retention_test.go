// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// code_record_retention_test.go — запись ПОГАШЕННОГО кода авторизации нужна и
// после срока самого кода.
//
// Срок кода (Config.AuthorizationCodeLifespan, потолок
// tokenpolicy.MaxAuthorizationCodeTTL) — окно, в которое код годен к обмену.
// Повтор погашенного кода узнаётся по его записи — третьим исходом выборки
// AuthorizationCodeVault.FetchAuthorizationCode, — а не по сроку: движок
// сверяет повтор раньше срока, и мост срока погашенного кода не судит. Повтор
// отзывает семейство гранта, поэтому запись нужна, пока семейство может жить.
// Это говорят шапка tokenpolicy.MaxAuthorizationCodeTTL и контракт выборки, а
// держат пробы этого файла.
//
// Срок кода истекает в пробе не паузой на часах, а старением записи
// (memoryPorts.ageCodeRecord): пауза делала исход зависимым от процессора —
// первый обмен, в который входит проверка секрета клиента, обязан был уложиться
// в короткий срок кода, и на медленной машине код истекал раньше обмена.
package oauthceremony_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
	"github.com/PRO-Robotech/corelib/tokenpolicy"
)

// codeRecordAge — на сколько проба старит записи кодов к повтору: потолок срока
// кода и минута сверх него. Срок кода, который собирает New, не длиннее
// потолка, поэтому состаренный код истёк при любых законных настройках. Минута
// — запас на шаг стенных часов назад между выдачей и повтором: срок записан по
// стенным часам.
const codeRecordAge = tokenpolicy.MaxAuthorizationCodeTTL + time.Minute

// requireFirstExchange — первый обмен кода прошёл. Отказ на коде, чей срок к
// этому мигу истёк, — не отказ продукта, а несозданное условие: проба не успела
// обменять код в его срок.
func requireFirstExchange(t *testing.T, store *memoryPorts, signature string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if expiresAt := store.codeExpiresAt(t, signature); !time.Now().UTC().Before(expiresAt) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: код истёк (%s) до первого обмена — проба не успела обменять его в срок: %v",
			expiresAt, err)
	}
	t.Fatalf("первый обмен отказал: %v", err)
}

// requireCodePastItsLifespan — предпосылка: к повтору срок кода истёк. Два
// признака. Срок, записанный у кода (его и читает движок), раньше нынешнего
// мига. И движок с этим согласен: нетронутый код, выданный тем же мигом и
// состаренный так же, отвергнут `invalid_grant` — не повтором и не «кода нет».
// Текст отказа истечения не называет (у движка это `invalid_token` в
// подробностях), поэтому предпосылка судится записанным сроком, а не текстом.
func requireCodePastItsLifespan(t *testing.T, store *memoryPorts, signature string, untouchedErr error) {
	t.Helper()
	expiresAt := store.codeExpiresAt(t, signature)
	if now := time.Now().UTC(); !expiresAt.Before(now) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: срок кода %s не истёк к %s (запись состарена на %s) — проба судила бы "+
			"повтор в пределах срока", expiresAt, now, codeRecordAge)
	}
	switch {
	case untouchedErr == nil:
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: нетронутый код, состаренный на %s, обменян — движок срока не судит",
			codeRecordAge)
	case errors.Is(untouchedErr, oauthceremony.ErrAuthorizationCodeConsumed),
		errors.Is(untouchedErr, oauthceremony.ErrGrantNotFound),
		!errors.Is(untouchedErr, oauthceremony.ErrInvalidGrant):
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: нетронутый код, состаренный на %s, отвергнут не сроком: случай %v (%v)",
			codeRecordAge, oauthceremony.CodeOf(untouchedErr), untouchedErr)
	}
}

// TestConsumedCodeRecordOutlivesTheCodeLifespan — погашенный код, предъявленный
// ПОСЛЕ своего срока, остаётся повтором и отзывает семейство, пока цела его
// запись. Близнец отличается ровно одним фактом: запись снята к повтору, как её
// сняла бы уборка по сроку кода, — и тот же повтор неотличим от неизвестного
// кода (`invalid_grant`), а семейство, выданное по коду, остаётся живым. Пара
// показывает, что различение держит запись, а не срок.
//
// Предпосылка обеих половин — срок кода к повтору истёк: нетронутый код,
// выданный тем же мигом и состаренный так же, отвергается истечением. Без неё
// пробы судили бы повтор в пределах срока (это предмет
// TestSequentialCodeReplayRevokesTheFamily) и не отличили бы уборку по сроку
// кода от уборки по пределу семейства.
func TestConsumedCodeRecordOutlivesTheCodeLifespan(t *testing.T) {
	for _, tc := range []struct {
		name  string
		swept bool
	}{
		{name: "запись погашенного кода цела — повтор отзывает семейство"},
		{name: "запись снята к повтору — неизвестный код, семейство живо", swept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())

			code, _ := issueCode(t, ceremony)
			untouched, _ := issueCode(t, ceremony)
			first, err := ceremony.Exchange(context.Background(), codeExchange(code))
			requireFirstExchange(t, store, opaqueDigest(code), err)
			grantID := grantOf(t, ceremony, first.AccessToken)

			store.ageCodeRecord(t, opaqueDigest(code), codeRecordAge)
			store.ageCodeRecord(t, opaqueDigest(untouched), codeRecordAge)
			_, err = ceremony.Exchange(context.Background(), codeExchange(untouched))
			requireCodePastItsLifespan(t, store, opaqueDigest(code), err)

			if tc.swept {
				store.sweepConsumedCode(t, opaqueDigest(code))
			}
			_, err = ceremony.Exchange(context.Background(), codeExchange(code))

			if !tc.swept {
				requireCodeReplayRefusal(t, err)
				requirePairDead(t, ceremony, store, grantID, first)
				return
			}
			if err == nil {
				t.Fatal("код, чья запись снята, обменян повторно")
			}
			if errors.Is(err, oauthceremony.ErrAuthorizationCodeConsumed) {
				t.Fatalf("без записи погашенного кода повтор всё же узнан (%v): близнец не отличается "+
					"от пробы фактом записи", err)
			}
			if !errors.Is(err, oauthceremony.ErrGrantNotFound) {
				t.Errorf("код без записи отвергнут случаем %v, ожидался %v (неизвестный код)",
					oauthceremony.CodeOf(err), oauthceremony.CodeGrantNotFound)
			}
			if wire := oauthceremony.CodeOf(err).WireCode(); wire != "invalid_grant" {
				t.Errorf("на проводе код без записи назван %q, ожидался \"invalid_grant\"", wire)
			}
			if store.familyRevoked(grantID) {
				t.Errorf("семейство гранта %s отозвано, хотя повтор узнавать было не по чему", grantID)
			}
			requireNotRevoked(t, store, grantID)
			if !introspect(t, ceremony, first.RefreshToken, oauthceremony.TokenKindRefresh).Active {
				t.Error("токен обновления, выданный по коду, назван негодным, хотя отзыва не было")
			}
		})
	}
}
