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

	"github.com/dicer-sh/dicer/internal/guest"
)

// bootExec chroots into the overlay rootfs and runs the guest entrypoint.
// The dicer-agent is started as a background sidecar before the entrypoint.
// When the entrypoint exits, its exit code is reported on the status disk and
// the machine ends. This function does not return.
//
// The entrypoint runs as PID 1 of its own PID namespace, as it would in a
// container: an init that insists on being PID 1 -- s6-overlay, tini -- is,
// and it reaps what is orphaned in there. dicer-init stays the machine's PID
// 1, outside it, where the agent runs too: an init's shutdown that signals
// every process it can see does not take the agent with it.
func bootExec(log *slog.Logger, cfg *guest.Config) {
	if err := syscall.Chroot(overlayRoot); err != nil {
		fatal(log, "chroot failed", err)
	}
	if err := os.Chdir("/"); err != nil {
		fatal(log, "chdir / failed", err)
	}

	_ = os.Setenv("PATH", guestPath)
	_ = os.Setenv("HOME", "/root")

	env := guestEnv(cfg.Env)

	// Start the guest agent as a background sidecar.
	log.Debug("starting dicer-agent")
	agentCmd := exec.Command("/usr/local/bin/dicer-agent")
	agentCmd.Stdout = os.Stdout
	agentCmd.Stderr = os.Stderr
	agentCmd.Env = env
	if err := agentCmd.Start(); err != nil {
		log.Error("failed to start dicer-agent", "err", err)
	}

	workdir := cfg.Workdir
	if workdir == "" {
		workdir = "/"
	}

	argv := cfg.Argv()
	appCmd := exec.Command(argv[0], argv[1:]...)
	appCmd.Stdin = os.Stdin
	appCmd.Stdout = os.Stdout
	appCmd.Stderr = os.Stderr
	appCmd.Env = env
	appCmd.Dir = workdir
	appCmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWPID}

	log.Info("starting entrypoint", "argv", argv, "workdir", workdir)

	// Listened for before the start, so that a stop asked for as the
	// entrypoint starts is not lost.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, forwardedSignals...)

	if err := appCmd.Start(); err != nil {
		fatal(log, "entrypoint start failed", err)
	}
	go forwardSignals(log, signals, appCmd.Process)

	exitCode := 0
	if err := appCmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = guest.ExitStatus(ee.ProcessState)
		} else {
			log.Error("entrypoint wait failed", "err", err)
		}
	}

	// The workload is the reason the machine exists: once it has exited,
	// the machine ends, and the host decides what happens next. The agent
	// goes with it.
	log.Info("entrypoint exited", "code", exitCode)
	reportExit(log, cfg, exitCode)
	halt(log, cfg.Halt)
}

// forwardedSignals are the signals to dicer-init that ask the workload to
// stop.
var forwardedSignals = []os.Signal{
	syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, guest.ShutdownSignal,
}

// forwardSignals passes the signals dicer-init receives on to the workload,
// as a container runtime does: a signal to the machine's PID 1 is meant for
// what it runs. The agent's request for an orderly shutdown is passed on as
// SIGTERM, which is what a workload stops on. A workload that ignores it is
// ended with the machine when the host runs out of patience.
func forwardSignals(log *slog.Logger, signals <-chan os.Signal, p *os.Process) {
	for sig := range signals {
		if sig == guest.ShutdownSignal {
			sig = syscall.SIGTERM
		}
		log.Info("passing a signal on to the workload", "signal", sig)
		if err := p.Signal(sig); err != nil {
			return
		}
	}
}
