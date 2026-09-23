// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// agent connects to the guest agent of a running instance. The returned
// function closes the connection.
func (h *instanceHandler) agent(name string) (diceragentv1.AgentServiceClient, func(), error) {
	inst, err := h.definitions.GetInstance(name)
	if err != nil {
		return nil, nil, toStatus(err)
	}

	agent, closeAgent, err := h.instances.Agent(inst)
	if err != nil {
		return nil, nil, toStatus(err)
	}
	return agent, closeAgent, nil
}

// agentStatus is what to tell a caller about an error from a guest agent. The
// agent answers in gRPC statuses already, so they pass through, except that a
// method the agent does not have means it was installed by a different
// daemon build when the instance booted, and a restart installs this one's.
func agentStatus(name string, err error) error {
	if status.Code(err) == codes.Unimplemented {
		return status.Errorf(codes.FailedPrecondition,
			"instance %q runs a guest agent that does not match this daemon; restart the instance to update it", name)
	}

	return err
}
