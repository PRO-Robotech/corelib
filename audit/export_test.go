// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package audit

import "context"

// PassExcluding — проход, который сверх исхода называет строки, исключённые им
// из клейма к его концу. Наружу пакета не выходит: это шов пробы, а не контракт.
func (s *Shipper) PassExcluding(ctx context.Context) (PassResult, []string, error) {
	return s.pass(ctx)
}
