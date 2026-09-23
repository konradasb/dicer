// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/cli/remote"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/grpcapi"
)

// run executes the dicer command line with args and returns what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

// isolateConfig gives the test a configuration directory of its own, and
// clears whatever remote the environment names.
func isolateConfig(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv(remote.DirEnv, dir)
	t.Setenv(remoteEnv, "")

	return dir
}

// serveDaemon serves the API on loopback, as dicerd does with api.tcp.listen
// set. It returns the address to reach it at.
func serveDaemon(t *testing.T) string {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)
	definitions, err := filestore.NewManager(filestore.Config{
		DataDir: filepath.Join(t.TempDir(), "data"),
		Logger:  logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	server := newTestServer()
	grpcapi.NewServer(grpcapi.Config{
		Definitions: definitions, APIAddress: listener.Addr().String(),
	}).Register(server)

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	return listener.Addr().String()
}

// The whole of setting up a remote: an address is recorded, and commands go
// to it.
func TestRemoteCreateAndCommandsUseIt(t *testing.T) {
	isolateConfig(t)

	address := serveDaemon(t)

	out, err := run(t, "remote", "create", "prod", address)
	if err != nil {
		t.Fatalf("remote create: %v\n%s", err, out)
	}

	// Reachable by name, as this client.
	if out, err := run(t, "--remote", "prod", "network", "list"); err != nil {
		t.Fatalf("network list on the new remote: %v\n%s", err, out)
	}

	// And once it is current, without naming it.
	if out, err := run(t, "remote", "use", "prod"); err != nil {
		t.Fatalf("remote use: %v\n%s", err, out)
	}
	if out, err := run(t, "network", "list"); err != nil {
		t.Fatalf("network list on the current remote: %v\n%s", err, out)
	}
}

func TestRemoteCreateFromSocketAddress(t *testing.T) {
	isolateConfig(t)

	if out, err := run(t, "remote", "create", "test", "unix:///run/dicer-test/dicer.sock"); err != nil {
		t.Fatalf("remote create: %v\n%s", err, out)
	}

	out, err := run(t, "remote", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{remote.Local, "test", "unix:///run/dicer-test/dicer.sock"} {
		if !strings.Contains(out, want) {
			t.Errorf("remote list = %q, missing %q", out, want)
		}
	}
}

func TestResolveTargetPrecedence(t *testing.T) {
	dir := isolateConfig(t)

	cfg, _ := remote.Load(dir)
	_ = cfg.Create("current", remote.Remote{Address: "unix:///run/current.sock"})
	_ = cfg.Create("env", remote.Remote{Address: "unix:///run/env.sock"})
	_ = cfg.Create("flag", remote.Remote{Address: "unix:///run/flag.sock"})

	resolve := func(args ...string) string {
		t.Helper()

		cmd := NewCommand()
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		target, err := resolveTarget(cmd)
		if err != nil {
			t.Fatalf("resolveTarget(%v): %v", args, err)
		}
		return target.name
	}
	save := func() {
		t.Helper()
		if err := cfg.Save(dir); err != nil {
			t.Fatal(err)
		}
	}

	save()
	if got := resolve(); got != remote.Local {
		t.Errorf("with nothing chosen: %q, want %q", got, remote.Local)
	}

	_ = cfg.Use("current")
	save()
	if got := resolve(); got != "current" {
		t.Errorf("with a current remote: %q, want current", got)
	}

	t.Setenv(remoteEnv, "env")
	if got := resolve(); got != "env" {
		t.Errorf("with $%s: %q, want env", remoteEnv, got)
	}

	if got := resolve("--remote", "flag"); got != "flag" {
		t.Errorf("with --remote: %q, want flag", got)
	}

	// A socket address needs nothing configured.
	if got := resolve("-r", "unix:///run/adhoc.sock"); got != "unix:///run/adhoc.sock" {
		t.Errorf("with an address: %q", got)
	}
}
