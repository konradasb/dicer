// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"net"
	"path/filepath"
	"strings"

	"github.com/dicer-sh/dicer/internal/certificate"
)

const (
	// DefaultSocket is the daemon's API socket, matching the daemon's
	// default. It is where a client looks unless told otherwise.
	DefaultSocket = "/run/dicer/dicer.sock"

	// The schemes a remote's address may have.
	schemeUnix = "unix://"
	schemeTCP  = "tcp://"
)

// Remote is a daemon a client can talk to: where it is, and -- over the
// network -- which certificate it must present.
type Remote struct {
	// Address is where: "unix:///path/to/socket", or "tcp://host:port"
	// for a daemon on another machine, reached over mutual TLS.
	Address string `yaml:"address"`

	// Fingerprint pins a TCP remote's certificate. A client talks to no
	// daemon that presents another.
	Fingerprint string `yaml:"fingerprint,omitempty"`
}

// IsLocal reports whether the remote is reached through a Unix socket.
func (r Remote) IsLocal() bool {
	return strings.HasPrefix(r.Address, schemeUnix)
}

// SocketPath returns a local remote's socket.
func (r Remote) SocketPath() string {
	return strings.TrimPrefix(r.Address, schemeUnix)
}

// HostPort returns a TCP remote's host:port.
func (r Remote) HostPort() string {
	return strings.TrimPrefix(r.Address, schemeTCP)
}

// Validate reports whether the remote can be connected to.
func (r Remote) Validate() error {
	switch {
	case r.IsLocal():
		if !filepath.IsAbs(r.SocketPath()) {
			return InvalidArgument("invalid address %q: the socket path must be absolute", r.Address)
		}
		return nil
	case strings.HasPrefix(r.Address, schemeTCP):
		if _, _, err := net.SplitHostPort(r.HostPort()); err != nil {
			return InvalidArgument("invalid address %q: want tcp://HOST:PORT", r.Address)
		}
		if r.Fingerprint == "" {
			return InvalidArgument("invalid address %q: a daemon reached over the network is pinned to "+
				"its certificate, so its fingerprint is needed too", r.Address)
		}
		if err := certificate.ValidateFingerprint(r.Fingerprint); err != nil {
			return InvalidArgument("invalid address %q: %s", r.Address, err)
		}
		return nil
	default:
		return InvalidArgument("invalid address %q: want %sPATH or %sHOST:PORT", r.Address, schemeUnix, schemeTCP)
	}
}

// LocalRemote returns the daemon on this machine, on its default socket.
func LocalRemote() Remote {
	return Remote{Address: schemeUnix + DefaultSocket}
}

// SocketRemote returns a remote for the daemon listening on a Unix socket.
func SocketRemote(path string) Remote {
	return Remote{Address: schemeUnix + path}
}

// TCPRemote returns a remote for the daemon at host:port, whose certificate
// has the given fingerprint.
func TCPRemote(hostPort, fingerprint string) Remote {
	return Remote{Address: schemeTCP + hostPort, Fingerprint: fingerprint}
}

// ParseAddress returns the remote an address names on its own, with nothing
// else to go on. Only a socket can be named that way: a TCP remote needs its
// fingerprint too, which only enrolling provides.
func ParseAddress(address string) (Remote, error) {
	if strings.HasPrefix(address, schemeTCP) {
		return Remote{}, InvalidArgument("a TCP remote must be created from an enrolment token, " +
			"so that its certificate is pinned")
	}

	r := Remote{Address: address}
	if err := r.Validate(); err != nil {
		return Remote{}, err
	}

	return r, nil
}

// IsAddress reports whether s is an address rather than a remote's name. The
// two cannot be confused: a name has no scheme.
func IsAddress(s string) bool {
	return strings.Contains(s, "://")
}
