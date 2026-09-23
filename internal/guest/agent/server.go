// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package agent runs inside the guest and serves the host's exec, copy, probe
// and shutdown requests over vsock.
package agent

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// server implements diceragentv1.AgentServiceServer.
type server struct{}

// Exec runs a command for the life of the stream. The first client message
// must be an ExecStart; later ones carry stdin or terminal resizes.
func (s *server) Exec(stream diceragentv1.AgentService_ExecServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	start := req.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first message must be an ExecStart")
	}

	command := start.GetCommand()
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	slog.Info("exec", "command", command, "tty", start.GetTty(), "cwd", start.GetCwd(), "timeout", start.GetTimeoutSeconds())

	ctx := stream.Context()
	if start.GetTimeoutSeconds() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(start.GetTimeoutSeconds())*time.Second)
		defer cancel()
	}

	if start.GetTty() {
		return execTTY(ctx, stream, start, command)
	}

	return execPlain(ctx, stream, start, command)
}
