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

	manager := NewManager(Config{
		Definitions: definitions,
		Networks:    newFakeNetworks(),
		RunDir:      filepath.Join(dir, "run"),
		HostNetwork: hostNetwork,
		Logger:      slog.New(slog.DiscardHandler),
	})
	t.Cleanup(manager.Close)

	// Nothing is adoptable until a test says so; see startAdoptable.
	manager.attach = func(pid int, _ string) (*process.Process, error) {
		return nil, fmt.Errorf("process %d: %w", pid, errNotRunning)
	}

	return manager, definitions, hostNetwork
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

	instance := types.InstanceSpec{
		ID: "id-" + name, Name: name,
		ImageRef: "alpine:latest", KernelName: "k", NetworkName: "default", VCPUs: 1,
	}
	definitions.instances[name] = instance

	return instance
}

// harness is an instance that can be started, stopped and snapshotted: a
// manager wired to a fake hypervisor whose VMMs are real processes, and an
// overlay disk with something in it.
type harness struct {
	manager     *Manager
	events      *fakeRecorder
	definitions *fakeDefinitions
	hostNetwork *fakeHostNetwork
	instance    types.InstanceSpec
	starter     *fakeStarter
	hv          *fakeHypervisor
	agent       *fakeGuestAgent
	overlay     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	manager, definitions, hostNetwork := newTestManager(t)
	instance := seedInstance(t, definitions, "web")
	definitions.kernels["k"] = types.Kernel{Name: "k"}

	hv := newFakeHypervisor()
	starter := &fakeStarter{version: testHypervisorVersion, hv: hv}
	// A VMM asked to shut down exits, as a real one does.
	hv.onShutdown = func() { _ = starter.vmm().Kill() }
	manager.starters = map[types.HypervisorType][]hypervisor.Starter{
		types.HypervisorTypeCloudHypervisor: {starter},
	}
	// Stop watching before the VMMs are killed, so that killing them at the
	// end of the test is not mistaken for a crash.
	t.Cleanup(func() {
		manager.Close()
		starter.terminateAll()
	})

	manager.images = &fakeImages{diskPath: filepath.Join(t.TempDir(), "disk.img")}
	manager.kernels = fakeKernels{path: "/boot/vmlinux"}
	manager.initrds = fakeInitrds{path: "/boot/initrd"}
	// There is no guest agent to ask for a graceful stop: tests that want
	// one say how the guest answers.
	manager.shutdownGuest = func(context.Context, string) error { return errors.New("no guest agent") }
	agent := &fakeGuestAgent{hostNetwork: hostNetwork}
	manager.setGuestClock = agent.setClock
	manager.setGuestIdentity = agent.setIdentity
	// Building a real config disk needs mke2fs and root.
	manager.provisionConfigDisk = func(_ context.Context, path string, _ *guest.Config) error {
		return os.WriteFile(path, []byte("config disk"), 0o600)
	}

	overlay := manager.overlayDiskPath(instance)
	if err := os.MkdirAll(filepath.Dir(overlay), 0o750); err != nil {
		t.Fatal(err)
	}
	// A hole, then data: a disk copy has to preserve both. Its existence
	// also spares Start formatting a new one.
	if err := os.WriteFile(overlay, append(make([]byte, 2<<20), []byte("guest data")...), 0o600); err != nil {
		t.Fatal(err)
	}

	recorded := &fakeRecorder{}
	manager.events = recorded

	return &harness{manager: manager, events: recorded, definitions: definitions, hostNetwork: hostNetwork, instance: instance, starter: starter, hv: hv, agent: agent, overlay: overlay}
}

// running records the instance as running without starting anything, for
// tests that only need the recorded state.
func (h *harness) running(t *testing.T) {
	t.Helper()

	pid := os.Getpid()
	err := h.manager.writeStatus(types.InstanceStatus{
		InstanceID:        h.instance.ID,
		State:             types.InstanceStateRunning,
		VMMPID:            &pid,
		HypervisorVersion: testHypervisorVersion,
		VCPUs:             h.instance.VCPUs,
		MemoryBytes:       h.instance.MemoryBytes,
		ImageDigest:       "sha256:aaaa",
	})
	if err != nil {
		t.Fatalf("writeStatus: %v", err)
	}

	// A running guest holds an address on its network.
	nw, err := h.definitions.Network(h.instance.NetworkName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.manager.networks.Allocate(nw, h.instance.ID, h.instance.StaticIP); err != nil {
		t.Fatalf("Allocate: %v", err)
	}
}

// status reads the instance's status under its lock. Taking the lock
// orders the read after any supervision work in progress, which is what
// makes the host-layer fakes safe to inspect afterwards.
func (h *harness) status(t *testing.T) types.InstanceStatus {
	t.Helper()

	lock := h.manager.lock(h.instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := h.manager.Status(h.instance)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return status
}

// waitForState polls until the instance reaches want.
func (h *harness) waitForState(t *testing.T, want types.InstanceState) types.InstanceStatus {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		status := h.status(t)
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", status.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// start starts the harness instance, checking that its VMM is supervised.
func (h *harness) start(t *testing.T) {
	t.Helper()

	if err := h.manager.Start(t.Context(), h.instance); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.manager.vmm(h.instance.ID) != h.starter.vmm() {
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

	h.instance.Restart = p
	h.definitions.instances[h.instance.Name] = h.instance
}

// exit ends the current guest as dicer-init does when its workload exits:
// the exit code is reported on the status disk, and the VMM goes.
func (h *harness) exit(t *testing.T, code int) {
	t.Helper()

	if err := writeStatusDisk(h.manager.statusDiskPath(h.instance.ID), guest.Status{Boots: 1, ExitCode: &code}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(h.starter.vmm().PID(), syscall.SIGTERM); err != nil {
		t.Fatalf("end VMM: %v", err)
	}
}

// restartAtOnce has restarts skip their backoff.
func (h *harness) restartAtOnce() {
	h.manager.restartWait = func(time.Time) time.Duration { return 0 }
}

// waitForVMMs waits until the starter has launched n VMMs in all.
func (h *harness) waitForVMMs(t *testing.T, n int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		lock := h.manager.lock(h.instance.ID)
		lock.Lock()
		launched := h.starter.vmmCount()
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
func forceState(t *testing.T, manager *Manager, instanceID string, state types.InstanceState) {
	t.Helper()

	status, err := manager.readStatus(instanceID)
	if err != nil {
		t.Fatalf("readStatus: %v", err)
	}
	status.State = state
	if err := manager.writeStatus(status); err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
}
