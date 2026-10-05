// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/cli/remote"
	"github.com/konradasb/dicer/internal/grpcapi"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestExecute(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantOut  string
	}{
		{"success", nil, 0, ""},
		{"plain error", errors.New("boom"), 1, "Error: boom\n"},
		{
			"a daemon's error is printed as its message alone",
			status.Error(codes.NotFound, `no instance "web"`),
			1, "Error: no instance \"web\"\n",
		},
		{
			"however it is wrapped",
			fmt.Errorf("%w (did you mean web?)", status.Error(codes.NotFound, `no instance "wbe"`)),
			1, "Error: no instance \"wbe\" (did you mean web?)\n",
		},
		{"exit status passes through silently", &exitError{code: 42}, 42, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{
				Use:           "test",
				SilenceErrors: true,
				SilenceUsage:  true,
				RunE:          func(*cobra.Command, []string) error { return tt.err },
			}
			cmd.SetArgs(nil)

			var stderr bytes.Buffer
			if code := exitStatus(cmd, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stderr.String(); got != tt.wantOut {
				t.Errorf("stderr = %q, want %q", got, tt.wantOut)
			}
		})
	}
}

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
	t.Setenv(remote.ConfigDirEnv, dir)
	t.Setenv(remoteEnv, "")

	return dir
}

// serveFakeDaemon serves srv on a socket and aims commands at it through
// $DICER_REMOTE, as a completion, which takes no --remote, needs. It returns
// the socket's address.
func serveFakeDaemon(t *testing.T, srv dicerdv1.DaemonServiceServer) string {
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

	server := newTestServer()
	dicerdv1.RegisterDaemonServiceServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	address := "unix://" + socket
	t.Setenv(remoteEnv, address)
	return address
}

// newTestServer returns a gRPC server that sends errors as dicerd does.
func newTestServer() *grpc.Server {
	return grpc.NewServer(
		grpc.ChainUnaryInterceptor(grpcapi.UnaryStatusInterceptor),
		grpc.ChainStreamInterceptor(grpcapi.StreamStatusInterceptor),
	)
}
