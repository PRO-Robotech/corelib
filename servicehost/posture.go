// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicehost

import (
	"math"

	"github.com/PRO-Robotech/corelib/observability"
	"github.com/PRO-Robotech/corelib/servicecontract"
)

// postureUnresolved — форма слушателя, которой нет в перечне самоотчёта.
// Возвращается для дескриптора, по которому носитель ничего не поднимет
// (непринятый либо nil): конструктор записи ([observability.NewBootPosture]) её
// отвергает, и строка самоотчёта о непроверенном дескрипторе не пишется.
const postureUnresolved = observability.ListenerForm(math.MaxUint8)

// PostureOf — форма слушателя и признак «сервисов нет» для самоотчёта о
// посадке, выведенные из ТОГО ЖЕ значения дескриптора, которое корень передаёт
// в [Serve].
//
// Функция одна, и корень формы литералом не присваивает: обе величины записи
// приходят отсюда, поэтому строка самоотчёта не может разойтись с тем, что
// носитель поднимает. Сочетание «сервисов нет и слушатель внутренний» отсюда не
// выражается; конструктор записи — второй рубеж для прямой сборки.
//
//	HostPair         → (pair, ложь)
//	HostNoGRPC       → (pair, истина)
//	HostInternalOnly → (internal_only, ложь)
//
// Разбор — исчерпывающий switch без default: значение оси без ветки здесь
// даёт форму вне перечня, которую конструктор записи отвергает.
func PostureOf(d *servicecontract.Descriptor) (observability.ListenerForm, bool) {
	if d == nil || !d.Accepted() {
		return postureUnresolved, false
	}
	switch d.HostForm() {
	case servicecontract.HostPair:
		return observability.ListenerFormPair, false
	case servicecontract.HostNoGRPC:
		return observability.ListenerFormPair, true
	case servicecontract.HostInternalOnly:
		return observability.ListenerFormInternalOnly, false
	}
	return postureUnresolved, false
}
