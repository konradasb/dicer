// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

// TestClassStaysOutOfTheMessage is the reason the constructors exist: the
// class is for code to match, and says nothing a person needs to read.
func TestClassStaysOutOfTheMessage(t *testing.T) {
	for _, tc := range []struct {
		err   error
		class error
		want  string
	}{
		{NotFound("no instance %q", "web"), ErrNotFound, `no instance "web"`},
		{Exists("instance %q already exists", "web"), ErrExists, `instance "web" already exists`},
		{InvalidState("instance %q is running", "web"), ErrInvalidState, `instance "web" is running`},
		{InvalidArgument("invalid name %q", "a_b"), ErrInvalidArgument, `invalid name "a_b"`},
		{ResourceExhausted("no room"), ErrResourceExhausted, "no room"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("message = %q, want %q", got, tc.want)
		}
		if !errors.Is(tc.err, tc.class) {
			t.Errorf("%q is not in class %v", tc.err, tc.class)
		}
		if !errors.Is(fmt.Errorf("context: %w", tc.err), tc.class) {
			t.Errorf("%q wrapped lost class %v", tc.err, tc.class)
		}
	}
}

func TestClassKeepsCause(t *testing.T) {
	err := InvalidState("cannot publish port 80: in use (%w)", io.ErrUnexpectedEOF)
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, ErrInvalidState) {
		t.Errorf("%v should be both its cause and its class", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("%v matched a class it is not in", err)
	}
}
