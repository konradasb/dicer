// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/errdefs"
)

// An agent without a method was installed by a different daemon build when
// the instance booted. The caller is told how to fix that rather than that
// the method is unimplemented.
func TestAgentErrorExplainsMissingMethod(t *testing.T) {
	err := agentError("web", status.Error(codes.Unimplemented, "unknown method CopyIn"))

	wantClass(t, err, errdefs.ErrInvalidState)
	if !strings.Contains(err.Error(), "restart the instance") {
		t.Errorf("message = %q, want it to say how to update the agent", err)
	}
}

// Anything else the agent says reaches the client as it said it.
func TestAgentErrorPassesThrough(t *testing.T) {
	sent := toStatus(agentError("web", status.Error(codes.NotFound, "/srv/x: no such file or directory")))

	if s := status.Convert(sent); s.Code() != codes.NotFound || s.Message() != "/srv/x: no such file or directory" {
		t.Errorf("sent %v, want the agent's status unchanged", s)
	}
}
