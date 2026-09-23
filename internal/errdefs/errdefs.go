// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package errdefs defines the classes of error the daemon's code returns,
// matched with errors.Is. It knows nothing of how an error reaches a client:
// internal/grpcapi sends each class as a gRPC status code.
//
// The constructor of each class keeps the class out of the message:
// NotFound("no instance %q", name) reads `no instance "web"` and still
// matches ErrNotFound.
package errdefs

import (
	"errors"
	"fmt"
)

var (
	// ErrNotFound is a resource that does not exist.
	ErrNotFound = errors.New("not found")

	// ErrExists is a resource created with a name another already has.
	ErrExists = errors.New("already exists")

	// ErrInvalidState is a resource in the wrong state for what was asked,
	// such as deleting a running instance.
	ErrInvalidState = errors.New("invalid state")

	// ErrInvalidArgument is a request that is malformed, whatever state the
	// host is in.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrResourceExhausted is a host with too little of something left:
	// CPU or memory to start an instance, addresses on a network. It may
	// succeed once something else lets go.
	ErrResourceExhausted = errors.New("resource exhausted")

	// ErrUnavailable is something the daemon needs that cannot be reached,
	// such as a registry. Trying again later may succeed.
	ErrUnavailable = errors.New("unavailable")
)

// NotFound returns an error in the ErrNotFound class, formatted as
// fmt.Errorf would.
func NotFound(format string, args ...any) error {
	return classify(ErrNotFound, format, args...)
}

// Exists returns an error in the ErrExists class.
func Exists(format string, args ...any) error {
	return classify(ErrExists, format, args...)
}

// InvalidState returns an error in the ErrInvalidState class.
func InvalidState(format string, args ...any) error {
	return classify(ErrInvalidState, format, args...)
}

// InvalidArgument returns an error in the ErrInvalidArgument class.
func InvalidArgument(format string, args ...any) error {
	return classify(ErrInvalidArgument, format, args...)
}

// ResourceExhausted returns an error in the ErrResourceExhausted class.
func ResourceExhausted(format string, args ...any) error {
	return classify(ErrResourceExhausted, format, args...)
}

// Unavailable returns an error in the ErrUnavailable class.
func Unavailable(format string, args ...any) error {
	return classify(ErrUnavailable, format, args...)
}

// classified is an error in a class, whose message is its own alone.
type classified struct {
	class error
	err   error
}

func classify(class error, format string, args ...any) error {
	return &classified{class: class, err: fmt.Errorf(format, args...)}
}

func (e *classified) Error() string { return e.err.Error() }

// Unwrap exposes the class, and whatever the message wrapped with %w.
func (e *classified) Unwrap() []error { return []error{e.err, e.class} }
