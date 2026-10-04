// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"errors"
	"fmt"
	"regexp"
)

// DirectoryRelations — закрытый набор отношений, по которым справочник
// адресов службы доступа проверяет право получателя (NTF-3 Р7, З27,
// CX3-13): единственное объявление, служба доступа его импортирует
// (направление corelib ← kaname). Значение — новый срез на каждый вызов:
// вызывающий не меняет набор у других.
func DirectoryRelations() []string { return []string{"v_get"} }

// Строка сигнала ленты в журнале подписки модуля (NTF-3 З10 п.5): ключ
// журнала, тип модели прав и форма объекта. Это объявление — источник для
// обеих половин записи сигнала: Go-половины (feed.Put, шаг 6; её ключ
// feed.JournalKey сверяет проба этого пакета) и SQL-половины, которую
// печатает notifygen.
const (
	FeedSignalKey        = "notification"
	FeedSignalObjectType = "notification_feed"
	// FeedSignalChange — род изменения строки сигнала словом платформы.
	// SQL-половина (функция resource-event) пишет строку сигнала тем словом
	// журнала модуля, которое его словарь переводит в этот род; слово
	// единственное, иначе notifygen отказывает.
	FeedSignalChange = "UPDATED"
)

// SignalRow — строка сигнала ленты модуля: ключ журнала Key, тип модели
// ObjectType и объект ObjectType:<модуль>.
type SignalRow struct {
	Key        string
	ObjectType string
	Object     string
}

// ErrModuleName — имя модуля не DNS-метка: объект notification_feed:<модуль>
// и метка метрик ленты такого имени не принимают.
var ErrModuleName = errors.New("notify/spec: module name is not a DNS label")

// moduleForm — DNS-метка; та же форма, что у имени модуля источника ленты
// (feed.Config.Module).
var moduleForm = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// FeedSignal — строка сигнала ленты модуля module. Имя не DNS-метка —
// ErrModuleName; строки нет.
func FeedSignal(module string) (SignalRow, error) {
	if !moduleForm.MatchString(module) {
		return SignalRow{}, fmt.Errorf("%w: %q", ErrModuleName, module)
	}
	return SignalRow{Key: FeedSignalKey, ObjectType: FeedSignalObjectType, Object: FeedSignalObjectType + ":" + module}, nil
}
