// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/errdefs"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// ExecInstance runs a command inside a running instance through its guest
// agent.
func (h *instanceHandler) ExecInstance(stream execClientStream) error {
	ctx := stream.Context()

	req, err := stream.Recv()
	if err != nil {
		return errdefs.InvalidArgument("receive start message: %v", err)
	}
	start := req.GetStart()
	if start == nil {
		return errdefs.InvalidArgument("first message must be an ExecInstanceStart")
	}

	agent, closeAgent, err := h.agent(start.GetName())
	if err != nil {
		return err
	}
	defer closeAgent()

	agentStream, err := agent.Exec(ctx)
	if err != nil {
		return errdefs.Unavailable("open guest exec stream: %v", err)
	}

	err = agentStream.Send(&diceragentv1.ExecRequest{
		Payload: &diceragentv1.ExecRequest_Start{
			Start: &diceragentv1.ExecStart{
				Command:        start.GetCommand(),
				Tty:            start.GetTty(),
				Cwd:            start.GetCwd(),
				TimeoutSeconds: start.GetTimeoutSeconds(),
				Rows:           start.GetRows(),
				Cols:           start.GetCols(),
				Env:            start.GetEnv(),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("send start to guest: %w", err)
	}

	return proxyExec(stream, agentStream)
}

type (
	// execClientStream is the daemon's end of a client's exec session.
	execClientStream = grpc.BidiStreamingServer[dicerdv1.ExecInstanceRequest, dicerdv1.ExecInstanceResponse]

	// execAgentStream is the daemon's end of the guest agent's exec session.
	execAgentStream = grpc.BidiStreamingClient[diceragentv1.ExecRequest, diceragentv1.ExecResponse]
)

// proxyExec relays an exec session until either side ends it.
func proxyExec(client execClientStream, agent execAgentStream) error {
	go forwardExecInput(client, agent)

	done := make(chan error, 1)
	go func() { done <- forwardExecOutput(agent, client) }()

	select {
	case err := <-done:
		return err
	case <-client.Context().Done():
		return client.Context().Err()
	}
}

// forwardExecInput sends the client's stdin and resizes to the agent until
// the client stops sending or the agent stops accepting.
func forwardExecInput(client execClientStream, agent execAgentStream) {
	for {
		msg, err := client.Recv()
		if err != nil {
			_ = agent.CloseSend()
			return
		}
		out := toAgentExecRequest(msg)
		if out == nil {
			continue
		}
		if err := agent.Send(out); err != nil {
			return
		}
	}
}

// forwardExecOutput sends the agent's output and exit code to the client
// until the agent closes the stream.
func forwardExecOutput(agent execAgentStream, client execClientStream) error {
	for {
		resp, err := agent.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		out := toClientExecResponse(resp)
		if out == nil {
			continue
		}
		if err := client.Send(out); err != nil {
			return err
		}
	}
}

// toAgentExecRequest converts a client exec message for the agent, or
// returns nil.
func toAgentExecRequest(msg *dicerdv1.ExecInstanceRequest) *diceragentv1.ExecRequest {
	switch p := msg.GetPayload().(type) {
	case *dicerdv1.ExecInstanceRequest_Stdin:
		return &diceragentv1.ExecRequest{
			Payload: &diceragentv1.ExecRequest_Stdin{Stdin: p.Stdin},
		}
	case *dicerdv1.ExecInstanceRequest_Resize:
		return &diceragentv1.ExecRequest{
			Payload: &diceragentv1.ExecRequest_Resize{
				Resize: &diceragentv1.ExecResize{Rows: p.Resize.GetRows(), Cols: p.Resize.GetCols()},
			},
		}
	default:
		return nil
	}
}

// toClientExecResponse converts an agent exec message for the client, or
// returns nil.
func toClientExecResponse(resp *diceragentv1.ExecResponse) *dicerdv1.ExecInstanceResponse {
	switch p := resp.GetPayload().(type) {
	case *diceragentv1.ExecResponse_Stdout:
		return &dicerdv1.ExecInstanceResponse{Payload: &dicerdv1.ExecInstanceResponse_Stdout{Stdout: p.Stdout}}
	case *diceragentv1.ExecResponse_Stderr:
		return &dicerdv1.ExecInstanceResponse{Payload: &dicerdv1.ExecInstanceResponse_Stderr{Stderr: p.Stderr}}
	case *diceragentv1.ExecResponse_ExitCode:
		return &dicerdv1.ExecInstanceResponse{Payload: &dicerdv1.ExecInstanceResponse_ExitCode{ExitCode: p.ExitCode}}
	default:
		return nil
	}
}
