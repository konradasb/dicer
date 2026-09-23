// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package version is the identity of the binary it is linked into, set at
// link time with -X.
package version

import "fmt"

var (
	// Version is the released version of Dicer.
	Version = "0.0.0"

	// BuildDate is when the binary was built.
	BuildDate = "1970-01-01T00:00:00Z"

	// Commit is the Git SHA the binary was built from.
	Commit = ""
)

// String describes this build for a person: "0.5.0 (built
// 2026-09-22T10:00:00Z from commit 1a2b3c4)".
func String() string {
	return fmt.Sprintf("%s (built %s from commit %s)", Version, BuildDate, Commit)
}
