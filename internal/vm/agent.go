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

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// guestSyncTimeout bounds how long a stop waits for the guest to flush its
// filesystems before ending its VMM regardless.
const guestSyncTimeout = 10 * time.Second

// Agent connects to the guest agent of a running instance, over the
// instance's vsock: it keeps working for an instance whose network is broken
// or isolated. The returned function closes the connection.
func (m *Manager) Agent(inst dicer.InstanceSpec) (diceragentv1.AgentServiceClient, func(), error) {
	rt, err := m.Runtime(inst)
	if err != nil {
		return nil, nil, err
	}
	if rt.State != dicer.StateRunning {
		return nil, nil, dicer.InvalidState("instance %q is %s, not running", inst.Name, rt.State.Lower())
	}

	return dialAgent(rt.VsockPath)
}

// dialAgent connects to the guest agent behind the vsock a VMM serves at
// vsockPath.
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

// syncGuest has a running guest flush its filesystems, so that ending its
// VMM loses nothing it wrote. Whatever the guest's PID 1 makes of a shutdown
// request, the agent can do this. It is best effort: a guest that cannot be
// reached, or does not answer in time, is stopped all the same.
func (m *Manager) syncGuest(ctx context.Context, inst dicer.InstanceSpec, rt dicer.InstanceStatus) {
	if rt.State != dicer.StateRunning || rt.VsockPath == "" {
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

// agentCallTimeout bounds a quick request to the guest agent, one that asks
// for something to be done rather than waits for it.
const agentCallTimeout = 5 * time.Second

// shutdownGuest asks the guest behind vsockPath to shut down in an orderly
// way, and returns once it has asked.
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
