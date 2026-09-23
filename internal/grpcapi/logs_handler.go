// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"bytes"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer/internal/vm"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// GetInstanceLogs streams an instance's log to the client.
func (h *instanceHandler) GetInstanceLogs(
	req *dicerdv1.GetInstanceLogsRequest,
	stream grpc.ServerStreamingServer[dicerdv1.InstanceLogChunk],
) error {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return toStatus(err)
	}

	source, err := logSource(req.GetSource())
	if err != nil {
		return err
	}
	if req.GetTailLines() < 0 {
		return status.Error(codes.InvalidArgument, "tail_lines cannot be negative")
	}

	opts := vm.LogOptions{
		Source:    source,
		TailLines: int(req.GetTailLines()),
		Follow:    req.GetFollow(),
	}

	if err := h.instances.StreamLogs(stream.Context(), inst, opts, &logStream{stream: stream}); err != nil {
		return toStatus(err)
	}

	return nil
}

// logSource maps the requested source onto the lifecycle's own.
func logSource(source dicerdv1.LogSource) (vm.LogSource, error) {
	switch source {
	case dicerdv1.LogSource_LOG_SOURCE_UNSPECIFIED, dicerdv1.LogSource_LOG_SOURCE_GUEST:
		return vm.LogSourceGuest, nil
	case dicerdv1.LogSource_LOG_SOURCE_HYPERVISOR:
		return vm.LogSourceHypervisor, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "unknown log source %s", source)
	}
}

// logStream is the io.Writer StreamLogs writes a log into, one message per
// write.
type logStream struct {
	stream grpc.ServerStreamingServer[dicerdv1.InstanceLogChunk]
}

func (l *logStream) Write(p []byte) (int, error) {
	// The stream may hold the message after Send returns, and the caller
	// reuses its buffer, so it has to be copied.
	if err := l.stream.Send(&dicerdv1.InstanceLogChunk{Data: bytes.Clone(p)}); err != nil {
		return 0, err
	}

	return len(p), nil
}
