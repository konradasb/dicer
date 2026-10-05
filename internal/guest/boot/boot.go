// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"log/slog"
	"os"
	"os/exec"

	"github.com/konradasb/dicer/internal/types"
)

// boot runs the init sequence. It does not return: it hands the machine to
// the workload or to systemd, or drops to a debug shell on an unrecoverable
// error.
func boot(log *slog.Logger, configFile string) {
	log.Info("dicer-init starting")

	if err := mountVirtualFilesystems(log); err != nil {
		fatal(log, "mount virtual filesystems", err)
	}
	if err := mountOverlayRootfs(log); err != nil {
		fatal(log, "mount overlay rootfs", err)
	}

	cfg, err := loadConfig(log, configFile)
	if err != nil {
		fatal(log, "load config", err)
	}

	checkBoot(log, cfg)

	cfg.Mode = resolveMode(overlayRoot, cfg)
	log.Info("init mode", "mode", cfg.Mode, "argv", cfg.Argv())

	if err := configureNetwork(log, cfg); err != nil {
		log.Warn("failed to configure network", "error", err)
	}

	if err := bindFilesystemsIntoOverlayRoot(log); err != nil {
		fatal(log, "bind filesystems into overlay root", err)
	}

	if err := installGuestAgent(log); err != nil {
		log.Warn("failed to install guest agent", "error", err)
	}

	if !cfg.SkipKernelHeaders {
		if err := extractKernelHeaders(log); err != nil {
			log.Warn("failed to set up kernel headers", "error", err)
		}
	}

	if cfg.Hostname != "" {
		if err := setHostname(cfg.Hostname); err != nil {
			log.Warn("failed to set hostname", "error", err)
		}
	}

	// Last, so what the instance mounts wins over what dicer-init wrote.
	mountAll(log, cfg.Mounts, cfg.Mode)

	log.Info("boot complete", "mode", cfg.Mode)
	switch cfg.Mode {
	case types.InitModeSystemd:
		bootSystemd(log, cfg)
	default:
		bootExec(log, cfg)
	}
}

// fatal logs err and drops to an interactive /bin/sh on the console, then
// exits with status 1 once the shell is closed. It does not return.
func fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "error", err)

	shell := exec.Command("/bin/sh", "-i")
	shell.Stdin = os.Stdin
	shell.Stdout = os.Stdout
	shell.Stderr = os.Stderr
	_ = shell.Run()
	os.Exit(1)
}
