// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package servicecontract

// admission.go — сборка оси потолка из ручек посадки.
//
// # Зачем отдельная функция, а не литерал в каждом корне
//
// Композиционных корней семь, и «взять ручки, подставить пол там, где посадка
// молчит» — одно решение, а не семь. Семь копий разъехались бы на первой же
// правке пола, и разъехались бы молча: процесс, взявший чужой пол, поднимается и
// отчитывается ровно так же.

import (
	"fmt"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// AdmissionFromPosture — величины ОБОИХ слушателей из ручек посадки.
//
// Пол платформы там, где посадка молчит; её собственные величины там, где она
// назвала ВЕСЬ набор; отказ там, где назвала часть либо назвала негодное.
//
// Отказ, а не дополнение полом: оператор, задавший темп и забывший
// одновременность, получил бы наполовину свои, наполовину чужие величины и
// считал бы предел выставленным. Отказ называет СЛУШАТЕЛЯ — искать причину
// оператор пойдёт в файл настроек, где, по его мнению, всё написано верно.
func AdmissionFromPosture(public, internal grpcsrv.AdmissionKnobs) (Admission, error) {
	pub, err := public.Resolve(grpcsrv.PlatformPublicAdmission())
	if err != nil {
		return Admission{}, fmt.Errorf("публичный слушатель: %w", err)
	}
	in, err := internal.Resolve(grpcsrv.PlatformInternalAdmission())
	if err != nil {
		return Admission{}, fmt.Errorf("внутренний слушатель: %w", err)
	}
	return Admission{Public: pub, Internal: in}, nil
}
