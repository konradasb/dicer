// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"log/slog"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/konradasb/dicer/internal/guest"
)

// checkBoot records this boot on the status disk and ends the machine if it
// is not the first, since that means the guest reset. Without a status disk
// it does nothing.
func checkBoot(log *slog.Logger, cfg *guest.Config) {
	if cfg.StatusDevice == "" {
		return
	}
	if err := waitForDevice(cfg.StatusDevice); err != nil {
		log.Warn("status disk not found, the host will not learn how the guest ends", "err", err)
		return
	}

	st, err := guest.ReadStatus(cfg.StatusDevice)
	if err != nil {
		log.Warn("cannot read status disk", "err", err)
		return
	}

	st.Boots++
	if err := guest.WriteStatus(cfg.StatusDevice, st); err != nil {
		log.Warn("cannot write status disk", "err", err)
	}

	if st.Boots > 1 {
		log.Error("the guest reset without reporting its end; halting")
		halt(log, cfg.Halt)
	}
}

// reportExit records the workload's exit code on the status disk, for the
// host to read once the machine has ended.
func reportExit(log *slog.Logger, cfg *guest.Config, code int) {
	if cfg.StatusDevice == "" {
		return
	}
	if err := guest.WriteStatus(cfg.StatusDevice, guest.Status{Boots: 1, ExitCode: &code}); err != nil {
		log.Error("cannot report the exit code to the host", "err", err)
	}
}

// halt flushes the guest's disks and ends the machine the way cfg says ends
// its VMM. It does not return.
func halt(log *slog.Logger, how guest.Halt) {
	unix.Sync()

	cmd := unix.LINUX_REBOOT_CMD_POWER_OFF
	if how == guest.HaltReset {
		cmd = unix.LINUX_REBOOT_CMD_RESTART
	}
	err := unix.Reboot(cmd)

	// Reboot returns only if it failed. Exiting PID 1 panics the kernel,
	// which the boot arguments turn into a reset: the machine still ends.
	log.Error("halt failed", "halt", how, "err", err)
	syscall.Exit(1)
}
