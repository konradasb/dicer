// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// testHypervisorVersion is the version the fake hypervisor reports.
const testHypervisorVersion = "v49.0.0"

// newTestManager returns a Manager over in-memory definitions and a fake host
// network, with nothing wired up to start a VMM; see newHarness for that.
func newTestManager(t *testing.T) (*Manager, *fakeDefinitions, *fakeHostNetwork) {
	t.Helper()

	dir := t.TempDir()
	definitions := newFakeDefinitions(dir)
	hostNetwork := &fakeHostNetwork{}

	mgr := NewManager(Config{
		Definitions: definitions,
		Addresses:   newFakeAddresses(),
		RunDir:      filepath.Join(dir, "run"),
		HostNetwork: hostNetwork,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(mgr.Close)

	// Nothing is adoptable until a test says so; see startAdoptable.
	mgr.attach = func(pid int, _ string) (*process.Process, error) {
		return nil, fmt.Errorf("process %d: %w", pid, errNotRunning)
	}

	return mgr, definitions, hostNetwork
}

// errNotRunning is what the test Manager's attach reports for every PID.
var errNotRunning = errors.New("not running")

// seedInstance defines an instance on the default network, defining that too
// if need be.
func seedInstance(t *testing.T, definitions *fakeDefinitions, name string) types.InstanceSpec {
	t.Helper()

	if _, ok := definitions.networks["default"]; !ok {
		definitions.networks["default"] = types.Network{
			ID: "net-default", Name: "default",
			Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Bridge: "dicer-default",
		}
	}

	inst := types.InstanceSpec{
		ID: "id-" + name, Name: name,
		ImageRef: "alpine:latest", KernelName: "k", NetworkName: "default", VCPUs: 1,
	}
	definitions.instances[name] = inst

	return inst
}

// harness is an instance that can be started, stopped and snapshotted: a
// manager wired to a fake hypervisor whose VMMs are real processes, and an
// overlay disk with something in it.
type harness struct {
	mgr         *Manager
	events      *fakeEvents
	definitions *fakeDefinitions
	hostNetwork *fakeHostNetwork
	inst        types.InstanceSpec
	starter     *fakeStarter
	hv          *fakeHypervisor
	overlay     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	mgr, definitions, hostNetwork := newTestManager(t)
	inst := seedInstance(t, definitions, "web")
	definitions.kernels["k"] = types.Kernel{Name: "k"}

	hv := newFakeHypervisor()
	starter := &fakeStarter{version: testHypervisorVersion, hv: hv}
	// A VMM asked to shut down exits, as a real one does.
	hv.onShutdown = func() { _ = starter.vmm().Kill() }
	mgr.starters = map[types.HypervisorType][]hypervisor.Starter{
		types.HypervisorCloudHypervisor: {starter},
	}
	// Stop watching before the VMMs are killed, so that killing them at the
	// end of the test is not mistaken for a crash.
	t.Cleanup(func() {
		mgr.Close()
		starter.terminateAll()
	})

	mgr.images = &fakeImages{diskPath: filepath.Join(t.TempDir(), "disk.img")}
	mgr.kernels = fakeKernels{path: "/boot/vmlinux"}
	mgr.initrds = fakeInitrds{path: "/boot/initrd"}
	// There is no guest agent to ask for a graceful stop: tests that want
	// one say how the guest answers.
	mgr.shutdownGuest = func(context.Context, string) error { return errors.New("no guest agent") }
	// Building a real config disk needs mke2fs and root.
	mgr.provisionConfigDisk = func(_ context.Context, path string, _ *guest.Config) error {
		return os.WriteFile(path, []byte("config disk"), 0o600)
	}

	overlay := mgr.overlayDiskPath(inst)
	if err := os.MkdirAll(filepath.Dir(overlay), 0o750); err != nil {
		t.Fatal(err)
	}
	// A hole, then data: a disk copy has to preserve both. Its existence
	// also spares Start formatting a new one.
	if err := os.WriteFile(overlay, append(make([]byte, 2*copyChunkSize), []byte("guest data")...), 0o600); err != nil {
		t.Fatal(err)
	}

	recorded := &fakeEvents{}
	mgr.events = recorded

	return &harness{mgr: mgr, events: recorded, definitions: definitions, hostNetwork: hostNetwork, inst: inst, starter: starter, hv: hv, overlay: overlay}
}

// running records the instance as running without starting anything, for
// tests that only need the recorded state.
func (h *harness) running(t *testing.T) {
	t.Helper()

	pid := os.Getpid()
	err := h.mgr.writeRuntime(types.InstanceStatus{
		InstanceID:        h.inst.ID,
		State:             types.StateRunning,
		HypervisorPID:     &pid,
		HypervisorVersion: testHypervisorVersion,
		VCPUs:             h.inst.VCPUs,
		MemoryBytes:       h.inst.MemoryBytes,
		ImageDigest:       "sha256:aaaa",
	})
	if err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}
}

// runtime reads the instance's runtime state under its lock. Taking the lock
// orders the read after any supervision work in progress, which is what
// makes the host-layer fakes safe to inspect afterwards.
func (h *harness) runtime(t *testing.T) types.InstanceStatus {
	t.Helper()

	lock := h.mgr.lock(h.inst.ID)
	lock.Lock()
	defer lock.Unlock()

	rt, err := h.mgr.Runtime(h.inst)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	return rt
}

// waitForState polls until the instance reaches want.
func (h *harness) waitForState(t *testing.T, want types.InstanceState) types.InstanceStatus {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		rt := h.runtime(t)
		if rt.State == want {
			return rt
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", rt.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// start starts the harness instance, checking that its VMM is supervised.
func (h *harness) start(t *testing.T) {
	t.Helper()

	if err := h.mgr.Start(t.Context(), h.inst); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.mgr.vmm(h.inst.ID) != h.starter.vmm() {
		t.Fatal("started VMM is not supervised")
	}
}

// crash kills the current VMM from outside the daemon, as an operator's
// kill -9 or the OOM killer would.
func (h *harness) crash(t *testing.T) {
	t.Helper()

	if err := syscall.Kill(h.starter.vmm().PID(), syscall.SIGKILL); err != nil {
		t.Fatalf("kill VMM: %v", err)
	}
}

// setRestart gives the harness instance a restart policy, in the definition
// the manager reads as well as the harness's copy.
func (h *harness) setRestart(t *testing.T, p types.RestartPolicy) {
	t.Helper()

	h.inst.Restart = p
	h.definitions.instances[h.inst.Name] = h.inst
}

// exit ends the current guest as dicer-init does when its workload exits:
// the exit code is reported on the status disk, and the VMM goes.
func (h *harness) exit(t *testing.T, code int) {
	t.Helper()

	if err := writeStatusDisk(h.mgr.statusDiskPath(h.inst.ID), guest.Status{Boots: 1, ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(h.starter.vmm().PID(), syscall.SIGTERM); err != nil {
		t.Fatalf("end VMM: %v", err)
	}
}

// restartAtOnce has restarts skip their backoff.
func (h *harness) restartAtOnce() {
	h.mgr.restartWait = func(time.Time) time.Duration { return 0 }
}

// waitForVMMs waits until the starter has launched n VMMs in all.
func (h *harness) waitForVMMs(t *testing.T, n int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		lock := h.mgr.lock(h.inst.ID)
		lock.Lock()
		launched := len(h.starter.vmms)
		lock.Unlock()

		if launched >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("launched %d VMMs, want %d", launched, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// forceState records an instance as being in state, bypassing the state
// machine, for tests that begin part way through a lifecycle.
func forceState(t *testing.T, mgr *Manager, instanceID string, state types.InstanceState) {
	t.Helper()

	rt, err := mgr.readRuntime(instanceID)
	if err != nil {
		t.Fatalf("readRuntime: %v", err)
	}
	rt.State = state
	if err := mgr.writeRuntime(rt); err != nil {
		t.Fatalf("writeRuntime: %v", err)
	}
}
