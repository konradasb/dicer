// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"crypto/tls"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/cli/remote"
)

// remoteEnv chooses the remote when --remote does not.
const remoteEnv = "DICER_REMOTE"

// target is the daemon a command talks to, and what it is called.
type target struct {
	name   string
	remote dicer.Remote
}

// String describes the target for a person: "prod (tcp://192.0.2.1:7443)".
func (t target) String() string {
	if t.name == t.remote.Address {
		return t.name
	}

	return t.name + " (" + t.remote.Address + ")"
}

// resolveTarget decides which daemon a command talks to: the one --remote
// names, then $DICER_REMOTE, then the current remote, then the local daemon.
//
// A remote is named, or given by its address if that is a socket's:
// "unix:///run/dicer-test/dicer.sock" needs nothing configured to be used.
func resolveTarget(cmd *cobra.Command) (target, error) {
	name, _ := cmd.Flags().GetString("remote")
	if name == "" {
		name = os.Getenv(remoteEnv)
	}

	if dicer.IsAddress(name) {
		r, err := dicer.ParseAddress(name)
		if err != nil {
			return target{}, err
		}
		return target{name: name, remote: r}, nil
	}

	dir, err := remote.Dir()
	if err != nil {
		return target{}, err
	}
	cfg, err := remote.Load(dir)
	if err != nil {
		return target{}, err
	}

	if name == "" {
		name = cfg.CurrentName()
	}
	r, err := cfg.Get(name)
	if err != nil {
		return target{}, err
	}

	return target{name: name, remote: r}, nil
}

// newClient connects to the daemon the command is aimed at. The returned
// function closes the connection.
//
// The CLI is the client package's first consumer, and uses nothing a program
// outside this repository could not: what it has that they do not is the
// remotes file, which it resolves to a remote here and passes in.
func newClient(cmd *cobra.Command) (*dicer.Client, func(), error) {
	t, err := resolveTarget(cmd)
	if err != nil {
		return nil, nil, err
	}

	if debugging(cmd) {
		newTracer(cmd).printf("remote %s", t)
	}

	opts := []dicer.Option{
		dicer.WithRemote(t.remote),
		dicer.WithDialOptions(dialOptions(cmd, t.String())...),
	}

	// A socket needs no identity, and asking for one would generate a key
	// for a user who may never talk to a daemon over the network.
	if !t.remote.IsLocal() {
		identity, err := clientIdentity()
		if err != nil {
			return nil, nil, err
		}
		opts = append(opts, dicer.WithIdentity(identity))
	}

	c, err := dicer.NewClient(opts...)
	if err != nil {
		// The client's own errors name the address they are about. Only a
		// remote known by a name adds anything, and only its name: saying
		// the address twice in one line helps nobody.
		if !dicer.IsAddress(t.name) {
			return nil, nil, fmt.Errorf("remote %s: %w", t, err)
		}
		return nil, nil, err
	}

	return c, func() { _ = c.Close() }, nil
}

// clientIdentity returns the key this user talks to daemons as.
func clientIdentity() (tls.Certificate, error) {
	dir, err := remote.Dir()
	if err != nil {
		return tls.Certificate{}, err
	}

	return remote.Identity(dir)
}
