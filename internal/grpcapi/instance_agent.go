// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/errdefs"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// agent connects to the guest agent of the running instance with the given
// name or ID. The returned function closes the connection.
func (h *instanceHandler) agent(nameOrID string) (diceragentv1.AgentServiceClient, func(), error) {
	instance, err := h.definitions.Instance(nameOrID)
	if err != nil {
		return nil, nil, err
	}

	return h.instances.Agent(instance)
}

// agentError passes a guest agent's status through, explaining an
// unimplemented call as an agent that needs the instance restarted to be
// updated.
func agentError(name string, err error) error {
	if status.Code(err) == codes.Unimplemented {
		return errdefs.InvalidState(
			"instance %q runs a guest agent that does not match this daemon; restart the instance to update it", name)
	}

	return err
}
