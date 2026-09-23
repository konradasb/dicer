// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer/internal/image"
)

func TestPullStatus(t *testing.T) {
	// As the resolver wraps it: a layer of context for every step.
	registryErr := func(code int) error {
		return fmt.Errorf("resolve manifest: fetch manifest: %w", &transport.Error{StatusCode: code})
	}
	netErr := fmt.Errorf("resolve manifest: %w",
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")})

	for _, tc := range []struct {
		ref      string
		cause    error
		wantCode codes.Code
		wantMsg  string
	}{
		{"nginx:9", registryErr(http.StatusNotFound), codes.NotFound, `image "nginx:9" not found on docker.io`},
		{
			"nosuchimage:1", registryErr(http.StatusUnauthorized), codes.NotFound,
			`image "nosuchimage:1" not found on docker.io (or it is private)`,
		},
		{
			"ghcr.io/acme/app:2", registryErr(http.StatusForbidden), codes.NotFound,
			`image "ghcr.io/acme/app:2" not found on ghcr.io (or it is private)`,
		},
		{
			"localhost:5000/app", registryErr(http.StatusTooManyRequests), codes.Unavailable,
			"localhost:5000 is limiting how often this host may pull; try again later",
		},
		{"nginx", netErr, codes.Unavailable, "cannot reach docker.io: connect: connection refused"},
		{"nginx", errors.New("disk full"), codes.Unavailable, `cannot pull image "nginx": disk full`},
	} {
		err := toStatus(&image.PullError{Ref: tc.ref, Cause: tc.cause})
		s := status.Convert(err)
		if s.Code() != tc.wantCode || s.Message() != tc.wantMsg {
			t.Errorf("%s: %v = %s %q, want %s %q", tc.ref, tc.cause, s.Code(), s.Message(), tc.wantCode, tc.wantMsg)
		}
	}
}
