// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"context"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/guest"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// Shutdown sends guest.ShutdownSignal to PID 1, which dicer-init and systemd
// both take as a request to shut down.
func (s *server) Shutdown(context.Context, *diceragentv1.ShutdownRequest) (*diceragentv1.ShutdownResponse, error) {
	if err := unix.Kill(1, guest.ShutdownSignal); err != nil {
		return nil, status.Errorf(codes.Internal, "signal PID 1: %v", err)
	}
	return &diceragentv1.ShutdownResponse{}, nil
}
