// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import "fmt"

// The build identity, stamped in at link time with -X. The defaults are what
// a build from source without them reports.
//
// It lives here because it is the version of Dicer as a whole: the daemon
// reports it as its own, the CLI prints it, and a program built on the client
// can say which Dicer it was built against.
var (
	// Version is the released version of Dicer.
	Version = "0.0.0"

	// BuildDate is when the binary was built.
	BuildDate = "1970-01-01T00:00:00Z"

	// Commit is the Git SHA the binary was built from.
	Commit = ""
)

// VersionString describes this build for a person: "0.5.0 (built
// 2026-09-22T10:00:00Z from commit 1a2b3c4)".
func VersionString() string {
	return fmt.Sprintf("%s (built %s from commit %s)", Version, BuildDate, Commit)
}
