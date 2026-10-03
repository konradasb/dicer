// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package virtiofs shares host directories with guests: it runs virtiofsd,
// the host's virtio-fs daemon, for each, on a socket the VMM connects to.
//
// virtiofsd is the host's, installed from its package. It serves one
// directory to one VMM, and exits when the VMM disconnects from it, so a
// share lasts as long as the guest it serves.
//
// The daemon itself may run with most of the filesystem read-only, as
// systemd's ProtectSystem makes it; a directory shared with a guest must be
// writable, so virtiofsd is started in the host's mount namespace, PID 1's,
// rather than the daemon's.
package virtiofs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/konradasb/dicer/internal/hostfs"
	"github.com/konradasb/dicer/internal/process"
)

const (
	// socketWaitTimeout bounds how long Start waits for virtiofsd to listen.
	socketWaitTimeout = 5 * time.Second
	// socketPollInterval is how often Start looks for the socket.
	socketPollInterval = 10 * time.Millisecond
	// probeTimeout bounds asking virtiofsd what it can do.
	probeTimeout = 5 * time.Second
	// maxSocketPath is the longest path a Unix socket can be bound to.
	maxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1
)

// ErrNotInstalled is Find's error when there is no virtiofsd to be found.
var ErrNotInstalled = errors.New("virtiofsd is not installed: install the virtiofsd package to mount directories")

// packagedPaths are where the distributions' packages install virtiofsd,
// which Find looks in after the PATH.
func packagedPaths() []string {
	return []string{"/usr/libexec/virtiofsd", "/usr/lib/virtiofsd", "/usr/lib/qemu/virtiofsd"}
}

// Find returns the path of virtiofsd: path, if it is given, or else the
// first found on the PATH or where the distributions' packages put it.
func Find(path string) (string, error) {
	return find(path, exec.LookPath, packagedPaths())
}

func find(path string, lookPath func(string) (string, error), candidates []string) (string, error) {
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("virtiofsd: %w", err)
		}
		return path, nil
	}

	if p, err := lookPath("virtiofsd"); err == nil {
		return p, nil
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", ErrNotInstalled
}

// Share is a host directory to share with a guest.
type Share struct {
	// Dir is the host directory, as the host's mount namespace has it.
	Dir string
	// Socket is the path virtiofsd listens on for the VMM.
	Socket string
	// ReadOnly makes virtiofsd refuse the guest's writes.
	ReadOnly bool
	// Log is the file virtiofsd's output is written to.
	Log string
}

// Daemon starts virtiofsd. It is safe for concurrent use.
type Daemon struct {
	binary string
	// enforcesReadOnly reports whether this virtiofsd has --readonly, and
	// so can refuse a guest's writes itself.
	enforcesReadOnly bool
	// enter is the command that runs virtiofsd in the host's mount
	// namespace, if the daemon's own is another.
	enter []string
}

// New returns a Daemon for the virtiofsd at binary, after asking it which
// options it has.
func New(ctx context.Context, binary string) (*Daemon, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	help, err := exec.CommandContext(ctx, binary, "--help").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("virtiofsd: %s --help: %w", binary, err)
	}

	d := &Daemon{binary: binary, enforcesReadOnly: strings.Contains(string(help), "--readonly")}
	if hostfs.Separate() {
		nsenter, err := exec.LookPath("nsenter")
		if err != nil {
			return nil, fmt.Errorf("virtiofsd: the daemon runs in a mount namespace of its own, "+
				"and nsenter, which virtiofsd needs to share the host's, is not installed: %w", err)
		}
		d.enter = []string{nsenter, "--mount=" + hostfs.MountNamespace, "--"}
	}
	return d, nil
}

// args returns the command line that runs virtiofsd for s.
func (d *Daemon) args(s Share) []string {
	argv := append([]string(nil), d.enter...)
	argv = append(argv, d.binary,
		"--socket-path", s.Socket,
		"--shared-dir", s.Dir,
		// Metadata is checked again after a second, so a change made on the
		// host reaches the guest soon, and files are cached in between.
		"--cache", "auto",
		"--sandbox", "namespace",
		"--announce-submounts",
	)
	if s.ReadOnly {
		argv = append(argv, "--readonly")
	}
	return argv
}

// Start runs virtiofsd for s and waits for it to listen. The process ends
// when the VMM that connects to it does, or when it is terminated.
func (d *Daemon) Start(ctx context.Context, s Share) (*process.Process, error) {
	if s.ReadOnly && !d.enforcesReadOnly {
		// The guest's own read-only mount is no protection: its root can
		// remount the share writable.
		return nil, fmt.Errorf("share %s: this virtiofsd cannot share a directory read-only: "+
			"install a newer one, which has --readonly", s.Dir)
	}
	if len(s.Socket) > maxSocketPath {
		return nil, fmt.Errorf("share %s: socket path %s is longer than %d bytes: choose a shorter run_dir",
			s.Dir, s.Socket, maxSocketPath)
	}

	if err := os.Remove(s.Socket); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket %s: %w", s.Socket, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Log), 0o750); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logFile, err := os.OpenFile(s.Log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open virtiofsd log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	argv := d.args(s)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // virtiofsd outlives ctx, as the VMM does
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// A process group of its own, so that signals sent to the daemon's group
	// do not reach virtiofsd.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	p, err := process.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start virtiofsd: %w", err)
	}

	if err := waitForSocket(ctx, s.Socket, p.Done()); err != nil {
		p.Terminate()
		if out, readErr := os.ReadFile(s.Log); readErr == nil && len(out) > 0 {
			return nil, fmt.Errorf("share %s: %w: %s", s.Dir, err, strings.TrimSpace(string(out)))
		}
		return nil, fmt.Errorf("share %s: %w", s.Dir, err)
	}
	return p, nil
}

// waitForSocket waits for virtiofsd to create its socket, or to exit.
func waitForSocket(ctx context.Context, socket string, exited <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, socketWaitTimeout)
	defer cancel()

	ticker := time.NewTicker(socketPollInterval)
	defer ticker.Stop()
	for {
		if info, err := os.Stat(socket); err == nil && info.Mode()&fs.ModeSocket != 0 {
			return nil
		}
		select {
		case <-exited:
			return errors.New("virtiofsd exited")
		case <-ctx.Done():
			return fmt.Errorf("virtiofsd did not listen on %s: %w", socket, ctx.Err())
		case <-ticker.C:
		}
	}
}
