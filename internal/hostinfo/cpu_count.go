// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package hostinfo reports the physical resources of the machine the daemon
// runs on, read from /proc. Reading fails on a system without a Linux /proc;
// parsing is portable, so it is tested everywhere.
package hostinfo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// CPUCount returns the number of logical processors (hardware threads)
// /proc/cpuinfo lists.
func CPUCount() (int, error) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return 0, fmt.Errorf("open /proc/cpuinfo: %w", err)
	}
	defer func() { _ = f.Close() }()

	return parseCPUCount(f)
}

// parseCPUCount counts the processor entries in cpuinfo-formatted input.
func parseCPUCount(r io.Reader) (int, error) {
	count := 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		key, _, ok := strings.Cut(scanner.Text(), ":")
		if ok && strings.TrimSpace(key) == "processor" {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read /proc/cpuinfo: %w", err)
	}
	if count == 0 {
		return 0, errors.New("no processors found in /proc/cpuinfo")
	}
	return count, nil
}
