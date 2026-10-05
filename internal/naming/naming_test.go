// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package naming

import (
	"errors"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

// TestValidateAcceptsOnlySubdomainNames checks names are RFC 1123
// subdomains, and so cannot climb out of the directory they are a path in.
func TestValidateAcceptsOnlySubdomainNames(t *testing.T) {
	valid := []string{"web", "web-1", "Web2", "a", "vmlinux-6.1", "v1.2.3", strings.Repeat("a", 63)}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			if err := Validate(name); err != nil {
				t.Errorf("Validate(%q) = %v, want nil", name, err)
			}
		})
	}

	invalid := []string{
		"", ".", "..", ".hidden", "trailing.", "a..b", "-web", "web-", "db_primary",
		"a/b", "../etc", "has space", strings.Repeat("a", 64) + ".x",
		strings.Repeat("a.", 127) + "a",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := Validate(name); !errors.Is(err, errdefs.ErrInvalidArgument) {
				t.Errorf("Validate(%q) = %v, want errdefs.ErrInvalidArgument", name, err)
			}
		})
	}
}

// TestValidateHostnameAllowsNoneOrAnRFC1123Name checks that an empty
// hostname, which leaves the guest its default, is accepted, as is any name
// the rule allows, and nothing else.
func TestValidateHostnameAllowsNoneOrAnRFC1123Name(t *testing.T) {
	for _, hostname := range []string{"", "web", "web.example.com"} {
		if err := ValidateHostname(hostname); err != nil {
			t.Errorf("ValidateHostname(%q) = %v, want nil", hostname, err)
		}
	}
	for _, hostname := range []string{"web_1", "-web", "has space", strings.Repeat("a", 254)} {
		if err := ValidateHostname(hostname); !errors.Is(err, errdefs.ErrInvalidArgument) {
			t.Errorf("ValidateHostname(%q) = %v, want errdefs.ErrInvalidArgument", hostname, err)
		}
	}
}
