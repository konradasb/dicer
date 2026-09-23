// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/errdefs"
)

// TestToStatus checks each error is sent with the code of its class and its
// own message.
func TestToStatus(t *testing.T) {
	for _, tc := range []struct {
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{errdefs.NotFound("no instance %q", "web"), codes.NotFound, `no instance "web"`},
		{fmt.Errorf("start web: %w", errdefs.InvalidState("it is running")), codes.FailedPrecondition,
			"start web: it is running"},
		{errdefs.Unavailable("cannot reach ghcr.io"), codes.Unavailable, "cannot reach ghcr.io"},
		{fmt.Errorf("probe: %w", errors.ErrUnsupported), codes.Unimplemented, "probe: unsupported operation"},
		{context.Canceled, codes.Canceled, "context canceled"},
		{errors.New("disk on fire"), codes.Internal, "disk on fire"},
		// A status from another service, a guest's agent, keeps its code.
		{status.Error(codes.PermissionDenied, "/etc/shadow: permission denied"), codes.PermissionDenied,
			"/etc/shadow: permission denied"},
	} {
		s := status.Convert(toStatus(tc.err))
		if s.Code() != tc.wantCode || s.Message() != tc.wantMsg {
			t.Errorf("%v sent as %s %q, want %s %q", tc.err, s.Code(), s.Message(), tc.wantCode, tc.wantMsg)
		}
	}

	if toStatus(nil) != nil {
		t.Error("toStatus(nil) is not nil")
	}
}

// TestErrorClassIsTheWrappersOwn checks a handler reporting an error in
// another class than its own, by wrapping it with %v, sends that class.
func TestErrorClassIsTheWrappersOwn(t *testing.T) {
	err := errdefs.InvalidArgument("%v", errdefs.NotFound("no kernel %q", "k"))
	if got := status.Code(toStatus(err)); got != codes.InvalidArgument {
		t.Errorf("code = %s, want InvalidArgument", got)
	}
}
