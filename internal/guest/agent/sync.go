// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"context"

	"golang.org/x/sys/unix"

	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// Sync implements diceragentv1.AgentServiceServer: it flushes every
// filesystem in the VM to its disks.
func (s *server) Sync(context.Context, *diceragentv1.SyncRequest) (*diceragentv1.SyncResponse, error) {
	unix.Sync()
	return &diceragentv1.SyncResponse{}, nil
}
