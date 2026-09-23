// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package firecracker

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/hypervisor"
)

// shortTempDir returns a temporary directory whose name is short enough to
// hold a Unix socket: the path of one is limited to about 100 bytes, and
// t.TempDir() spends most of that on the test's name.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "fc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

// request is one call the fake Firecracker recorded.
type request struct {
	method string
	path   string
	body   string
}

// fakeFirecracker serves the Firecracker API on a Unix socket, as the real
// VMM does, and records what it is asked. handler answers the requests a
// test cares about; anything else gets 204, as Firecracker does for a
// successful configuration call.
type fakeFirecracker struct {
	mu       sync.Mutex
	requests []request
}

func newFakeFirecracker(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body string)) (*Hypervisor, *fakeFirecracker) {
	t.Helper()

	fake := &fakeFirecracker{}
	socketPath := filepath.Join(shortTempDir(t), "api.sock")

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			body := string(data)

			fake.mu.Lock()
			fake.requests = append(fake.requests, request{method: r.Method, path: r.URL.Path, body: body})
			fake.mu.Unlock()

			if handler != nil {
				handler(w, r, body)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return NewHypervisor(socketPath), fake
}

func (f *fakeFirecracker) recorded() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

// find returns the body of the first request to method and path.
func (f *fakeFirecracker) find(t *testing.T, method, path string) string {
	t.Helper()

	for _, r := range f.recorded() {
		if r.method == method && r.path == path {
			return r.body
		}
	}
	t.Fatalf("no %s %s in %+v", method, path, f.recorded())
	return ""
}

// TestSetupApplyOrder pins the configuration sequence: Firecracker only
// accepts these before boot, and the guest's devices come out in this order.
func TestSetupApplyOrder(t *testing.T) {
	hv, fake := newFakeFirecracker(t, nil)

	setup, err := newSetup(testSpec())
	if err != nil {
		t.Fatalf("newSetup: %v", err)
	}
	if err := setup.apply(t.Context(), hv.client); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var got []string
	for _, r := range fake.recorded() {
		if r.method != http.MethodPut {
			t.Errorf("%s %s: configuration must use PUT", r.method, r.path)
		}
		got = append(got, r.path)
	}

	want := []string{
		"/boot-source", "/machine-config",
		"/drives/disk0", "/drives/disk1",
		"/network-interfaces/eth0",
		"/vsock", "/serial",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("configured %v, want %v", got, want)
	}

	var boot bootSource
	if err := json.Unmarshal([]byte(fake.find(t, http.MethodPut, "/boot-source")), &boot); err != nil {
		t.Fatal(err)
	}
	if boot.KernelImagePath == "" || boot.InitrdPath == "" {
		t.Errorf("boot source = %+v, want a kernel and an initrd", boot)
	}
}

func TestGetVMInfo(t *testing.T) {
	states := map[string]hypervisor.VirtualMachineState{
		instanceNotStarted: hypervisor.VirtualMachineStateStopped,
		instanceRunning:    hypervisor.VirtualMachineStateRunning,
		instancePaused:     hypervisor.VirtualMachineStatePaused,
	}

	for reported, want := range states {
		t.Run(reported, func(t *testing.T) {
			hv, _ := newFakeFirecracker(t, func(w http.ResponseWriter, r *http.Request, _ string) {
				switch r.URL.Path {
				case "/":
					_ = json.NewEncoder(w).Encode(instanceInfo{State: reported, VMMVersion: "1.17.0"})
				case "/vm/config":
					_ = json.NewEncoder(w).Encode(vmConfig{MachineConfig: machineConfig{MemSizeMiB: 512}})
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			})

			info, err := hv.GetVMInfo(t.Context())
			if err != nil {
				t.Fatalf("GetVMInfo: %v", err)
			}
			if info.State != want {
				t.Errorf("state = %q, want %q", info.State, want)
			}
			if info.MemoryBytes == nil || *info.MemoryBytes != 512*mib {
				t.Errorf("memory = %v, want 512 MiB", info.MemoryBytes)
			}
		})
	}
}

func TestGetVMInfoCountsHotpluggedMemory(t *testing.T) {
	hv, _ := newFakeFirecracker(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.URL.Path {
		case "/":
			_ = json.NewEncoder(w).Encode(instanceInfo{State: instanceRunning})
		case "/vm/config":
			_ = json.NewEncoder(w).Encode(vmConfig{
				MachineConfig: machineConfig{MemSizeMiB: 512},
				MemoryHotplug: &memoryHotplugConfig{TotalSizeMiB: 512},
			})
		case "/hotplug/memory":
			_ = json.NewEncoder(w).Encode(memoryHotplugStatus{TotalSizeMiB: 512, PluggedSizeMiB: 128})
		}
	})

	info, err := hv.GetVMInfo(t.Context())
	if err != nil {
		t.Fatalf("GetVMInfo: %v", err)
	}
	if info.MemoryBytes == nil || *info.MemoryBytes != 640*mib {
		t.Errorf("memory = %v, want 640 MiB (512 booted plus 128 plugged)", info.MemoryBytes)
	}
}

func TestPauseAndResume(t *testing.T) {
	hv, fake := newFakeFirecracker(t, nil)

	if err := hv.PauseVM(t.Context()); err != nil {
		t.Fatalf("PauseVM: %v", err)
	}
	if err := hv.ResumeVM(t.Context()); err != nil {
		t.Fatalf("ResumeVM: %v", err)
	}

	got := fake.recorded()
	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2", len(got))
	}
	for i, want := range []string{vmPaused, vmResumed} {
		if got[i].method != http.MethodPatch || got[i].path != "/vm" {
			t.Errorf("request %d = %s %s, want PATCH /vm", i, got[i].method, got[i].path)
		}
		var state vmState
		if err := json.Unmarshal([]byte(got[i].body), &state); err != nil || state.State != want {
			t.Errorf("request %d body = %s, want state %s", i, got[i].body, want)
		}
	}
}

