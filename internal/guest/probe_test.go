// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateProbeOutputKeepsRunes(t *testing.T) {
	out := TruncateProbeOutput(strings.Repeat("é", MaxProbeOutput))
	if len(out) > MaxProbeOutput || !utf8.ValidString(out) {
		t.Errorf("TruncateProbeOutput = %d bytes, valid UTF-8 %v", len(out), utf8.ValidString(out))
	}
}
