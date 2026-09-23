// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package remote

import (
	"errors"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

func TestRemoteValidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote Remote
		want   error
	}{
		{"socket", Remote{Address: "unix:///run/dicer/dicer.sock"}, nil},
		{"host and port", Remote{Address: "192.0.2.1:7443"}, nil},
		{"dns target", Remote{Address: "dns:///dicer1.example.com:7443"}, nil},
		{"with tls", Remote{Address: "192.0.2.1:7443", TLS: &TLS{CAFile: "ca.pem"}}, nil},
		{"relative socket", Remote{Address: "unix://dicer.sock"}, errdefs.ErrInvalidArgument},
		{"no port", Remote{Address: "192.0.2.1"}, errdefs.ErrInvalidArgument},
		{"half a keypair", Remote{Address: "192.0.2.1:7443", TLS: &TLS{CertFile: "c.pem"}}, errdefs.ErrInvalidArgument},
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

// A socket is controlled by its file permissions; TLS on it would be
// configuration that does nothing, which is worse than being refused.
func TestSocketRemoteRefusesTLS(t *testing.T) {
	err := Remote{Address: "unix:///run/dicer/dicer.sock", TLS: &TLS{CAFile: "ca.pem"}}.Validate()
	if !errors.Is(err, errdefs.ErrInvalidArgument) || !strings.Contains(err.Error(), "file permissions") {
		t.Errorf("a unix remote with TLS = %v, want it refused, saying why", err)
	}
}

func TestIsAddress(t *testing.T) {
	for _, s := range []string{"unix:///run/dicer/dicer.sock", "192.0.2.1:7443", "dns:///host:7443"} {
		if !IsAddress(s) {
			t.Errorf("IsAddress(%q) = false", s)
		}
	}
	for _, s := range []string{"local", "prod", "dicer1.example.com", ""} {
		if IsAddress(s) {
			t.Errorf("IsAddress(%q) = true", s)
		}
	}
}

func TestClientOptions(t *testing.T) {
	opts, err := Remote{Address: "unix:///run/dicer/dicer.sock"}.ClientOptions()
	if err != nil || len(opts) != 1 {
		t.Errorf("a socket's options = %d, %v; want its address alone", len(opts), err)
	}

	// The files are read when a connection is made, so a missing one is
	// reported then.
	if _, err := (Remote{Address: "192.0.2.1:7443", TLS: &TLS{CAFile: "does-not-exist.pem"}}).ClientOptions(); err == nil {
		t.Error("a missing CA file was accepted")
	}
}
