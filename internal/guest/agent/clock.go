// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"context"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// SetClock implements diceragentv1.AgentServiceServer: it steps the wall
// clock to the time given.
func (s *server) SetClock(_ context.Context, req *diceragentv1.SetClockRequest) (*diceragentv1.SetClockResponse, error) {
	if req.GetTime() == nil {
		return nil, status.Error(codes.InvalidArgument, "the request has no time")
	}

	ts := unix.NsecToTimespec(req.GetTime().AsTime().UnixNano())
	if err := unix.ClockSettime(unix.CLOCK_REALTIME, &ts); err != nil {
		return nil, status.Errorf(codes.Internal, "set the clock: %v", err)
	}
	return &diceragentv1.SetClockResponse{}, nil
}
