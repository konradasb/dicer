// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/types"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// guestSyncTimeout bounds how long a stop waits for the guest to flush its
// filesystems before ending its VMM regardless.
const guestSyncTimeout = 10 * time.Second

// Agent connects to a running instance's guest agent over vsock. The returned
// function closes the connection.
func (m *Manager) Agent(inst types.InstanceSpec) (diceragentv1.AgentServiceClient, func(), error) {
	rt, err := m.Runtime(inst)
	if err != nil {
		return nil, nil, err
	}
	if rt.State != types.StateRunning {
		return nil, nil, errdefs.InvalidState("instance %q is %s, not running", inst.Name, rt.State.Lower())
	}

	return dialAgent(rt.VsockPath)
}

// dialAgent connects to the guest agent behind vsockPath.
func dialAgent(vsockPath string) (diceragentv1.AgentServiceClient, func(), error) {
	conn, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return hypervisor.DialVsock(ctx, vsockPath, guest.AgentPort)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to guest agent: %w", err)
	}

	return diceragentv1.NewAgentServiceClient(conn), func() { _ = conn.Close() }, nil
}

// syncGuest asks a running guest to flush its filesystems before its VMM is
// ended. It is best effort.
func (m *Manager) syncGuest(ctx context.Context, inst types.InstanceSpec, rt types.InstanceStatus) {
	if rt.State != types.StateRunning || rt.VsockPath == "" {
		return
	}

	agent, closeAgent, err := dialAgent(rt.VsockPath)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot reach guest agent to flush the guest's disks",
			"instance", inst.Name, "error", err)
		return
	}
	defer closeAgent()

	ctx, cancel := context.WithTimeout(ctx, guestSyncTimeout)
	defer cancel()

	if _, err := agent.Sync(ctx, &diceragentv1.SyncRequest{}); err != nil {
		m.logger.WarnContext(ctx, "guest did not flush its disks before stopping",
			"instance", inst.Name, "error", err)
	}
}

// agentCallTimeout bounds a quick request to the guest agent.
const agentCallTimeout = 5 * time.Second

// shutdownGuest asks the guest behind vsockPath to shut down, without waiting
// for it to.
func shutdownGuest(ctx context.Context, vsockPath string) error {
	agent, closeAgent, err := dialAgent(vsockPath)
	if err != nil {
		return err
	}
	defer closeAgent()

	ctx, cancel := context.WithTimeout(ctx, agentCallTimeout)
	defer cancel()

	_, err = agent.Shutdown(ctx, &diceragentv1.ShutdownRequest{})
	return err
}
