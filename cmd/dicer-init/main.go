// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Command dicer-init runs as PID 1 inside a Dicer guest. The implementation
// lives in internal/guest/boot.
package main

import (
	"fmt"
	"os"

	"github.com/konradasb/dicer/internal/guest/boot"
)

func main() {
	if err := boot.NewCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
