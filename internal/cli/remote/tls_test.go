// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package remote

import (
	"errors"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

func TestTLSValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		tls  *TLS
		want error
	}{
		{"none at all", nil, nil},
		{"a CA alone verifies the daemon", &TLS{CAFile: "ca.pem"}, nil},
		{"a pair alone", &TLS{CertFile: "c.pem", KeyFile: "k.pem"}, nil},
		{"both halves", &TLS{CertFile: "c.pem", KeyFile: "k.pem", CAFile: "ca.pem"}, nil},
		{"a certificate without its key", &TLS{CertFile: "c.pem"}, errdefs.ErrInvalidArgument},
		{"a key without its certificate", &TLS{KeyFile: "k.pem"}, errdefs.ErrInvalidArgument},
		{"a server name and nothing else", &TLS{ServerName: "dicer1"}, errdefs.ErrInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tls.Validate()
			switch {
			case tc.want == nil && err != nil:
				t.Errorf("Validate = %v, want no error", err)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Errorf("Validate = %v, want %v", err, tc.want)
			}
		})
	}
}

// The name the daemon's certificate is checked against is the address's
// host, which gRPC fills in, unless the remote names another.
func TestTLSConfigServerName(t *testing.T) {
	if _, err := (&TLS{}).Config(); err == nil {
		t.Fatal("an empty TLS built a configuration")
	}
	if _, err := (&TLS{CAFile: "does-not-exist.pem"}).Config(); err == nil {
		t.Fatal("a missing CA file was accepted")
	}
}
