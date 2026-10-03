// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package virtiofs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeEnv makes the test binary act as virtiofsd: one that answers --help,
// mentioning --readonly unless the variable says "old", and listens on
// --socket-path until it is killed, or exits at once if it says "fail".
const fakeEnv = "DICER_FAKE_VIRTIOFSD"

func TestMain(m *testing.M) {
	if mode, ok := os.LookupEnv(fakeEnv); ok && len(os.Args) > 1 {
		os.Exit(fakeVirtiofsd(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeVirtiofsd(mode string, args []string) int {
	if args[0] == "--help" {
		if mode != "old" {
			fmt.Println("      --readonly    Read-only mode")
		}
		return 0
	}
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "Error: shared directory is not a directory")
		return 1
	}

	i := slices.Index(args, "--socket-path")
	if i < 0 || i+1 >= len(args) {
		return 2
	}
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", args[i+1])
	if err != nil {
		return 3
	}
	defer func() { _ = l.Close() }()
	time.Sleep(time.Minute)
	return 0
}

// fake returns a Daemon running the test binary as virtiofsd, in mode.
func fake(t *testing.T, mode string) *Daemon {
	t.Helper()
	t.Setenv(fakeEnv, mode)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(t.Context(), self)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFind(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "virtiofsd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got, err := Find(bin); err != nil || got != bin {
		t.Errorf("Find(%s) = %s, %v; want it as given", bin, got, err)
	}
	if _, err := Find(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("Find of a path that is not there succeeded")
	}

	// Unconfigured, it is looked for on the PATH, then where packages put it.
	onPath := func(string) (string, error) { return bin, nil }
	notOnPath := func(string) (string, error) { return "", errors.New("not found") }
	if got, err := find("", onPath, nil); err != nil || got != bin {
		t.Errorf("find() = %s, %v; want the one on the PATH", got, err)
	}
	if got, err := find("", notOnPath, []string{filepath.Join(t.TempDir(), "none"), bin}); err != nil || got != bin {
		t.Errorf("find() = %s, %v; want the packaged one", got, err)
	}
	if _, err := find("", notOnPath, nil); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("find() with none = %v, want ErrNotInstalled", err)
	}
}

func TestArgs(t *testing.T) {
	d := &Daemon{binary: "/usr/libexec/virtiofsd", enforcesReadOnly: true}

	got := d.args(Share{Dir: "/home/me/src", Socket: "/run/fs0.sock", ReadOnly: true})
	want := []string{
		"/usr/libexec/virtiofsd", "--socket-path", "/run/fs0.sock", "--shared-dir", "/home/me/src",
		"--cache", "auto", "--sandbox", "namespace", "--announce-submounts", "--readonly",
	}
	if !slices.Equal(got, want) {
		t.Errorf("args = %q\nwant  %q", got, want)
	}
	if slices.Contains(d.args(Share{Dir: "/src", Socket: "/run/fs0.sock"}), "--readonly") {
		t.Error("a writable share was made read-only")
	}

	// From a mount namespace of its own, the daemon runs it in the host's.
	entered := &Daemon{binary: "virtiofsd", enter: []string{"/usr/bin/nsenter", "--mount=/proc/1/ns/mnt", "--"}}
	if got := entered.args(Share{Dir: "/d", Socket: "/s"}); !slices.Equal(got[:4], []string{
		"/usr/bin/nsenter", "--mount=/proc/1/ns/mnt", "--", "virtiofsd",
	}) {
		t.Errorf("args = %q, want virtiofsd run in PID 1's mount namespace", got)
	}
}

func TestNewProbesReadOnly(t *testing.T) {
	if !fake(t, "new").enforcesReadOnly {
		t.Error("a virtiofsd that has --readonly was taken not to")
	}
	if fake(t, "old").enforcesReadOnly {
		t.Error("a virtiofsd without --readonly was taken to have it")
	}
}

func TestStart(t *testing.T) {
	d := fake(t, "new")
	dir := t.TempDir()
	socket := filepath.Join(dir, "fs0.sock")
	// A socket left by an earlier start is replaced.
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := d.Start(t.Context(), Share{Dir: t.TempDir(), Socket: socket, Log: filepath.Join(dir, "logs", "fs0.log")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(p.Terminate)

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "unix", socket)
	if err != nil {
		t.Fatalf("nothing listens on the socket: %v", err)
	}
	_ = conn.Close()

	p.Terminate()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Error("virtiofsd did not end when terminated")
	}
}

func TestStartRefusesWhatItCannotDo(t *testing.T) {
	dir := t.TempDir()
	share := Share{Dir: t.TempDir(), Socket: filepath.Join(dir, "fs0.sock"), Log: filepath.Join(dir, "fs0.log")}

	// A virtiofsd without --readonly cannot keep a guest from writing: the
	// share is refused, not made writable in all but name.
	readOnly := share
	readOnly.ReadOnly = true
	if _, err := fake(t, "old").Start(t.Context(), readOnly); err == nil ||
		!strings.Contains(err.Error(), "cannot share a directory read-only") {
		t.Errorf("Start read-only on an old virtiofsd = %v, want it refused", err)
	}

	long := share
	long.Socket = "/" + strings.Repeat("x", maxSocketPath) + ".sock"
	if _, err := fake(t, "new").Start(t.Context(), long); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("Start on a socket path too long to bind = %v, want it refused", err)
	}
}

func TestStartReportsWhyItFailed(t *testing.T) {
	d := fake(t, "fail")
	dir := t.TempDir()

	_, err := d.Start(t.Context(), Share{Dir: "/nope", Socket: filepath.Join(dir, "fs0.sock"), Log: filepath.Join(dir, "fs0.log")})
	if err == nil || !strings.Contains(err.Error(), "shared directory is not a directory") {
		t.Errorf("Start = %v, want virtiofsd's own error", err)
	}
}
