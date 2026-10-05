// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cloudhypervisor

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/konradasb/dicer/internal/hypervisor"
)

// fakeVMM serves handler as a VMM's API on a Unix socket and returns a
// Hypervisor connected to it.
func fakeVMM(t *testing.T, handler http.HandlerFunc) *Hypervisor {
	t.Helper()

	// Not t.TempDir: its name carries the test's, and macOS caps a socket
	// path at 104 bytes.
	dir, err := os.MkdirTemp("", "vmm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "sock")

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(l) }()
	t.Cleanup(func() { _ = server.Close() })

	return NewHypervisor(path)
}

func TestVMInfoReportsStateOfGuest(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   hypervisor.VMState
	}{
		{"running", http.StatusOK, `{"state":"Running","config":{}}`, hypervisor.VMStateRunning},
		{"paused", http.StatusOK, `{"state":"Paused","config":{}}`, hypervisor.VMStatePaused},
		{"shut down", http.StatusOK, `{"state":"Shutdown","config":{}}`, hypervisor.VMStateStopped},
		{"no guest", http.StatusInternalServerError, "VM is not created", hypervisor.VMStateStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hv := fakeVMM(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			info, err := hv.VMInfo(t.Context())
			if err != nil {
				t.Fatalf("VMInfo: %v", err)
			}
			if info.State != tt.want {
				t.Errorf("State = %s, want %s", info.State, tt.want)
			}
		})
	}
}

// TestFailedRequestReportsVMMAnswer checks that a request the VMM refuses
// fails with the status and the VMM's explanation.
func TestFailedRequestReportsVMMAnswer(t *testing.T) {
	hv := fakeVMM(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "VM is not running", http.StatusMethodNotAllowed)
	})

	err := hv.PauseVM(t.Context())
	if err == nil || !strings.Contains(err.Error(), "status 405: VM is not running") {
		t.Errorf("PauseVM = %v, want the VMM's refusal", err)
	}
}

// TestResizeVMMemoryRefusesBelowBootMemory checks that memory below what the
// guest booted with is refused, rather than sent to Cloud Hypervisor, which
// would ignore it and report success.
func TestResizeVMMemoryRefusesBelowBootMemory(t *testing.T) {
	const mib = 1 << 20
	tests := []struct {
		name       string
		bytes      int64
		wantResize bool
	}{
		{"within the region", 768 * mib, true},
		{"below the boot memory", 256 * mib, false},
		{"beyond the region", 2048 * mib, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resized atomic.Bool
			hv := fakeVMM(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/vm.resize" {
					resized.Store(true)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"state":"Running","config":{"memory":{"size":%d,"hotplug_size":%d}}}`, 512*mib, 512*mib)
			})

			err := hv.ResizeVMMemory(t.Context(), tt.bytes)
			if (err == nil) != tt.wantResize || resized.Load() != tt.wantResize {
				t.Errorf("ResizeVMMemory = %v, resized = %t; want resized = %t", err, resized.Load(), tt.wantResize)
			}
		})
	}
}
