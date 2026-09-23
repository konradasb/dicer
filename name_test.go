// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	valid := []string{"web", "web-1", "Web2", "a", "vmlinux-6.1", "v1.2.3", strings.Repeat("a", 63)}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"", ".", "..", ".hidden", "trailing.", "a..b", "-web", "web-", "db_primary",
		"a/b", "../etc", "has space", strings.Repeat("a", 64) + ".x",
	}
	for _, name := range invalid {
		if err := ValidateName(name); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidArgument", name, err)
		}
	}
}
