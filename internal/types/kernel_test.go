// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package types

import (
	"errors"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/errdefs"
)

// TestKernelValidate checks that a kernel the daemon could never boot is
// refused, and one it can is not.
func TestKernelValidate(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	tests := []struct {
		name   string
		modify func(*Kernel)
		valid  bool
	}{
		{name: "without a checksum", modify: func(*Kernel) {}, valid: true},
		{name: "with a checksum", modify: func(k *Kernel) { k.SHA256 = digest }, valid: true},
		{name: "aarch64", modify: func(k *Kernel) { k.Architecture = ArchitectureAArch64 }, valid: true},
		{name: "invalid name", modify: func(k *Kernel) { k.Name = "vmlinux_6" }},
		{name: "no architecture", modify: func(k *Kernel) { k.Architecture = "" }},
		{name: "unknown architecture", modify: func(k *Kernel) { k.Architecture = "riscv64" }},
		{name: "checksum not hex", modify: func(k *Kernel) { k.SHA256 = strings.Repeat("zz", 32) }},
		{name: "checksum too short", modify: func(k *Kernel) { k.SHA256 = "abcd" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := Kernel{Name: "vmlinux-6.1", Architecture: ArchitectureX86_64}
			tt.modify(&k)

			err := k.Validate()
			switch {
			case tt.valid && err != nil:
				t.Errorf("Validate = %v, want nil", err)
			case !tt.valid && !errors.Is(err, errdefs.ErrInvalidArgument):
				t.Errorf("Validate = %v, want an invalid argument", err)
			}
		})
	}
}
