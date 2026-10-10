// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package instance

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
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/volume"
)

// testHypervisorVersion is the version the fake hypervisor reports.
const testHypervisorVersion = "v49.0.0"

// newTestManager returns a Manager over in-memory definitions and a fake host
// network, with nothing wired up to start a VMM; see newHarness for that.
func newTestManager(t *testing.T) (*Manager, *fakeStore, *fakeHostNetwork) {
	t.Helper()

	dir := t.TempDir()
	store := newFakeStore(dir)
	hostNetwork := &fakeHostNetwork{}

	manager := NewManager(Config{
		Store:       store,
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
	// Every guest boots at once, unless a test says otherwise.
	manager.awaitAgent = func(context.Context, string) error { return nil }

	return manager, store, hostNetwork
}

// errNotRunning is what the test Manager's attach reports for every PID.
var errNotRunning = errors.New("not running")

// seedInstance defines an instance on the default network, defining that too
// if need be.
func seedInstance(t *testing.T, store *fakeStore, name string) Spec {
	t.Helper()

	if _, ok := store.networks["default"]; !ok {
		store.networks["default"] = network.Network{
			ID: "net-default", Name: "default",
			Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Bridge: "dicer-default",
		}
	}

	instance := Spec{
		ID: "id-" + name, Name: name,
		ImageRef: "alpine:latest", ImageDigest: "sha256:aaaa", KernelName: "k", NetworkName: "default",
		VCPUs: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 20,
	}
	store.instances[name] = instance

	return instance
}

// harness is an instance that can be started, stopped and snapshotted: a
// manager wired to a fake hypervisor whose VMMs are real processes, and an
// overlay disk with something in it.
type harness struct {
	manager     *Manager
	events      *fakeRecorder
	store       *fakeStore
	hostNetwork *fakeHostNetwork
	instance    Spec
	starter     *fakeStarter
	hv          *fakeHypervisor
	agent       *fakeGuestAgent
	overlay     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	manager, store, hostNetwork := newTestManager(t)
	instance := seedInstance(t, store, "web")
	store.kernels["k"] = kernel.Kernel{Name: "k"}
	store.volumes["data"] = volume.Volume{ID: "volume-data", Name: "data", SizeBytes: 1 << 30}

	hv := newFakeHypervisor()
	starter := &fakeStarter{version: testHypervisorVersion, hv: hv}
	// A VMM asked to shut down exits, as a real one does.
	hv.onShutdown = func() { _ = starter.vmm().Kill() }
	manager.starters = map[hypervisor.Type][]hypervisor.Starter{
		hypervisor.TypeCloudHypervisor: {starter},
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

	return &harness{manager: manager, events: recorded, store: store, hostNetwork: hostNetwork, instance: instance, starter: starter, hv: hv, agent: agent, overlay: overlay}
}

// running records the instance as running without starting anything, for
// tests that only need the recorded state.
func (h *harness) running(t *testing.T) {
	t.Helper()
	h.define()

	pid := os.Getpid()
	err := h.manager.writeStatus(Status{
		InstanceID:        h.instance.ID,
		State:             StateRunning,
		VMMPID:            &pid,
		HypervisorVersion: testHypervisorVersion,
		VCPUs:             h.instance.VCPUs,
		MemoryBytes:       h.instance.MemoryBytes,
		ImageDigest:       "sha256:aaaa",
	})
	if err != nil {
		t.Fatalf("writeStatus: %v", err)
	}
	// A booted guest has counted its boot on its status disk.
	if err := writeStatusDisk(h.manager.statusDiskPath(h.instance.ID), guest.Status{Boots: 1}); err != nil {
		t.Fatal(err)
	}

	// A running guest holds an address on its network.
	nw, err := h.store.Network(h.instance.NetworkName)
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
func (h *harness) status(t *testing.T) Status {
	t.Helper()

	lock := h.manager.lock(h.instance.ID)
	lock.Lock()
	defer lock.Unlock()

	status, err := h.manager.statusOf(h.instance)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return status
}

// waitForState polls until the instance reaches want.
func (h *harness) waitForState(t *testing.T, want State) Status {
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
	h.define()

	if err := h.manager.start(t.Context(), h.instance); err != nil {
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
func (h *harness) setRestart(t *testing.T, p RestartPolicy) {
	t.Helper()

	h.instance.Restart = p
	h.define()
}

// define stores h.instance as the instance's definition, as a test has
// changed it. The manager reads the definition again under the instance's
// lock, so a change a test makes counts only once it is stored.
func (h *harness) define() {
	h.store.instances[h.instance.Name] = h.instance
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
func forceState(t *testing.T, manager *Manager, instanceID string, state State) {
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

// withPorts gives the harness's instance ports, in its definition too.
func (h *harness) withPorts(ports ...network.PortMapping) {
	h.instance.Ports = ports
	h.store.instances[h.instance.Name] = h.instance
}
