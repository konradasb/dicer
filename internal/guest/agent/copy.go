// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer/internal/archive"
	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// CopyIn implements diceragentv1.AgentServiceServer: it unpacks the archive
// that follows the start message at the path it names.
func (s *server) CopyIn(stream grpc.ClientStreamingServer[diceragentv1.CopyInRequest, diceragentv1.CopyInResponse]) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	start := req.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first message must be a CopyInStart")
	}

	dest := guestPath(start.GetPath())
	slog.Info("copy in", "path", dest)

	err = archive.Receive(func() ([]byte, error) {
		msg, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return msg.GetData(), nil
	}, dest)
	if err != nil {
		return copyStatus(err)
	}

	return stream.SendAndClose(&diceragentv1.CopyInResponse{})
}

// CopyOut implements diceragentv1.AgentServiceServer: it sends the path it
// is asked for as an archive.
func (s *server) CopyOut(
	req *diceragentv1.CopyOutRequest, stream grpc.ServerStreamingServer[diceragentv1.CopyOutResponse],
) error {
	src := guestPath(req.GetPath())
	slog.Info("copy out", "path", src)

	_, err := archive.Send(src, func(chunk []byte) error {
		return stream.Send(&diceragentv1.CopyOutResponse{Data: chunk})
	})
	if err != nil {
		return copyStatus(err)
	}

	return nil
}

// guestPath makes a path absolute, taking a relative one from the root: the
// agent's own working directory means nothing to the caller.
func guestPath(p string) string {
	if !filepath.IsAbs(p) {
		p = "/" + p
	}
	return filepath.Clean(p)
}

// copyStatus says why a copy failed in terms the caller can act on. An error
// from the stream itself is already a status and passes through.
func copyStatus(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}

	// "/srv/x: no such file or directory", not "lstat /srv/x: ...": which
	// system call found out is nothing the caller can use.
	msg := err.Error()
	if pathErr := (*fs.PathError)(nil); errors.As(err, &pathErr) {
		msg = pathErr.Path + ": " + pathErr.Err.Error()
	}

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return status.Error(codes.NotFound, msg)
	case errors.Is(err, fs.ErrPermission):
		return status.Error(codes.PermissionDenied, msg)
	default:
		return status.Error(codes.FailedPrecondition, msg)
	}
}
