// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/errdefs"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// agent connects to the guest agent of a running instance. The returned
// function closes the connection.
func (h *instanceHandler) agent(name string) (diceragentv1.AgentServiceClient, func(), error) {
	inst, err := h.definitions.GetInstance(name)
	if err != nil {
		return nil, nil, err
	}

	agent, closeAgent, err := h.instances.Agent(inst)
	if err != nil {
		return nil, nil, err
	}
	return agent, closeAgent, nil
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
