// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"

	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// ListInstanceProcesses asks a running instance's guest agent for the
// processes in the guest.
func (h *instanceHandler) ListInstanceProcesses(
	ctx context.Context, req *dicerdv1.ListInstanceProcessesRequest,
) (*dicerdv1.ListInstanceProcessesResponse, error) {
	agent, closeAgent, err := h.agent(req.GetName())
	if err != nil {
		return nil, err
	}
	defer closeAgent()

	resp, err := agent.ListProcesses(ctx, &diceragentv1.ListProcessesRequest{})
	if err != nil {
		return nil, agentError(req.GetName(), err)
	}

	processes := make([]*dicerdv1.Process, 0, len(resp.GetProcesses()))
	for _, p := range resp.GetProcesses() {
		processes = append(processes, &dicerdv1.Process{
			Pid:                 p.GetPid(),
			Ppid:                p.GetPpid(),
			User:                p.GetUser(),
			State:               p.GetState(),
			Name:                p.GetName(),
			Command:             p.GetCommand(),
			StartTime:           p.GetStartTime(),
			CpuTime:             p.GetCpuTime(),
			ResidentMemoryBytes: p.GetResidentMemoryBytes(),
		})
	}
	return &dicerdv1.ListInstanceProcessesResponse{Processes: processes}, nil
}
