// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

// ClosedSweepStatement — оператор уборки закрытых строк службы svc: проба
// УК82 читает его план, не выписывая текст оператора второй раз.
func ClosedSweepStatement(svc string) string { return closedSweepSQL(svc) }

// ExpireStatement — оператор истечения службы svc (проба УК83 строит из него
// инъекцию без шага l).
func ExpireStatement(svc string) string { return expireSQL(svc) }
