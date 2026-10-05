// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Package agent runs inside the guest and serves the host's exec, copy, probe,
// process listing, shutdown, clock and identity requests over vsock.
package agent

// server implements diceragentv1.AgentServiceServer.
type server struct{}
