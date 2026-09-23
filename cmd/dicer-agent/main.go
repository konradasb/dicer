// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

// Command dicer-agent serves exec requests inside a Dicer guest. The implementation
// lives in internal/guest/agent.
package main

import (
	"fmt"
	"os"

	"github.com/konradasb/dicer/internal/guest/agent"
)

func main() {
	if err := agent.NewCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
