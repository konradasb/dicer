// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/process"
)

func TestClassifyExit(t *testing.T) {
	zero, three := 0, 3
	crashed := &exec.ExitError{}

	tests := []struct {
		name    string
		status  guest.Status
		vmmErr  error
		clean   bool
		code    *int
		failure string
	}{
		{name: "exit 0", status: guest.Status{Boots: 1, ExitCode: &zero}, clean: true, code: &zero},
		{name: "exit 3", status: guest.Status{Boots: 1, ExitCode: &three}, code: &three, failure: "exit code 3"},
		{
			name:   "a reported code wins over the VMM",
			status: guest.Status{Boots: 1, ExitCode: &zero}, vmmErr: crashed,
			clean: true, code: &zero,
		},
		{name: "second boot", status: guest.Status{Boots: 2}, failure: "guest reset"},
		{name: "VMM crashed", status: guest.Status{Boots: 1}, vmmErr: crashed, failure: "hypervisor exited unexpectedly"},
		// A VMM that ends on a reset exits cleanly whether or not the
		// guest meant it, so a clean VMM exit alone says nothing.
		{name: "silent end, clean VMM", status: guest.Status{Boots: 1}, failure: "guest ended, no exit code reported"},
		{
			name:   "silent end, adopted VMM",
			status: guest.Status{Boots: 1}, vmmErr: process.ErrExitStatusUnknown,
			failure: "hypervisor exited, no exit code reported",
		},
		{name: "never booted", vmmErr: errors.New("signal: killed"), failure: "hypervisor exited unexpectedly"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyExit(tt.status, tt.vmmErr)

			if got.Clean() != tt.clean {
				t.Errorf("Clean() = %v, want %v (failure: %v)", got.Clean(), tt.clean, got.Failure)
			}
			if (got.Code == nil) != (tt.code == nil) || (got.Code != nil && *got.Code != *tt.code) {
				t.Errorf("Code = %v, want %v", got.Code, tt.code)
			}
			if tt.failure != "" && (got.Failure == nil || !strings.Contains(got.Failure.Error(), tt.failure)) {
				t.Errorf("Failure = %v, want it to mention %q", got.Failure, tt.failure)
			}
		})
	}
}
