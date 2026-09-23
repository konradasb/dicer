// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"fmt"
)

// ErrInvalidReference reports an image reference that cannot be parsed.
var ErrInvalidReference = errors.New("invalid image reference")

// PullError reports a failure to fetch an image from its registry.
type PullError struct {
	Ref   string
	Cause error
}

// Error implements error.
func (e *PullError) Error() string {
	return fmt.Sprintf("pull image %s: %v", e.Ref, e.Cause)
}

// Unwrap returns the underlying cause.
func (e *PullError) Unwrap() error {
	return e.Cause
}

// ConvertError reports a failure to convert a pulled image to a disk format.
type ConvertError struct {
	Digest string
	Format string
	Cause  error
}

// Error implements error.
func (e *ConvertError) Error() string {
	return fmt.Sprintf("convert image %s to %s: %v", e.Digest, e.Format, e.Cause)
}

// Unwrap returns the underlying cause.
func (e *ConvertError) Unwrap() error {
	return e.Cause
}
