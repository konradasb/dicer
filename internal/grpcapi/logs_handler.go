// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"bytes"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// GetInstanceLogs streams an instance's log to the client.
func (h *instanceHandler) GetInstanceLogs(
	req *dicerdv1.GetInstanceLogsRequest,
	stream grpc.ServerStreamingServer[dicerdv1.InstanceLogChunk],
) error {
	inst, err := h.definitions.GetInstance(req.GetName())
	if err != nil {
		return err
	}

	source, err := logSources.fromProto(req.GetSource())
	if err != nil {
		return err
	}
	if req.GetTailLines() < 0 {
		return errdefs.InvalidArgument("tail_lines cannot be negative")
	}

	opts := vm.LogOptions{
		Source:    source,
		TailLines: int(req.GetTailLines()),
		Follow:    req.GetFollow(),
	}

	if err := h.instances.StreamLogs(stream.Context(), inst, opts, &logStream{stream: stream}); err != nil {
		return err
	}

	return nil
}

// logStream is the io.Writer StreamLogs writes a log into, one message per
// write.
type logStream struct {
	stream grpc.ServerStreamingServer[dicerdv1.InstanceLogChunk]
}

func (l *logStream) Write(p []byte) (int, error) {
	// The caller reuses p after Write returns.
	if err := l.stream.Send(&dicerdv1.InstanceLogChunk{Data: bytes.Clone(p)}); err != nil {
		return 0, err
	}

	return len(p), nil
}
