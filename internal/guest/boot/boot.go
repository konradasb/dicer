// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"log/slog"
	"os"
	"os/exec"

	"github.com/dicer-sh/dicer"
)

// boot runs the full init sequence and never returns — it either execs the
// guest process or drops to a debug shell on an unrecoverable error.
func boot(log *slog.Logger, configFile string) {
	log.Info("dicer-init starting")

	if err := mountVirtualFilesystems(log); err != nil {
		fatal(log, "mount virtual filesystems", err)
	}
	if err := mountOverlayRootfs(log); err != nil {
		fatal(log, "mount overlay rootfs", err)
	}

	cfg, err := loadConfigDisk(log, configFile)
	if err != nil {
		fatal(log, "load config", err)
	}

	checkBoot(log, cfg)

	cfg.Mode = resolveMode(overlayRoot, cfg)
	log.Info("init mode", "mode", cfg.Mode, "argv", cfg.Argv())

	if err := configureNetwork(log, cfg); err != nil {
		log.Warn("failed to configure network", "err", err)
	}

	mountVolumes(log, cfg.VolumeMounts)

	if err := bindFilesystemsToNewRoot(log); err != nil {
		fatal(log, "bind filesystems to new root", err)
	}

	if err := installGuestAgent(log); err != nil {
		log.Warn("failed to install guest agent", "err", err)
	}

	if !cfg.SkipKernelHeaders {
		if err := extractKernelHeaders(log); err != nil {
			log.Warn("failed to setup kernel headers", "err", err)
		}
	}

	if cfg.Hostname != "" {
		if err := setHostname(cfg.Hostname); err != nil {
			log.Warn("failed to set hostname", "err", err)
		}
	}

	if err := mountFiles(cfg.Files, cfg.Mode); err != nil {
		log.Warn("failed to mount injected files", "err", err)
	}

	log.Info("boot complete", "mode", cfg.Mode)
	switch cfg.Mode {
	case dicer.ModeSystemd:
		bootSystemd(log, cfg)
	default:
		bootExec(log, cfg)
	}
}

// fatal logs the error and drops to an interactive debug shell. It never returns.
func fatal(log *slog.Logger, msg string, err error) {
	if err != nil {
		log.Error(msg, "err", err)
	} else {
		log.Error(msg)
	}
	dropToDebugShell()
}

// dropToDebugShell spawns an interactive /bin/sh reachable via the serial
// console. Called on unrecoverable boot errors; exits with code 1 when the
// shell is closed.
func dropToDebugShell() {
	sh := exec.Command("/bin/sh", "-i")
	sh.Stdin = os.Stdin
	sh.Stdout = os.Stdout
	sh.Stderr = os.Stderr
	_ = sh.Run()
	os.Exit(1)
}
