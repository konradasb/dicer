// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"errors"
	"fmt"
)

// Errors that cross a package boundary are put in one of the classes here.
// These are the conditions the API reports and callers act on, so they are
// defined once and compared with errors.Is, on both sides of the wire: the
// daemon classifies an error, and the client recovers the class from the
// status code it arrived as. An error meaningful to only one package belongs
// in that package, not here.
//
// An error is put in a class with the constructor of that name, which keeps
// the class out of its message: NotFound("no instance %q", name) reads
// `no instance "web"`, not `no instance "web": not found`, and still matches
// ErrNotFound.
var (
	// ErrNotFound is returned when a requested resource does not exist.
	ErrNotFound = errors.New("not found")

	// ErrExists is returned when creating a resource whose name is taken.
	ErrExists = errors.New("already exists")

	// ErrInvalidState is returned when a resource is in the wrong state for
	// the requested operation.
	ErrInvalidState = errors.New("invalid state")

	// ErrInvalidArgument is returned when a request is malformed, whatever
	// state the system is in.
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrResourceExhausted is returned when the host has too little of
	// something left to do what was asked: CPU or memory to start an
	// instance, addresses on a network. It may succeed once something else
	// lets go.
	ErrResourceExhausted = errors.New("resource exhausted")
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
