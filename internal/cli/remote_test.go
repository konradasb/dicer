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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
	"github.com/dicer-sh/dicer/internal/certificate"
	"github.com/dicer-sh/dicer/internal/cli/remote"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/grpcapi"
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

// serveDaemon serves the API over mutual TLS on loopback, as dicerd does with
// api.tcp.listen set, and returns the access manager behind it.
func serveDaemon(t *testing.T) *access.Manager {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)
	definitions, err := filestore.NewManager(filestore.Config{
		DataDir: filepath.Join(t.TempDir(), "data"),
		Logger:  logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	pair, err := certificate.LoadOrGenerate(t.TempDir(), "dicerd")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := certificate.FingerprintOf(pair)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	m := access.NewManager(access.Config{
		Store:       definitions,
		Fingerprint: fingerprint,
		// An address nothing listens on comes first, as a host's
		// unreachable interface might: enrolment must move on from it.
		Addresses: []string{"127.0.0.1:1", listener.Addr().String()},
		Logger:    logger,
	})

	auth := grpcapi.CertificateAuthentication(m)
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(certificate.ServerConfig(pair))),
		grpc.ChainUnaryInterceptor(auth.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(auth.StreamInterceptor()),
	)
	grpcapi.NewServer(grpcapi.Config{Definitions: definitions, Access: m}).Register(server)

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	return m
}

func TestRemoteCreateEnrolsAndCommandsUseIt(t *testing.T) {
	isolateConfig(t)
	daemon := serveDaemon(t)

	join, _, err := daemon.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "remote", "create", "prod", join.String())
	if err != nil {
		t.Fatalf("remote create: %v\n%s", err, out)
	}

	// The remote is reachable by name, as this client, and the daemon
	// knows it by the name the token gave it.
	out, err = run(t, "--remote", "prod", "client", "list", "--format", "json")
	if err != nil {
		t.Fatalf("client list on the new remote: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"laptop"`) {
		t.Errorf("client list = %s, want the enrolled client", out)
	}

	// The daemon hands back the certificate this client enrolled with.
	out, err = run(t, "--remote", "prod", "client", "show", "laptop", "--pem")
	if err != nil {
		t.Fatalf("client show --pem: %v\n%s", err, out)
	}
	dir, _ := remote.Dir()
	pair, err := remote.Identity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out != certificate.EncodePEM(pair.Certificate[0]) {
		t.Errorf("client show --pem = %q, want this client's certificate", out)
	}

	// And once it is current, without naming it.
	if out, err := run(t, "remote", "use", "prod"); err != nil {
		t.Fatalf("remote use: %v\n%s", err, out)
	}
	if out, err := run(t, "client", "list"); err != nil {
		t.Fatalf("client list on the current remote: %v\n%s", err, out)
	}
}

func TestRemoteCreateWithSpentTokenFails(t *testing.T) {
	isolateConfig(t)
	daemon := serveDaemon(t)

	join, _, err := daemon.CreateToken("laptop", dicer.DefaultTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "remote", "create", "prod", join.String()); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "remote", "create", "again", join.String()); err == nil {
		t.Error("a spent token enrolled a second time")
	}

	// A failed enrolment leaves no remote behind.
	dir, _ := remote.Dir()
	cfg, _ := remote.Load(dir)
	if _, err := cfg.Get("again"); err == nil {
		t.Error("the failed remote was saved")
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
	_ = cfg.Create("current", dicer.SocketRemote("/run/current.sock"))
	_ = cfg.Create("env", dicer.SocketRemote("/run/env.sock"))
	_ = cfg.Create("flag", dicer.SocketRemote("/run/flag.sock"))

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
