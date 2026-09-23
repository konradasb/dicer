// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package access

import "context"

// Identity is who a request comes from: a local user of the API socket, or a
// trusted client by name.
//
// Every caller may do everything. Identity makes each request attributable --
// the audit log names it -- and gives deciding what a caller may do one place
// to live.
type Identity struct {
	// client is the trusted client's name, or empty for a local caller.
	client string
}

// LocalIdentity is the identity of a caller on the API socket.
func LocalIdentity() Identity {
	return Identity{}
}

// ClientIdentity is the identity of the trusted client with the given name.
func ClientIdentity(name string) Identity {
	return Identity{client: name}
}

// IsLocal reports whether the caller is on the API socket.
func (i Identity) IsLocal() bool {
	return i.client == ""
}

// String names the caller for a log: "local", or "client/<name>".
func (i Identity) String() string {
	if i.IsLocal() {
		return "local"
	}

	return "client/" + i.client
}

type identityKey struct{}

// NewContext returns a copy of ctx carrying the caller's identity.
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// FromContext returns the caller's identity, and whether ctx carries one. A
// request that reached a handler without one was not authenticated, which is
// a bug in how the server was assembled.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
