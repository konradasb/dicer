// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import "unicode/utf8"

// MaxProbeOutput bounds the health probe output the agent reports and the
// host keeps.
const MaxProbeOutput = 4096

// TruncateOutput cuts s to at most MaxProbeOutput bytes, on a rune boundary.
func TruncateOutput(s string) string {
	n := MaxProbeOutput
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
