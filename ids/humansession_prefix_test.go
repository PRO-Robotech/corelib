// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import (
	"regexp"
	"testing"
)

// humanSessionAddressForm — форма идентификатора записи сессии человека,
// которую служба доступа выдаёт наружу перечнем своих сессий и принимает
// выходом из выбранной: `hss-` и 17 знаков крокфордова алфавита. Выписана
// ВНЕШНИМ фактом, а не выведена из генератора: иначе проба согласилась бы с
// любой формой, которую генератор произведёт.
var humanSessionAddressForm = regexp.MustCompile(`^hss-[0-9a-hjkmnp-tv-z]{17}$`)

// TestHumanSessionPrefix_InCanon — запись сессии человека адресуется формой
// `hss-<17>` (PRO-Robotech/corelib#101, заказчик PRO-Robotech/kaname#634):
// перечень своих сессий называет этот идентификатор, выход из выбранной сессии
// его принимает.
//
// Без записи в каноне дефис-приставок `validate.ResourceID` отвергал бы
// КОРРЕКТНЫЙ идентификатор, который служба сама и выдала, — тот же класс, что
// у `lim`, `mbr`, `ak` и `tfm`.
//
// Канон утверждается ЛИТЕРАЛОМ, а не через экспортируемую константу: иначе,
// пока приставка не заведена, файл не собрался бы, и «приставка не объявлена»
// стало бы неотличимо от «файл не компилируется».
func TestHumanSessionPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["hss"]; !ok {
		t.Errorf("дефис-приставка %q отсутствует в KnownHyphenPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный %q- идентификатор сессии человека", "hss", "hss")
	}
}

// TestHumanSessionPrefix_OnlyTheHyphenFormIsTheAddress — КОНТРОЛЬ формы: из
// двух генераторов адресную форму производит ровно один. Дефисная
// NewHyphenID(`hss`) ей соответствует, слитная NewID(`hss`) — нет. Пара меняет
// РОВНО ОДИН факт — генератор; приставка и тело у обоих одни. После записи в
// канон «идентификатор отвергнут» может означать только отсутствие записи, а не
// форму тела.
func TestHumanSessionPrefix_OnlyTheHyphenFormIsTheAddress(t *testing.T) {
	if id := NewHyphenID("hss"); !humanSessionAddressForm.MatchString(id) {
		t.Errorf("NewHyphenID(%q) = %q — не адресная форма сессии человека (%s)", "hss", id, humanSessionAddressForm)
	}
	if id := NewID("hss"); humanSessionAddressForm.MatchString(id) {
		t.Errorf("NewID(%q) = %q прошёл адресную форму %s — проба перестала различать слитную и дефисную формы",
			"hss", id, humanSessionAddressForm)
	}
}
