// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// importingDaemon keeps what an ImportKernel call sends.
type importingDaemon struct {
	dicerdv1.UnimplementedDaemonServiceServer

	start *dicerdv1.ImportKernelStart
	data  []byte
}

func (d *importingDaemon) ImportKernel(
	stream grpc.ClientStreamingServer[dicerdv1.ImportKernelRequest, dicerdv1.Kernel],
) error {
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&dicerdv1.Kernel{Name: d.start.GetName()})
		}
		if err != nil {
			return err
		}
		if start := req.GetStart(); start != nil {
			d.start = start
		}
		d.data = append(d.data, req.GetData()...)
	}
}

// TestKernelImportSendsTheFile checks that kernel import sends the file it
// is given, with its name, architecture and checksum, and sends nothing for
// a file that is not there.
func TestKernelImportSendsTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(file, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}

	daemon := &importingDaemon{}
	serveFakeDaemon(t, daemon)
	if out, err := run(t, "kernel", "import", "k", file, "--arch", "x86_64", "--sha256", "ab"); err != nil {
		t.Fatalf("kernel import: %v\n%s", err, out)
	}
	start := daemon.start
	if start.GetName() != "k" || start.GetArch() != dicerdv1.Architecture_ARCHITECTURE_X86_64 ||
		start.GetSha256() != "ab" || string(daemon.data) != "kernel" {
		t.Errorf("sent %+v and %q, want the kernel k for x86_64 with its checksum, and the file", start, daemon.data)
	}

	daemon = &importingDaemon{}
	serveFakeDaemon(t, daemon)
	if _, err := run(t, "kernel", "import", "k", filepath.Join(t.TempDir(), "missing"), "--arch", "x86_64"); err == nil {
		t.Error("importing a file that is not there succeeded")
	}
	if daemon.start != nil {
		t.Error("a file that is not there was imported")
	}
}
