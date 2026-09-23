// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// An agent without a method was installed by a different daemon build when
// the instance booted. The caller is told how to fix that rather than that
// the method is unimplemented.
func TestAgentStatusExplainsMissingMethod(t *testing.T) {
	err := agentStatus("web", status.Error(codes.Unimplemented, "unknown method CopyIn"))

	wantCode(t, err, codes.FailedPrecondition)
	if !strings.Contains(status.Convert(err).Message(), "restart the instance") {
		t.Errorf("message = %q, want it to say how to update the agent", status.Convert(err).Message())
	}
}

// Anything else the agent says passes through as it said it.
func TestAgentStatusPassesThrough(t *testing.T) {
	err := agentStatus("web", status.Error(codes.NotFound, "no such file"))

	wantCode(t, err, codes.NotFound)
}
