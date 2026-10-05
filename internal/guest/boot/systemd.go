// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"syscall"

	"github.com/konradasb/dicer/internal/guest"
)

// bootSystemd injects the guest agent's unit and the exit unit, then
// chroots into the overlay root and execs systemd as PID 1. It does not
// return.
func bootSystemd(log *slog.Logger, cfg *guest.Config) {
	if err := injectGuestAgentUnit(cfg.Env); err != nil {
		log.Warn("guest agent unit injection failed", "error", err)
	} else {
		log.Debug("guest agent unit injected")
	}
	if err := injectExitUnit(cfg.StatusDevice); err != nil {
		log.Warn("exit unit injection failed", "error", err)
	}

	if err := syscall.Chroot(overlayRoot); err != nil {
		fatal(log, "chroot", err)
	}
	if err := os.Chdir("/"); err != nil {
		fatal(log, "chdir /", err)
	}

	argv := cfg.Argv()
	log.Info("exec systemd", "argv", argv)

	if err := syscall.Exec(argv[0], argv, guestEnv(cfg.Env)); err != nil {
		fatal(log, "exec systemd", err)
	}
}

// exitUnitTemplate reports a clean end on the status disk as systemd powers
// the guest off. A reboot or a halt goes unreported, and so reads to the
// host as an end that was not clean: the guest did not ask to stay down.
const exitUnitTemplate = `[Unit]
Description=Report the guest's power-off to the Dicer host
DefaultDependencies=no
Before=shutdown.target

[Service]
Type=oneshot
ExecStart=` + guestAgentPath + ` report-exit --device %s --code 0

[Install]
WantedBy=poweroff.target
`

// injectExitUnit installs and enables the unit that reports a power-off on
// the status disk at device. A guest with no status disk gets none.
func injectExitUnit(device string) error {
	if device == "" {
		return nil
	}

	const (
		unitDir  = overlayRoot + "/etc/systemd/system"
		wantsDir = unitDir + "/poweroff.target.wants"
	)

	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", wantsDir, err)
	}

	unit := fmt.Sprintf(exitUnitTemplate, device)
	if err := os.WriteFile(unitDir+"/dicer-exit.service", []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	link := wantsDir + "/dicer-exit.service"
	if err := os.Symlink("../dicer-exit.service", link); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("enable unit: %w", err)
	}

	return nil
}
