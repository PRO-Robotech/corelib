// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import (
	"regexp"
	"testing"
)

// tokenFamilySchemaForm — форма ключа семейства токенов, которую держит
// ограничение схемы службы доступа: `tfm-` и 17 знаков крокфордова алфавита.
// Выписана здесь как ВНЕШНИЙ факт — то, что примет база, — а не выведена из
// генератора: иначе проба согласилась бы с любой формой, которую генератор
// произведёт.
var tokenFamilySchemaForm = regexp.MustCompile(`^tfm-[0-9a-hjkmnp-tv-z]{17}$`)

// TestTokenFamilyPrefix_InCanon — ключ семейства токенов (идентификатор
// гранта, который служба доступа чеканит крючком церемонии
// oauthceremony.Config.NewGrantID) адресуется формой `tfm-<17>`.
//
// Без записи в каноне дефис-префиксов `validate.ResourceID` отвергал бы
// КОРРЕКТНЫЙ идентификатор, который сама служба и выдала, — тот же класс, что
// у `lim`, `mbr` и `ak`.
//
// Канон утверждается ЛИТЕРАЛОМ, а не через экспортируемую константу: иначе,
// пока префикс не заведён, файл не собрался бы, и «префикс не объявлен» стало
// бы неотличимо от «файл не компилируется».
func TestTokenFamilyPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["tfm"]; !ok {
		t.Errorf("дефис-префикс %q отсутствует в KnownHyphenPrefixes — "+
			"validate.ResourceID отвергал бы КАЖДЫЙ корректный %q- ключ семейства токенов", "tfm", "tfm")
	}
}

// TestTokenFamilyPrefix_OnlyTheHyphenFormPassesTheSchema — из двух генераторов
// форму, которую принимает схема, производит ровно один.
//
// Слитная форма NewID(`tfm`) — `tfm<17>`, без дефиса, — ограничением схемы
// отвергается: служба, зовущая её в крючке чеканки, получила бы отказ вставки
// на каждой выдаче кода. Дефисная NewHyphenID(`tfm`) — принимается. Пара
// меняет РОВНО ОДИН факт — генератор; префикс и тело у обоих одни.
func TestTokenFamilyPrefix_OnlyTheHyphenFormPassesTheSchema(t *testing.T) {
	if id := NewHyphenID("tfm"); !tokenFamilySchemaForm.MatchString(id) {
		t.Errorf("NewHyphenID(%q) = %q — схема ключа семейства его не примет (%s)", "tfm", id, tokenFamilySchemaForm)
	}
	if id := NewID("tfm"); tokenFamilySchemaForm.MatchString(id) {
		t.Errorf("NewID(%q) = %q прошёл форму схемы %s — проба перестала различать слитную и дефисную формы",
			"tfm", id, tokenFamilySchemaForm)
	}
}
