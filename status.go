// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// classes pairs each error class with the gRPC status code it travels as.
//
// It is read in both directions, and is the only place the correspondence is
// written down: the daemon reports an error under the code its class maps to,
// and a client recovers the class from the code, so that a caller on the far
// side of the wire matches ErrNotFound just as one in the daemon does.
var classes = []struct {
	class error
	code  codes.Code
}{
	{ErrNotFound, codes.NotFound},
	{ErrExists, codes.AlreadyExists},
	{ErrInvalidState, codes.FailedPrecondition},
	{ErrInvalidArgument, codes.InvalidArgument},
	{ErrResourceExhausted, codes.ResourceExhausted},
	{errors.ErrUnsupported, codes.Unimplemented},
}

// StatusCode returns the gRPC status code an error is reported to clients
// under, which is codes.Internal for an error in no class: an error the
// daemon did not expect is the daemon's fault until classified otherwise.
//
// It is for the server side. A caller holding an error from a Client has its
// class already, and matches it with errors.Is.
func StatusCode(err error) codes.Code {
	for _, c := range classes {
		if errors.Is(err, c.class) {
			return c.code
		}
	}

	return codes.Internal
}

// classOf returns the error class a status code means, or nil for a code
// that is no class of its own: a call that was cancelled, timed out or never
// reached the daemon failed for reasons that have nothing to do with what was
// asked.
func classOf(code codes.Code) error {
	for _, c := range classes {
		if c.code == code {
			return c.class
		}
	}

	return nil
}

// classifyStatus puts the error from a call into the class its status code
// names, keeping the error itself intact underneath: its message is
// unchanged, and it is still a status error, so a caller that wants the code
// can still ask for it.
func classifyStatus(err error) error {
	if err == nil {
		return nil
	}

	class := classOf(status.Code(err))
	if class == nil {
		return err
	}

	return &apiError{class: class, err: err}
}

// apiError is a failed call in the class its status code named.
type apiError struct {
	class error
	err   error
}

func (e *apiError) Error() string { return e.err.Error() }

// Unwrap exposes the class, so errors.Is finds it, and the error itself.
func (e *apiError) Unwrap() []error { return []error{e.err, e.class} }

// GRPCStatus hands out the status underneath unchanged, so that a caller
// reading the code or the message gets what the daemon sent. Without it,
// status.FromError finds the status by unwrapping and replaces its message
// with this error's, which is that same status printed in full.
func (e *apiError) GRPCStatus() *status.Status { return status.Convert(e.err) }

// classifyUnary classifies the error from a unary call.
func classifyUnary(
	ctx context.Context, method string, req, reply any,
	cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
) error {
	return classifyStatus(invoker(ctx, method, req, reply, cc, opts...))
}

// classifyStream classifies the errors from a streaming call, which arrive
// from the stream rather than from opening it.
func classifyStream(
	ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn,
	method string, streamer grpc.Streamer, opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	s, err := streamer(ctx, desc, cc, method, opts...)
	if err != nil {
		return nil, classifyStatus(err)
	}

	return classifiedStream{s}, nil
}

// classifiedStream is a client stream whose errors are in their classes.
// io.EOF is left alone: it ends a stream rather than failing it, and callers
// compare against it directly.
type classifiedStream struct {
	grpc.ClientStream
}

func (s classifiedStream) SendMsg(m any) error {
	return classifyStatus(s.ClientStream.SendMsg(m))
}

func (s classifiedStream) RecvMsg(m any) error {
	return classifyStatus(s.ClientStream.RecvMsg(m))
}

// isStreamClosed reports whether an error from a send means the daemon has
// already ended the stream, which is io.EOF by gRPC's convention. What went
// wrong arrives from the next Recv, so a caller reports that rather than
// this.
func isStreamClosed(err error) bool {
	return errors.Is(err, io.EOF)
}
