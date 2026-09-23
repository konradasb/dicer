// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/errdefs"
)

// The daemon's code returns errors in the classes errdefs defines. A client
// receives a gRPC status instead, whose code says the class: the
// interceptors below are where one becomes the other, and nothing else in
// the daemon builds a status.

// classes pairs each error class with the status code it is sent as.
var classes = []struct {
	class error
	code  codes.Code
}{
	{errdefs.ErrNotFound, codes.NotFound},
	{errdefs.ErrExists, codes.AlreadyExists},
	{errdefs.ErrInvalidState, codes.FailedPrecondition},
	{errdefs.ErrInvalidArgument, codes.InvalidArgument},
	{errdefs.ErrResourceExhausted, codes.ResourceExhausted},
	{errdefs.ErrUnavailable, codes.Unavailable},
	{errors.ErrUnsupported, codes.Unimplemented},
	{context.Canceled, codes.Canceled},
	{context.DeadlineExceeded, codes.DeadlineExceeded},
}

// toStatus converts err to the status it is sent as, with err's message: the
// code of its class, else of the status it already carries, as one from a
// guest's agent does, else Internal. To report an error in another class
// than its own, as a request naming a missing kernel is InvalidArgument,
// wrap it with %v rather than %w.
func toStatus(err error) error {
	if err == nil {
		return nil
	}

	for _, c := range classes {
		if errors.Is(err, c.class) {
			return status.Error(c.code, err.Error())
		}
	}
	if s, ok := status.FromError(err); ok {
		return s.Err()
	}

	return status.Error(codes.Internal, err.Error())
}

// UnaryStatusInterceptor sends the error a unary handler returns as a status.
func UnaryStatusInterceptor(
	ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	resp, err := handler(ctx, req)
	return resp, toStatus(err)
}

// StreamStatusInterceptor sends the error a streaming handler returns as a
// status.
func StreamStatusInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return toStatus(handler(srv, ss))
}
