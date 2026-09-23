// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"errors"
	"strings"
	"testing"
)

const testFingerprint = "sha256:" + "ab00000000000000000000000000000000000000000000000000000000000000"

func TestParseAddress(t *testing.T) {
	r, err := ParseAddress("unix:///tmp/dicer.sock")
	if err != nil {
		t.Fatalf("ParseAddress(unix): %v", err)
	}
	if r.SocketPath() != "/tmp/dicer.sock" {
		t.Errorf("socket = %q", r.SocketPath())
	}

	// A TCP address alone carries no fingerprint, and connecting to one
	// without would trust whoever answers.
	_, err = ParseAddress("tcp://192.0.2.1:7443")
	if err == nil || !strings.Contains(err.Error(), "enrolment token") {
		t.Errorf("ParseAddress(tcp) = %v, want a pointer to enrolment", err)
	}
}

func TestRemoteValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote Remote
		want   error
	}{
		{"socket", SocketRemote("/run/dicer/dicer.sock"), nil},
		{"pinned tcp", TCPRemote("192.0.2.1:7443", testFingerprint), nil},
		{"relative socket", SocketRemote("dicer.sock"), ErrInvalidArgument},
		{"no scheme", Remote{Address: "192.0.2.1:7443"}, ErrInvalidArgument},
		{"no port", TCPRemote("192.0.2.1", testFingerprint), ErrInvalidArgument},
		// Unpinned, a TCP remote would trust whatever answers, so it is
		// not a remote that can be connected to at all.
		{"unpinned tcp", Remote{Address: "tcp://192.0.2.1:7443"}, ErrInvalidArgument},
		{"short fingerprint", TCPRemote("192.0.2.1:7443", "sha256:ab"), ErrInvalidArgument},
	} {
		err := tc.remote.Validate()
		switch {
		case tc.want == nil && err != nil:
			t.Errorf("%s: Validate = %v, want no error", tc.name, err)
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s: Validate = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestIsAddress(t *testing.T) {
	for _, s := range []string{"unix:///run/dicer/dicer.sock", "tcp://192.0.2.1:7443"} {
		if !IsAddress(s) {
			t.Errorf("IsAddress(%q) = false", s)
		}
	}
	for _, s := range []string{"local", "prod", ""} {
		if IsAddress(s) {
			t.Errorf("IsAddress(%q) = true", s)
		}
	}
}
