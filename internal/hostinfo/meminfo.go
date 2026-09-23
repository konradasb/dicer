// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package hostinfo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// MemoryTotal returns the host's usable RAM in bytes: MemTotal in
// /proc/meminfo.
func MemoryTotal() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("open /proc/meminfo: %w", err)
	}
	defer func() { _ = f.Close() }()

	return parseMemTotal(f)
}

// parseMemTotal returns MemTotal, in bytes, from meminfo-formatted input.
func parseMemTotal(r io.Reader) (int64, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok || strings.TrimSpace(key) != "MemTotal" {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemTotal %q: %w", rest, err)
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read /proc/meminfo: %w", err)
	}
	return 0, errors.New("MemTotal not found in /proc/meminfo")
}
