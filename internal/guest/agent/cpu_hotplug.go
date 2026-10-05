// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/sys/unix"
)

// cpuDir is where the kernel lists the guest's CPUs.
const cpuDir = "/sys/devices/system/cpu"

// cpuAddedEvent matches the uevent the kernel sends when the hypervisor adds
// a vCPU to the running guest: "add@/devices/system/cpu/cpu3".
var cpuAddedEvent = regexp.MustCompile(`^add@/devices/system/cpu/cpu[0-9]+$`)

// onlineHotpluggedCPUs brings online the vCPUs the hypervisor adds to the
// running guest, which the kernel leaves offline for userspace to online, as
// udev would on a distribution. It returns only if it cannot listen for
// them.
func onlineHotpluggedCPUs() error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, unix.NETLINK_KOBJECT_UEVENT)
	if err != nil {
		return fmt.Errorf("open uevent socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	// Group 1 is the kernel's own uevents.
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: 1}); err != nil {
		return fmt.Errorf("bind uevent socket: %w", err)
	}

	// After listening, so that a vCPU added before the agent started, or in
	// between, is not missed.
	onlineCPUs()

	buf := make([]byte, 64<<10)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		switch {
		case errors.Is(err, unix.EINTR), errors.Is(err, unix.ENOBUFS):
			// ENOBUFS is uevents dropped while the agent was behind: one of
			// them may have been a vCPU's.
			onlineCPUs()
			continue
		case err != nil:
			return fmt.Errorf("read uevent: %w", err)
		}
		if cpuAdded(buf[:n]) {
			onlineCPUs()
		}
	}
}

// cpuAdded reports whether a uevent says a CPU was added. Its first field is
// ACTION@DEVPATH.
func cpuAdded(event []byte) bool {
	header, _, _ := bytes.Cut(event, []byte{0})
	return cpuAddedEvent.Match(header)
}

// onlineCPUs brings every offline CPU online. One that cannot be is logged
// and left.
func onlineCPUs() {
	paths, _ := filepath.Glob(filepath.Join(cpuDir, "cpu[0-9]*", "online"))
	for _, path := range paths {
		state, err := os.ReadFile(path)
		if err != nil || string(bytes.TrimSpace(state)) != "0" {
			continue
		}
		if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
			slog.Warn("cannot bring a hot-added vCPU online", "path", path, "error", err)
			continue
		}
		slog.Info("brought a hot-added vCPU online", "cpu", filepath.Base(filepath.Dir(path)))
	}
}
