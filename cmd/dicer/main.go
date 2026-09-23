// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Command dicer is the command-line client for dicerd. The implementation
// lives in internal/cli.
package main

import (
	"os"

	"github.com/dicer-sh/dicer/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