func TestSnapshotVM(t *testing.T) {
	hv, fake := newFakeFirecracker(t, nil)
	dir := filepath.Join(t.TempDir(), "snapshot")

	if err := hv.SnapshotVM(t.Context(), dir); err != nil {
		t.Fatalf("SnapshotVM: %v", err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("snapshot directory not created: %v", err)
	}

	var create snapshotCreate
	if err := json.Unmarshal([]byte(fake.find(t, http.MethodPut, "/snapshot/create")), &create); err != nil {
		t.Fatal(err)
	}
	if create.SnapshotType != "Full" ||
		create.SnapshotPath != filepath.Join(dir, snapshotStateFile) ||
		create.MemFilePath != filepath.Join(dir, snapshotMemoryFile) {
		t.Errorf("snapshot request = %+v", create)
	}
}

func TestResizeVMMemory(t *testing.T) {
	hv, fake := newFakeFirecracker(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.URL.Path == "/vm/config" {
			_ = json.NewEncoder(w).Encode(vmConfig{
				MachineConfig: machineConfig{MemSizeMiB: 512},
				MemoryHotplug: &memoryHotplugConfig{TotalSizeMiB: 512},
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := hv.ResizeVMMemory(t.Context(), 768*mib); err != nil {
		t.Fatalf("ResizeVMMemory: %v", err)
	}

	var update memoryHotplugUpdate
	if err := json.Unmarshal([]byte(fake.find(t, http.MethodPatch, "/hotplug/memory")), &update); err != nil {
		t.Fatal(err)
	}
	// 768 MiB total is the 512 MiB it booted with plus 256 plugged.
	if update.RequestedSizeMiB != 256 {
		t.Errorf("requested_size_mib = %d, want 256", update.RequestedSizeMiB)
	}

	// Beyond the hotpluggable region, and below the boot memory, there is
	// nothing virtio-mem can do.
	if err := hv.ResizeVMMemory(t.Context(), 4096*mib); err == nil {
		t.Error("resizing beyond the hotpluggable region succeeded")
	}
	if err := hv.ResizeVMMemory(t.Context(), 256*mib); err == nil {
		t.Error("resizing below the boot memory succeeded")
	}
}

func TestResizeVMMemoryWithoutHotplug(t *testing.T) {
	hv, _ := newFakeFirecracker(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.URL.Path == "/vm/config" {
			_ = json.NewEncoder(w).Encode(vmConfig{MachineConfig: machineConfig{MemSizeMiB: 512}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	err := hv.ResizeVMMemory(t.Context(), 1024*mib)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ResizeVMMemory = %v, want ErrUnsupported", err)
	}
}

func TestUnsupportedOperations(t *testing.T) {
	hv, _ := newFakeFirecracker(t, nil)

	if err := hv.DestroyVM(t.Context()); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("DestroyVM = %v, want ErrUnsupported", err)
	}
	if err := hv.ResizeVMCPU(t.Context(), 4); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ResizeVMCPU = %v, want ErrUnsupported", err)
	}
}

// TestAPIErrorCarriesFaultMessage checks that Firecracker's own explanation
// of a refusal reaches the caller, since that is all there is to go on.
func TestAPIErrorCarriesFaultMessage(t *testing.T) {
	hv, _ := newFakeFirecracker(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(apiError{FaultMessage: "cannot pause a VM that is not running"})
	})

	err := hv.PauseVM(t.Context())
	if err == nil || !strings.Contains(err.Error(), "cannot pause a VM that is not running") {
		t.Errorf("PauseVM = %v, want Firecracker's fault message", err)
	}
}

func TestCapabilities(t *testing.T) {
	caps := NewHypervisor("/nonexistent.sock").Capabilities()

	if !caps.SupportsPause || !caps.SupportsVsock || !caps.SupportsSnapshot ||
		!caps.SupportsHotplugMemory || !caps.SupportsDiskIOLimit {
		t.Errorf("capabilities = %+v, want the operations Firecracker supports", caps)
	}
	if caps.SupportsHotplugCPU || caps.SupportsCPUAffinity || caps.SupportsGPUPassthrough {
		t.Errorf("capabilities = %+v, claims what Firecracker cannot do", caps)
	}
}
