// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package remote is the daemons the CLI knows, kept with the current one in
// ~/.config/dicer/remotes.yaml, and how each is reached.
package remote

import (
	"net"
	"path/filepath"
	"strings"

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/errdefs"
)

const (
	// Local names the built-in remote: the daemon on this machine, on its
	// default socket. It is always there and cannot be deleted.
	Local = "local"

	// socketScheme is what the address of a daemon's socket starts with.
	socketScheme = "unix://"
)

// Remote is a daemon the CLI talks to.
type Remote struct {
	// Address is a gRPC target: unix:///path/to/socket, or HOST:PORT for a
	// daemon's TCP listener.
	Address string `yaml:"address"`

	// TLS is what a TCP address is reached with. Nil is plaintext.
	TLS *TLS `yaml:"tls,omitempty"`
}

// IsAddress reports whether s is an address rather than a remote's name,
// which holds neither ':' nor '/'.
func IsAddress(s string) bool {
	return strings.ContainsAny(s, ":/")
}

// Parse returns the remote an address names, with no TLS.
func Parse(address string) (Remote, error) {
	r := Remote{Address: address}
	if err := r.Validate(); err != nil {
		return Remote{}, err
	}

	return r, nil
}

// Validate returns an error if the remote cannot be connected to. The TLS
// files are not read.
func (r Remote) Validate() error {
	if path, ok := strings.CutPrefix(r.Address, socketScheme); ok {
		switch {
		case !filepath.IsAbs(path):
			return errdefs.InvalidArgument("invalid address %q: the socket path must be absolute", r.Address)
		case r.TLS != nil:
			return errdefs.InvalidArgument(
				"a unix:// remote cannot have tls: a socket is controlled by its file permissions")
		}
		return nil
	}

	if _, _, err := net.SplitHostPort(strings.TrimPrefix(r.Address, "dns:///")); err != nil {
		return errdefs.InvalidArgument("invalid address %q: want %s/PATH or HOST:PORT", r.Address, socketScheme)
	}
	return r.TLS.Validate()
}

// ClientOptions returns what dicer.NewClient needs to reach the remote. The
// TLS files are read here, so a missing one is reported.
func (r Remote) ClientOptions() ([]dicer.Option, error) {
	opts := []dicer.Option{dicer.WithAddress(r.Address)}
	if r.TLS != nil {
		cfg, err := r.TLS.Config()
		if err != nil {
			return nil, err
		}
		opts = append(opts, dicer.WithTLS(cfg))
	}

	return opts, nil
}
