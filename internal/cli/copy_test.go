// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/archive"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestParseCopyEndpoint(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want copyEndpoint
	}{
		{"web:/srv", copyEndpoint{instance: "web", path: "/srv"}},
		{"web:srv/app", copyEndpoint{instance: "web", path: "srv/app"}},
		{"./app", copyEndpoint{path: "./app"}},
		{"/tmp/x", copyEndpoint{path: "/tmp/x"}},
		// A slash before the colon makes it a local path.
		{"./a:b", copyEndpoint{path: "./a:b"}},
		{"/tmp/a:b", copyEndpoint{path: "/tmp/a:b"}},
	} {
		if got := parseCopyEndpoint(tc.arg); got != tc.want {
			t.Errorf("parseCopyEndpoint(%q) = %+v, want %+v", tc.arg, got, tc.want)
		}
	}
}

// fakeCopyDaemon serves the copy RPCs as the daemon and the guest agent do
// between them, with a directory standing in for the guest's filesystem.
type fakeCopyDaemon struct {
	dicerdv1.UnimplementedDaemonServiceServer
	guest string
}

func (d *fakeCopyDaemon) CopyToInstance(
	stream grpc.ClientStreamingServer[dicerdv1.CopyToInstanceRequest, emptypb.Empty],
) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}

	err = archive.Receive(func() ([]byte, error) {
		msg, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return msg.GetData(), nil
	}, filepath.Join(d.guest, req.GetStart().GetPath()))
	if err != nil {
		return err
	}

	return stream.SendAndClose(&emptypb.Empty{})
}

func (d *fakeCopyDaemon) CopyFromInstance(
	req *dicerdv1.CopyFromInstanceRequest, stream grpc.ServerStreamingServer[dicerdv1.CopyFromInstanceResponse],
) error {
	_, err := archive.Send(filepath.Join(d.guest, req.GetPath()), func(chunk []byte) error {
		return stream.Send(&dicerdv1.CopyFromInstanceResponse{Data: chunk})
	})
	return err
}

// serveCopyDaemon starts a fakeCopyDaemon on a socket, and returns the
// --remote address for it and the directory standing in for the guest.
func serveCopyDaemon(t *testing.T) (string, string) {
	t.Helper()
	isolateConfig(t)

	// A short directory: a socket path is limited to about 100 bytes, and
	// a test's temporary directory can be most of that on its own.
	dir, err := os.MkdirTemp("", "dicer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "d.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}

	guest := t.TempDir()
	server := newTestServer()
	dicerdv1.RegisterDaemonServiceServer(server, &fakeCopyDaemon{guest: guest})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	return "unix://" + socket, guest
}

func TestCopyRoundTrip(t *testing.T) {
	remote, guest := serveCopyDaemon(t)
	if err := os.MkdirAll(filepath.Join(guest, "srv"), 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "app")
	writeTestFile(t, filepath.Join(src, "bin", "run"), "#!/bin/sh\n")

	// In: into the guest's existing /srv, under its own name.
	out, err := run(t, "-r", remote, "instance", "cp", src, "web:/srv")
	if err != nil {
		t.Fatalf("cp in: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "Copied "+src+" to web:/srv (") {
		t.Errorf("output = %q, want it to say what was copied where", out)
	}
	if got := readTestFile(t, filepath.Join(guest, "srv", "app", "bin", "run")); got != "#!/bin/sh\n" {
		t.Errorf("guest file = %q", got)
	}

	// Out: to a path that does not exist, which it becomes.
	dest := filepath.Join(t.TempDir(), "restored")
	if out, err := run(t, "-r", remote, "instance", "cp", "web:/srv/app", dest); err != nil {
		t.Fatalf("cp out: %v\n%s", err, out)
	}
	if got := readTestFile(t, filepath.Join(dest, "bin", "run")); got != "#!/bin/sh\n" {
		t.Errorf("local file = %q", got)
	}
}

func TestCopyNeedsExactlyOneInstance(t *testing.T) {
	isolateConfig(t)

	if _, err := run(t, "instance", "cp", "./a", "./b"); err == nil ||
		!strings.Contains(err.Error(), "NAME:PATH") {
		t.Errorf("two local paths: %v, want a hint at NAME:PATH", err)
	}
	if _, err := run(t, "instance", "cp", "web:/a", "db:/b"); err == nil ||
		!strings.Contains(err.Error(), "between two instances") {
		t.Errorf("two instances: %v, want it refused", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// What the other end says went wrong is what the user sees, not a complaint
// about the archive it never sent.
func TestCopyFailureFromTheGuestIsReportedAsIs(t *testing.T) {
	remote, _ := serveCopyDaemon(t)

	_, err := run(t, "-r", remote, "instance", "cp", "web:/does/not/exist", t.TempDir())
	if err == nil {
		t.Fatal("copying a missing guest path succeeded")
	}
	if strings.Contains(err.Error(), "read archive") {
		t.Errorf("error = %q, want the guest's own error", err)
	}
}
