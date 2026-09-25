// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"
)

// entrypointCommand is the hidden subcommand bootExec runs dicer-init again as,
// in the entrypoint's own PID and mount namespaces, to start it there.
const entrypointCommand = "entrypoint"

// Exit statuses for an entrypoint that could not be started, as a shell
// reports them.
const (
	exitCannotExecute = 126
	exitNotFound      = 127
)

func newEntrypointCommand() *cobra.Command {
	return &cobra.Command{
		Use:    entrypointCommand + " -- COMMAND [ARG...]",
		Short:  "Start the entrypoint in the namespaces dicer-init made for it",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		Run: func(_ *cobra.Command, argv []string) {
			os.Exit(runEntrypoint(argv))
		},
	}
}

// runEntrypoint mounts a /proc of the entrypoint's own PID namespace, then
// replaces itself with the entrypoint, which so becomes the namespace's PID 1.
// Without its own /proc the entrypoint would see the machine's processes under
// IDs other than its own, and anything reading /proc/<pid> -- ps, or
// containerd looking up its parent -- would find the wrong process or none.
//
// It returns only if the entrypoint could not be started, with the status a
// shell would give.
func runEntrypoint(argv []string) int {
	// bootExec starts it as the first process of a new PID namespace. Run any
	// other way, it would mount a /proc over the machine's own.
	if os.Getpid() != 1 {
		return entrypointFailed("start "+argv[0],
			errors.New("not the first process of a PID namespace: dicer-init starts this itself"), exitCannotExecute)
	}

	// The new /proc must stay in this mount namespace, not propagate back to
	// dicer-init's.
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return entrypointFailed("make mounts private", err, exitCannotExecute)
	}
	const flags = syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC
	if err := syscall.Mount("proc", "/proc", "proc", flags, ""); err != nil {
		return entrypointFailed("mount /proc", err, exitCannotExecute)
	}

	path, err := exec.LookPath(argv[0])
	if err != nil {
		status := exitCannotExecute
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			status = exitNotFound
		}
		return entrypointFailed("start "+argv[0], err, status)
	}

	err = syscall.Exec(path, argv, os.Environ())

	status := exitCannotExecute
	if errors.Is(err, syscall.ENOENT) {
		status = exitNotFound
	}
	return entrypointFailed("start "+argv[0], err, status)
}

// entrypointFailed says on the console why the entrypoint did not start, and
// returns the status to exit with.
func entrypointFailed(what string, err error, status int) int {
	fmt.Fprintf(os.Stderr, "dicer-init: %s: %v\n", what, err)
	return status
}
