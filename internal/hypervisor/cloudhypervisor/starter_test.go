// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"runtime"
	"testing"
)

// TestDefaultKernelArgsNameTheSerialPort checks that the console is the
// serial port's name on each architecture: a guest whose console does not
// exist never boots.
func TestDefaultKernelArgsNameTheSerialPort(t *testing.T) {
	for goarch, want := range map[string]string{
		"amd64": "console=ttyS0 reboot=k panic=1",
		"arm64": "console=ttyAMA0 reboot=k panic=1",
	} {
		if got := defaultKernelArgs(goarch); got != want {
			t.Errorf("defaultKernelArgs(%q) = %q, want %q", goarch, got, want)
		}
	}

	if got, want := (&Starter{}).DefaultKernelArgs(), defaultKernelArgs(runtime.GOARCH); got != want {
		t.Errorf("DefaultKernelArgs() = %q, want the host's, %q", got, want)
	}
}
