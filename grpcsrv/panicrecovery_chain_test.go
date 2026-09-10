// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

import (
	"fmt"
	"log/slog"
	"os"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// childChain — цепочка, с которой поднимается дочерний процесс пробы. Обе
// половины пары отличаются РОВНО этой функцией и ничем больше.
func childChain(mode string) ([]grpc.UnaryServerInterceptor, []grpc.StreamServerInterceptor) {
	switch mode {
	case "without":
		return nil, nil
	case "with":
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		return []grpc.UnaryServerInterceptor{grpcsrv.UnaryPanicRecovery(logger)},
			[]grpc.StreamServerInterceptor{grpcsrv.StreamPanicRecovery(logger)}
	default:
		fmt.Fprintf(os.Stderr, "режим %q не поддержан\n", mode)
		os.Exit(5)
		return nil, nil
	}
}
