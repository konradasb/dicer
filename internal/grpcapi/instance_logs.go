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
	instance, err := h.definitions.Instance(req.GetName())
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

	options := vm.LogOptions{
		Source:    source,
		TailLines: int(req.GetTailLines()),
		Follow:    req.GetFollow(),
	}

	return h.instances.StreamLogs(stream.Context(), instance, options, logChunkWriter{stream: stream})
}

// logChunkWriter is the io.Writer StreamLogs writes a log into, sending each
// write as one InstanceLogChunk.
type logChunkWriter struct {
	stream grpc.ServerStreamingServer[dicerdv1.InstanceLogChunk]
}

// Write sends p as one chunk.
func (w logChunkWriter) Write(p []byte) (int, error) {
	// The caller reuses p after Write returns.
	if err := w.stream.Send(&dicerdv1.InstanceLogChunk{Data: bytes.Clone(p)}); err != nil {
		return 0, err
	}

	return len(p), nil
}
