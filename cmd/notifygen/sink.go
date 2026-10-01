// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
)

// sink — вывод с липкой ошибкой записи. Вывод -list — вход гейта ссылки на
// feed.Put; оборванная запись обязана дать красный код, а не короткое
// множество при коде 0 (CX1-35).
type sink struct {
	w   io.Writer
	err error
}

func (s *sink) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.err = err
	}
	return n, err
}

func (s *sink) printf(format string, a ...any) {
	// Ошибку запоминает Write; её судит run по завершении.
	_, _ = fmt.Fprintf(s, format, a...)
}
