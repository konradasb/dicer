// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package naming

import (
	"errors"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

func TestValidate(t *testing.T) {
	valid := []string{"web", "web-1", "Web2", "a", "vmlinux-6.1", "v1.2.3", strings.Repeat("a", 63)}
	for _, name := range valid {
		if err := Validate(name); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"", ".", "..", ".hidden", "trailing.", "a..b", "-web", "web-", "db_primary",
		"a/b", "../etc", "has space", strings.Repeat("a", 64) + ".x",
	}
	for _, name := range invalid {
		if err := Validate(name); !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("Validate(%q) = %v, want errdefs.ErrInvalidArgument", name, err)
		}
	}
}
