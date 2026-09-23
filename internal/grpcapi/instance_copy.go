// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// The copy RPCs relay an archive between the client and the guest agent
// without looking inside it: packing and unpacking happen at the two ends,
// with cp's rules, and the daemon is only the way between them. See
// internal/archive.

// CopyToInstance relays an archive from the client into a running instance.
func (h *instanceHandler) CopyToInstance(
	stream grpc.ClientStreamingServer[dicerdv1.CopyToInstanceRequest, emptypb.Empty],
) error {
	req, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "receive start message: %v", err)
	}
	start := req.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first message must be a CopyToInstanceStart")
	}
	if start.GetPath() == "" {
		return status.Error(codes.InvalidArgument, "path is required")
	}

	agent, closeAgent, err := h.agent(start.GetName())
	if err != nil {
		return err
	}
	defer closeAgent()

	upload, err := agent.CopyIn(stream.Context())
	if err != nil {
		return agentStatus(start.GetName(), err)
	}
	err = upload.Send(&diceragentv1.CopyInRequest{
		Payload: &diceragentv1.CopyInRequest_Start{Start: &diceragentv1.CopyInStart{Path: start.GetPath()}},
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return agentStatus(start.GetName(), err)
	}

	// Relayed until the client finishes, or the agent stops listening --
	// an io.EOF from Send, whose reason CloseAndRecv then returns.
	for err == nil {
		var msg *dicerdv1.CopyToInstanceRequest
		msg, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		err = upload.Send(&diceragentv1.CopyInRequest{
			Payload: &diceragentv1.CopyInRequest_Data{Data: msg.GetData()},
		})
		if err != nil && !errors.Is(err, io.EOF) {
			return agentStatus(start.GetName(), err)
		}
	}

	if _, err := upload.CloseAndRecv(); err != nil {
		return agentStatus(start.GetName(), err)
	}

	return stream.SendAndClose(&emptypb.Empty{})
}

// CopyFromInstance relays an archive out of a running instance to the
// client.
func (h *instanceHandler) CopyFromInstance(
	req *dicerdv1.CopyFromInstanceRequest, stream grpc.ServerStreamingServer[dicerdv1.CopyFromInstanceResponse],
) error {
	if req.GetPath() == "" {
		return status.Error(codes.InvalidArgument, "path is required")
	}

	agent, closeAgent, err := h.agent(req.GetName())
	if err != nil {
		return err
	}
	defer closeAgent()

	download, err := agent.CopyOut(stream.Context(), &diceragentv1.CopyOutRequest{Path: req.GetPath()})
	if err != nil {
		return agentStatus(req.GetName(), err)
	}

	for {
		msg, err := download.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return agentStatus(req.GetName(), err)
		}

		if err := stream.Send(&dicerdv1.CopyFromInstanceResponse{Data: msg.GetData()}); err != nil {
			return err
		}
	}
}
