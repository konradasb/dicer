// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/image/reference"
)

// ErrInvalidReference reports an image reference that cannot be parsed.
var ErrInvalidReference = errors.New("invalid image reference")

// PullError reports a failure to fetch an image from its registry. It
// describes it in terms of the image and its registry rather than the HTTP
// exchange, and is in the class that says whether to try again:
// errdefs.ErrNotFound for an image the registry does not have, and
// errdefs.ErrUnavailable for everything else.
type PullError struct {
	Ref   string
	Cause error
}

// Error implements error.
func (e *PullError) Error() string {
	msg, _ := e.describe()
	return msg
}

// Unwrap returns the underlying cause and the class.
func (e *PullError) Unwrap() []error {
	_, class := e.describe()
	return []error{e.Cause, class}
}

// describe returns the message and class of the failure.
func (e *PullError) describe() (string, error) {
	registry := registryOf(e.Ref)

	var httpErr *transport.Error
	if errors.As(e.Cause, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Sprintf("image %q not found on %s", e.Ref, registry), errdefs.ErrNotFound
		case http.StatusUnauthorized, http.StatusForbidden:
			// Registries answer so for missing repositories too.
			return fmt.Sprintf("image %q not found on %s (or it is private)", e.Ref, registry), errdefs.ErrNotFound
		case http.StatusTooManyRequests:
			return registry + " is limiting how often this host may pull; try again later",
				errdefs.ErrUnavailable
		}
	}

	var netErr net.Error
	if errors.As(e.Cause, &netErr) {
		return fmt.Sprintf("cannot reach %s: %v", registry, innermost(e.Cause)), errdefs.ErrUnavailable
	}

	return fmt.Sprintf("cannot pull image %q: %v", e.Ref, innermost(e.Cause)), errdefs.ErrUnavailable
}

// registryOf returns the registry an image reference names, "docker.io" if
// it names none.
func registryOf(ref string) string {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return "its registry"
	}
	host, _, ok := strings.Cut(parsed.Repository(), "/")
	if !ok || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "docker.io"
	}
	return host
}

// innermost returns the error at the bottom of err's chain.
func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
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
