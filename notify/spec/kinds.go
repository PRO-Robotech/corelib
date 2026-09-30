// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import "github.com/PRO-Robotech/corelib/notify/form"

// AttrKinds — перечень видов атрибута, которые принимает формат шаблона.
// ЕДИНСТВЕННОЕ его объявление (З4, CX1-50): пробы B29 и B30 берут перечень
// отсюда, а не литералом. Каждый вид перечня notify/form обязана знать —
// сверка в одну сторону держится пробой form (typeset_test.go).
func AttrKinds() []form.Kind {
	return []form.Kind{
		form.KindText,
		form.KindSecret,
		form.KindPath,
		form.KindToken,
		form.KindTimestamp,
	}
}
