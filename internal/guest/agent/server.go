// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package agent runs inside the virtual machine and serves the host's exec
// and file copy requests over vsock.
//
// vsock rather than the network deliberately: both keep working for an
// instance whose networking is broken or deliberately isolated.
package agent

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// server implements diceragentv1.AgentServiceServer.
type server struct{}

// Exec implements diceragentv1.AgentServiceServer. The first client message
// must be an ExecStart; subsequent messages carry stdin data or terminal
// resize events.
//
// The command lives as long as the stream: a client that disconnects takes
// its command with it rather than leaving it running unobserved.
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
