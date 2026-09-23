// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Command dicerd is the Dicer host daemon. The implementation
// lives in internal/daemon.
package main

import (
	"fmt"
	"os"

	"github.com/dicer-sh/dicer/internal/daemon"
)

func main() {
	if err := daemon.NewCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
