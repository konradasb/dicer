// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/spf13/cobra"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/version"
)

// NewCommand returns the root command for the guest init process.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "dicer-init",
		Short:         "Boot a Dicer guest: runs as PID 1 inside the VM",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version.Version,
		Args:          cobra.NoArgs,
		RunE:          runRoot,
	}

	cmd.SetVersionTemplate(version.String() + "\n")

	cmd.Flags().StringP("config", "c", guest.ConfigFile, "Config filename on the config disk ("+configDiskDevice+")")

	return cmd
}

func runRoot(cmd *cobra.Command, _ []string) error {
	// Best-effort early mounts before logging is available. Safe to call when
	// filesystems are already mounted; the syscalls return EBUSY and are ignored.
	mountEarlyFilesystems()

	file, path := openConsole()
	os.Stdout = file
	os.Stderr = file
	log := slog.New(slog.NewTextHandler(&console{path: path, f: file}, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	configFile, err := cmd.Flags().GetString("config")
	if err != nil {
		return fmt.Errorf("get config flag: %w", err)
	}

	boot(log, configFile)
	return nil
}

// openConsole returns the first available virtio or serial console device,
// and its path; or stderr, and no path, if none can be opened.
func openConsole() (*os.File, string) {
	for _, path := range []string{"/dev/hvc0", "/dev/ttyAMA0", "/dev/ttyS0"} {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			continue
		}

		return f, path
	}

	return os.Stderr, ""
}

// console is where dicer-init logs. It is reopened when a write fails, since
// a workload's init may hang up the terminal.
type console struct {
	mu   sync.Mutex
	path string // empty if the console cannot be reopened
	f    *os.File
}

func (c *console) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n, err := c.f.Write(p)
	if err == nil || c.path == "" {
		return n, err
	}

	f, openErr := os.OpenFile(c.path, os.O_WRONLY, 0)
	if openErr != nil {
		return n, err
	}
	c.f = f // the old file is left open: os.Stdout and os.Stderr still refer to it
	return c.f.Write(p)
}
