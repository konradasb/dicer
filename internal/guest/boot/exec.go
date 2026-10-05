// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/konradasb/dicer/internal/guest"
)

// bootExec starts the guest agent, then runs the entrypoint in the overlay
// root as PID 1 of its own PID namespace, with a mount namespace and /proc of
// its own. When it exits, its exit code is written to the status disk and the
// machine ends. It does not return.
func bootExec(log *slog.Logger, cfg *guest.Config) {
	if err := syscall.Chroot(overlayRoot); err != nil {
		fatal(log, "chroot", err)
	}
	if err := os.Chdir("/"); err != nil {
		fatal(log, "chdir /", err)
	}

	_ = os.Setenv("PATH", guestPath)
	_ = os.Setenv("HOME", guestHome)

	env := guestEnv(cfg.Env)

	log.Debug("starting guest agent")
	agentCmd := exec.Command(guestAgentPath)
	agentCmd.Stdout = os.Stdout
	agentCmd.Stderr = os.Stderr
	agentCmd.Env = env
	if err := agentCmd.Start(); err != nil {
		log.Error("failed to start guest agent", "error", err)
	}

	workdir := cfg.Workdir
	if workdir == "" {
		workdir = "/"
	}

	// dicer-init starts again as the namespaces' first process, to mount their
	// /proc before it becomes the workload: Go cannot run code between
	// creating a process and running its program. /proc/self/exe is this
	// binary, which the chroot has otherwise left behind.
	argv := cfg.Argv()
	entrypointCmd := exec.Command("/proc/self/exe", append([]string{entrypointCommand, "--"}, argv...)...)
	entrypointCmd.Stdin = os.Stdin
	entrypointCmd.Stdout = os.Stdout
	entrypointCmd.Stderr = os.Stderr
	entrypointCmd.Env = env
	entrypointCmd.Dir = workdir
	entrypointCmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWPID | syscall.CLONE_NEWNS}

	log.Info("starting entrypoint", "argv", argv, "workdir", workdir)

	// Listened for before the start, so that a stop asked for as the
	// entrypoint starts is not lost.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, forwardedSignals...)

	exitCode := runWorkload(log, entrypointCmd, signals)

	// The workload is the reason the machine exists: once it has exited,
	// the machine ends, and the host decides what happens next. The guest
	// agent goes with it.
	log.Info("entrypoint exited", "code", exitCode)
	reportExit(log, cfg, exitCode)
	halt(log, cfg.Halt)
}

// runWorkload starts cmd, passes signals on to it and waits for it to exit,
// returning its exit code. A workload that cannot be started ends as one
// that exited would, with the status a shell gives: the instance then says
// why, rather than running with nothing in it.
func runWorkload(log *slog.Logger, cmd *exec.Cmd, signals <-chan os.Signal) int {
	if err := cmd.Start(); err != nil {
		log.Error("entrypoint start failed", "error", err)
		return exitCannotExecute
	}
	go forwardSignals(log, signals, cmd.Process)

	err := cmd.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return guest.ExitStatus(exitErr.ProcessState)
	default:
		log.Error("entrypoint wait failed", "error", err)
		return 0
	}
}

// forwardedSignals are the signals to dicer-init that ask the workload to
// stop.
var forwardedSignals = []os.Signal{
	syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, guest.ShutdownSignal,
}

// forwardSignals passes the signals dicer-init receives to the workload,
// translating the shutdown signal into SIGTERM.
func forwardSignals(log *slog.Logger, signals <-chan os.Signal, process *os.Process) {
	for sig := range signals {
		if sig == guest.ShutdownSignal {
			sig = syscall.SIGTERM
		}
		log.Info("passing a signal on to the workload", "signal", sig)
		if err := process.Signal(sig); err != nil {
			return
		}
	}
}
