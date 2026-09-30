// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/validate/nameform"
)

// Initiator — инициатор изменения в форме, которую несут строка журнала и
// событие подписки: `user:<id>`, `service_account:<id>` либо
// `system:<служба>-<роль>`.
//
// Значение строит только [InitiatorOf]; нулевое значение — пустая строка — не
// инициатор, и ни один путь фундамента его не принимает.
type Initiator string

// String отдаёт инициатора в написании журнала и провода.
func (i Initiator) String() string { return string(i) }

// ErrNoInitiator — принципал не переводится в инициатора: его нет, он
// системный (`{system, *}` — запасная и анонимная формы), его тип вне трёх,
// id пуст или не той приставки, либо имя компонента не DNS-метка.
//
// Текст отказа значения id не несёт: отказ уходит в журнал процесса и в
// ошибку транзакции, а id принципала — идентификатор личности.
var ErrNoInitiator = errors.New("auth: principal has no initiator form")

// Типы принципала, у которых есть форма инициатора. Написание — то, что кладёт
// извлечение личности (`operations.Principal.Type`).
const (
	principalTypeUser           = "user"
	principalTypeServiceAccount = "service_account"
)

// componentMark — признак компонента в id принципала формы [SystemPrincipalFor].
const componentMark = "system."

// InitiatorOf переводит принципал в инициатора. Это ЕДИНСТВЕННОЕ место о форме
// субъекта: помощник транзакции журнала, запись `intent_initiator`, адресация
// писем сбоя и `operation-failed` зовут его, а не судят форму сами (NTF-3, З3).
//
//	{user, usr…}               → user:usr…
//	{service_account, sva…}    → service_account:sva…
//	{user, system.<служба>-<роль>} (форма SystemPrincipalFor) → system:<служба>-<роль>
//	{system, *}, тип вне трёх, пустой id, id не той приставки → ErrNoInitiator
//
// Признак `system.` судится только у типа `user`: id пользователей и сервисных
// аккаунтов выпускается приставкой и телом крокфордова алфавита (`ids.NewID`,
// `ids.NewHyphenID`) и точки нести не может, поэтому признак ни с одним
// настоящим id не пересекается. Имя компонента после признака обязано быть
// DNS-меткой — той же формы, что `CHECK` колонки инициатора в журнале.
//
// Семейство id судится каталогом приставок `ids` в обеих формах записи
// (слитной и дефисной), а не `validate.ResourceID`: последний не различает
// семейства и пропускает пустую строку.
//
// Субъект сюда приходит из `operations.PrincipalFromContextOK`; запасной путь
// `PrincipalFromContext` (системный субъект при отсутствии) для инициатора не
// используется — отсутствие принципала решает вызывающий, до этой функции.
func InitiatorOf(p operations.Principal) (Initiator, error) {
	switch p.Type {
	case principalTypeUser:
		if label, ok := strings.CutPrefix(p.ID, componentMark); ok {
			if !nameform.OK(label) {
				return "", fmt.Errorf("%w: component name is not a DNS label", ErrNoInitiator)
			}
			return Initiator("system:" + label), nil
		}
		if !ofFamily(p.ID, ids.PrefixUser) {
			return "", fmt.Errorf("%w: user id is not of the user family", ErrNoInitiator)
		}
		return Initiator("user:" + p.ID), nil
	case principalTypeServiceAccount:
		if !ofFamily(p.ID, ids.PrefixServiceAccount) {
			return "", fmt.Errorf("%w: service account id is not of the service account family", ErrNoInitiator)
		}
		return Initiator("service_account:" + p.ID), nil
	default:
		// `system` сюда же: `{system, bootstrap}` и анонимная форма края —
		// не личность, от имени которой пишется журнал.
		return "", fmt.Errorf("%w: principal type %q has no initiator form", ErrNoInitiator, p.Type)
	}
}

func ofFamily(id, prefix string) bool {
	return ids.IsValid(id, prefix) || ids.IsValidHyphen(id, prefix)
}
