// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/registry"
)

// TestPullErrorDescribes checks a failed pull reads in terms of the image and
// its registry, in the class that says whether to try again.
func TestPullErrorDescribes(t *testing.T) {
	// As the resolver wraps it: a layer of context for every step.
	registryErr := func(code int) error {
		return fmt.Errorf("resolve manifest: fetch manifest: %w", &transport.Error{StatusCode: code})
	}
	netErr := fmt.Errorf("resolve manifest: %w",
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")})
	// As the HTTP transport returns a keychain's failure: inside a url.Error,
	// which is a net.Error too.
	credErr := fmt.Errorf("resolve manifest: %w", &url.Error{Op: "Get", URL: "https://ghcr.io/v2/",
		Err: &registry.CredentialsError{Registry: "ghcr.io", Err: errors.New("open /etc/dicerd/token: no such file")}})

	for _, tc := range []struct {
		ref     string
		cause   error
		class   error
		wantMsg string
	}{
		{"nginx:9", registryErr(http.StatusNotFound), errdefs.ErrNotFound, `image "nginx:9" not found on docker.io`},
		{
			"nosuchimage:1", registryErr(http.StatusUnauthorized), errdefs.ErrNotFound,
			`image "nosuchimage:1" not found on docker.io (or it is private)`,
		},
		{
			"ghcr.io/acme/app:2", registryErr(http.StatusForbidden), errdefs.ErrNotFound,
			`image "ghcr.io/acme/app:2" not found on ghcr.io (or it is private)`,
		},
		{
			"localhost:5000/app", registryErr(http.StatusTooManyRequests), errdefs.ErrUnavailable,
			"localhost:5000 is limiting how often this host may pull; try again later",
		},
		{"nginx", netErr, errdefs.ErrUnavailable, "cannot reach docker.io: connect: connection refused"},
		{
			"ghcr.io/acme/app:2", credErr, errdefs.ErrUnavailable,
			"cannot log in to ghcr.io: open /etc/dicerd/token: no such file",
		},
		{"nginx", errors.New("disk full"), errdefs.ErrUnavailable, `cannot pull image "nginx": disk full`},
	} {
		err := error(&PullError{Ref: tc.ref, Cause: tc.cause})
		if err.Error() != tc.wantMsg || !errors.Is(err, tc.class) || !errors.Is(err, tc.cause) {
			t.Errorf("%s: %v = %q, want %q in class %v, wrapping its cause", tc.ref, tc.cause, err, tc.wantMsg, tc.class)
		}
	}
}
