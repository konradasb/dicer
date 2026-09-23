// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hypervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/konradasb/dicer/internal/process"
)

const (
	// socketWaitTimeout bounds how long StartProcess waits for the VMM to
	// create its API socket before treating the start as failed.
	socketWaitTimeout = 5 * time.Second

	// socketPollInterval is how often StartProcess checks for the socket.
	socketPollInterval = 10 * time.Millisecond

	// socketProbeTimeout bounds a liveness probe of an existing socket.
	socketProbeTimeout = 100 * time.Millisecond
)

// LogPath returns the file the VMM serving its API on socketPath writes its
// own output to. It sits beside the socket, so it goes wherever the socket
// goes.
func LogPath(socketPath string) string {
	return filepath.Join(filepath.Dir(socketPath), "logs", "vmm.log")
}

// StartProcess launches a VMM detached from the daemon and waits for it to
// serve on socketPath. ctx bounds only the wait; on failure the process is
// killed and the error includes its log.
func StartProcess(ctx context.Context, socketPath, binaryPath string, args ...string) (*process.Process, error) {
	if socketInUse(socketPath) {
		return nil, fmt.Errorf("socket %s is already in use; is a VMM already running?", socketPath)
	}

	// A stale socket from a VMM that died would make the new one fail to
	// bind. If it cannot be removed, the VMM reports why.
	_ = os.Remove(socketPath)

	logPath := LogPath(socketPath)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open vmm log: %w", err)
	}
	// The child holds its own copy of the descriptor once started.
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(binaryPath, args...) //nolint:noctx // the VMM must outlive ctx; see above
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// A process group of its own, so signals aimed at the daemon's group do
	// not take the VMs with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	p, err := process.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", filepath.Base(binaryPath), err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, socketWaitTimeout)
	defer cancel()

	if err := waitForSocket(waitCtx, socketPath, p.Done()); err != nil {
		p.Terminate()
		if log, readErr := os.ReadFile(logPath); readErr == nil && len(log) > 0 {
			return nil, fmt.Errorf("%w: vmm log: %s", err, log)
		}
		return nil, err
	}

	return p, nil
}

// socketInUse reports whether something accepts connections on a Unix socket.
func socketInUse(path string) bool {
	conn, err := net.DialTimeout("unix", path, socketProbeTimeout) //nolint:noctx // a bounded local probe with nothing to cancel
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitForSocket polls until a Unix socket accepts connections, giving up
// early if the VMM that was to serve it exits.
func waitForSocket(ctx context.Context, path string, exited <-chan struct{}) error {
	ticker := time.NewTicker(socketPollInterval)
	defer ticker.Stop()

	var d net.Dialer
	for {
		select {
		case <-exited:
			return errors.New("the VMM exited before serving its API")
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("timed out waiting for the VMM API socket")
			}
			return ctx.Err()
		case <-ticker.C:
			if conn, err := d.DialContext(ctx, "unix", path); err == nil {
				_ = conn.Close()
				return nil
			}
		}
	}
}
