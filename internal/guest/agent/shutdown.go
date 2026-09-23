// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"context"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer/internal/guest"
	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// Shutdown implements diceragentv1.AgentServiceServer: it asks PID 1 to shut
// the guest down. PID 1 is dicer-init, which asks the workload to stop, or
// systemd, which powers the machine off; both take guest.ShutdownSignal to
// mean that.
func (s *server) Shutdown(context.Context, *diceragentv1.ShutdownRequest) (*diceragentv1.ShutdownResponse, error) {
	if err := unix.Kill(1, guest.ShutdownSignal); err != nil {
		return nil, status.Errorf(codes.Internal, "signal PID 1: %v", err)
	}
	return &diceragentv1.ShutdownResponse{}, nil
}
