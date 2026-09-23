// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// The fakes below are why Definitions and Addresses are interfaces declared in
// this package: the lifecycle can be tested without a filesystem, without the
// storage implementation, and -- since filestore imports vm -- without an
// import cycle.

// fakeDefinitions is an in-memory Definitions.
//
// The manager writes to it from the goroutines that supervise a guest -- an
// instance started with --rm is deleted by the one that notices the VMM
// exit -- while the test that is waiting reads it. So every method takes the
// mutex, as the real store's own locking does.
type fakeDefinitions struct {
	mu        sync.Mutex
	instances map[string]types.InstanceSpec
	networks  map[string]types.Network
	kernels   map[string]types.Kernel
	volumes   map[string]types.Volume
	dir       string
}

func newFakeDefinitions(dir string) *fakeDefinitions {
	return &fakeDefinitions{
		instances: make(map[string]types.InstanceSpec),
		networks:  make(map[string]types.Network),
		kernels:   make(map[string]types.Kernel),
		volumes:   make(map[string]types.Volume),
		dir:       dir,
	}
}

func (f *fakeDefinitions) GetInstance(nameOrID string) (types.InstanceSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.getInstance(nameOrID)
}

// getInstance is GetInstance without the lock, for the methods that already
// hold it.
func (f *fakeDefinitions) getInstance(nameOrID string) (types.InstanceSpec, error) {
	if inst, ok := f.instances[nameOrID]; ok {
		return inst, nil
	}
	for _, inst := range f.instances {
		if inst.ID == nameOrID {
			return inst, nil
		}
	}
	return types.InstanceSpec{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

func (f *fakeDefinitions) ListInstances() ([]types.InstanceSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	names := slices.Sorted(maps.Keys(f.instances))
	out := make([]types.InstanceSpec, 0, len(names))
	for _, n := range names {
		out = append(out, f.instances[n])
	}
	return out, nil
}

func (f *fakeDefinitions) CreateInstance(inst types.InstanceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.instances[inst.Name]; ok {
		return errdefs.Exists("instance %q already exists", inst.Name)
	}
	f.instances[inst.Name] = inst
	return nil
}

func (f *fakeDefinitions) UpdateInstance(inst types.InstanceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, err := f.getInstance(inst.ID); err != nil {
		return err
	}
	f.instances[inst.Name] = inst
	return nil
}

func (f *fakeDefinitions) RenameInstance(nameOrID string, renamed types.InstanceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	current, err := f.getInstance(nameOrID)
	if err != nil {
		return err
	}
	if _, taken := f.instances[renamed.Name]; taken && renamed.Name != current.Name {
		return errdefs.Exists("instance %q already exists", renamed.Name)
	}

	delete(f.instances, current.Name)
	f.instances[renamed.Name] = renamed

	return nil
}

func (f *fakeDefinitions) DeleteInstance(nameOrID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	inst, err := f.getInstance(nameOrID)
	if err != nil {
		return err
	}
	delete(f.instances, inst.Name)
	return nil
}

func (f *fakeDefinitions) InstanceDir(name string) string {
	return filepath.Join(f.dir, "instances", name)
}

func (f *fakeDefinitions) GetNetwork(nameOrID string) (types.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if n, ok := f.networks[nameOrID]; ok {
		return n, nil
	}
	return types.Network{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

func (f *fakeDefinitions) ListNetworks() ([]types.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	names := slices.Sorted(maps.Keys(f.networks))
	out := make([]types.Network, 0, len(names))
	for _, n := range names {
		out = append(out, f.networks[n])
	}
	return out, nil
}

func (f *fakeDefinitions) GetKernel(nameOrID string) (types.Kernel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if k, ok := f.kernels[nameOrID]; ok {
		return k, nil
	}
	return types.Kernel{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

func (f *fakeDefinitions) GetVolume(nameOrID string) (types.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if v, ok := f.volumes[nameOrID]; ok {
		return v, nil
	}
	return types.Volume{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

// fakeAddresses is an in-memory Addresses.
type fakeAddresses struct {
	byNetwork map[string][]types.NetworkAllocation
	next      int
}

func newFakeAddresses() *fakeAddresses {
	return &fakeAddresses{byNetwork: make(map[string][]types.NetworkAllocation)}
}

func (f *fakeAddresses) Allocate(n types.Network, instanceID, staticIP string) (types.NetworkAllocation, error) {
	for _, existing := range f.byNetwork[n.Name] {
		if existing.InstanceID == instanceID {
			return existing, nil
		}
	}

	f.next++
	ip := staticIP
	if ip == "" {
		ip = fmt.Sprintf("10.0.0.%d", f.next+1)
	}

	alloc := types.NetworkAllocation{
		NetworkID:  n.ID,
		InstanceID: instanceID,
		IP:         ip,
		MAC:        fmt.Sprintf("02:00:00:00:00:%02x", f.next),
	}
	f.byNetwork[n.Name] = append(f.byNetwork[n.Name], alloc)
	return alloc, nil
}

func (f *fakeAddresses) Get(networkName, instanceID string) (types.NetworkAllocation, error) {
	for _, alloc := range f.byNetwork[networkName] {
		if alloc.InstanceID == instanceID {
			return alloc, nil
		}
	}
	return types.NetworkAllocation{}, fmt.Errorf("%q: %w", instanceID, errdefs.ErrNotFound)
}

func (f *fakeAddresses) Release(networkName, instanceID string) error {
	kept := f.byNetwork[networkName][:0]
	for _, alloc := range f.byNetwork[networkName] {
		if alloc.InstanceID != instanceID {
			kept = append(kept, alloc)
		}
	}
	f.byNetwork[networkName] = kept
	return nil
}

func (f *fakeAddresses) Reconcile(networks []string, live map[string]struct{}) (int, error) {
	var released int
	for _, n := range networks {
		kept := make([]types.NetworkAllocation, 0, len(f.byNetwork[n]))
		for _, alloc := range f.byNetwork[n] {
			if _, ok := live[alloc.InstanceID]; ok {
				kept = append(kept, alloc)
				continue
			}
			released++
		}
		f.byNetwork[n] = kept
	}
	return released, nil
}

// fakeHostNetwork records what the manager asked of the host network, so a
// test can assert that cleanup actually reached the host layer.
type fakeHostNetwork struct {
	setUpBridges    []string
	removedTAPs     []string
	tornDownBridges []string

	// published holds the ports each instance has published, by instance
	// ID, and at which address; unpublished lists every UnpublishPorts.
	published   map[string]publishedPorts
	unpublished []string
	publishErr  error

	// cancelledTeardowns counts the teardowns asked for with a context
	// already cancelled, which the real host network could not carry out.
	cancelledTeardowns atomic.Int32
}

type publishedPorts struct {
	ip    string
	ports []types.PortMapping
}

func (f *fakeHostNetwork) SetupBridge(_ context.Context, n *types.Network) error {
	f.setUpBridges = append(f.setUpBridges, n.Bridge)
	return nil
}

func (f *fakeHostNetwork) CreateTAP(
	_ context.Context, _ *types.Network, _ *types.NetworkAllocation, _ network.Bandwidth,
) error {
	return nil
}

func (f *fakeHostNetwork) RemoveTAP(_ context.Context, _ *types.Network, instanceID string) {
	f.removedTAPs = append(f.removedTAPs, instanceID)
}

func (f *fakeHostNetwork) PublishPorts(
	_ context.Context, _ *types.Network, alloc *types.NetworkAllocation, ports []types.PortMapping,
) error {
	if f.publishErr != nil {
		return f.publishErr
	}
	if f.published == nil {
		f.published = make(map[string]publishedPorts)
	}
	f.published[alloc.InstanceID] = publishedPorts{ip: alloc.IP, ports: ports}
	return nil
}

func (f *fakeHostNetwork) UnpublishPorts(ctx context.Context, instanceID string) {
	if ctx.Err() != nil {
		f.cancelledTeardowns.Add(1)
	}
	f.unpublished = append(f.unpublished, instanceID)
	delete(f.published, instanceID)
}

func (f *fakeHostNetwork) TeardownBridge(ctx context.Context, n *types.Network) {
	if ctx.Err() != nil {
		f.cancelledTeardowns.Add(1)
	}
	f.tornDownBridges = append(f.tornDownBridges, n.Bridge)
}

// fakeImages hands out a fixed image, standing in for the image store.
type fakeImages struct {
	diskPath string
	pulls    int

	// held, if set, is the image the host already has for every reference.
	held *types.Image
}

func (f *fakeImages) Get(ref string) (*types.Image, error) {
	if f.held == nil {
		return nil, errdefs.NotFound("no image %q", ref)
	}
	return f.held, nil
}

func (f *fakeImages) Pull(context.Context, string, image.ProgressFunc) (*types.Image, error) {
	f.pulls++
	return &types.Image{
		Name:       "docker.io/library/alpine:3.21",
		Digest:     "sha256:aaaa",
		DiskPath:   f.diskPath,
		Entrypoint: []string{"/bin/sh"},
	}, nil
}

// fakeHypervisor records the control operations asked of a running guest.
type fakeHypervisor struct {
	caps hypervisor.Capabilities

	paused, resumed int
	snapshotDirs    []string
	snapshotErr     error

	// onShutdown, if set, is what the VMM does when asked to exit.
	onShutdown func()
}

func newFakeHypervisor() *fakeHypervisor {
	return &fakeHypervisor{caps: hypervisor.Capabilities{SupportsSnapshot: true, SupportsPause: true}}
}

func (f *fakeHypervisor) Capabilities() hypervisor.Capabilities { return f.caps }

func (f *fakeHypervisor) PauseVM(context.Context) error {
	f.paused++
	return nil
}

func (f *fakeHypervisor) ResumeVM(context.Context) error {
	f.resumed++
	return nil
}

// SnapshotVM writes a file where a hypervisor would write the guest's state.
func (f *fakeHypervisor) SnapshotVM(_ context.Context, destPath string) error {
	if f.snapshotErr != nil {
		return f.snapshotErr
	}
	f.snapshotDirs = append(f.snapshotDirs, destPath)

	if err := os.MkdirAll(destPath, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destPath, "vmstate"), []byte("guest state"), 0o600)
}

func (f *fakeHypervisor) DestroyVM(context.Context) error  { return nil }
func (f *fakeHypervisor) ShutdownVM(context.Context) error { return nil }

func (f *fakeHypervisor) Shutdown(context.Context) error {
	if f.onShutdown != nil {
		f.onShutdown()
	}
	return nil
}

func (f *fakeHypervisor) GetVMInfo(context.Context) (*hypervisor.VirtualMachineInfo, error) {
	return &hypervisor.VirtualMachineInfo{State: hypervisor.VirtualMachineStateRunning}, nil
}

func (f *fakeHypervisor) ResizeVMMemory(context.Context, int64) error { return nil }

func (f *fakeHypervisor) ResizeVMMemoryAndWait(context.Context, int64, time.Duration) error {
	return nil
}

func (f *fakeHypervisor) ResizeVMCPU(context.Context, int) error { return nil }

// fakeStarter hands back a fakeHypervisor, remembers what it was asked to
// restore, and stands in for the VMM with a real process -- a sleep -- so
// that supervision sees a real exit when a test kills it.
type fakeStarter struct {
	version         string
	hv              *fakeHypervisor
	restoredFrom    []string
	restoredConsole hypervisor.ConsoleConfig
	restoreErr      error
	startErr        error

	// vmms is every process the starter launched, the latest last.
	vmms []*process.Process
}

func (f *fakeStarter) Version() string         { return f.version }
func (f *fakeStarter) DefaultBootArgs() string { return "console=ttyS0" }
func (f *fakeStarter) PowerOffEndsVM() bool    { return true }

func (f *fakeStarter) StartVM(
	context.Context, string, hypervisor.VirtualMachine,
) (*process.Process, hypervisor.Hypervisor, error) {
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	vmm, err := f.launch()
	if err != nil {
		return nil, nil, err
	}
	return vmm, f.hv, nil
}

func (f *fakeStarter) RestoreVM(
	_ context.Context, _ string, snapshotPath string, console hypervisor.ConsoleConfig,
) (*process.Process, hypervisor.Hypervisor, error) {
	if f.restoreErr != nil {
		return nil, nil, f.restoreErr
	}
	f.restoredFrom = append(f.restoredFrom, snapshotPath)
	f.restoredConsole = console

	vmm, err := f.launch()
	if err != nil {
		return nil, nil, err
	}
	return vmm, f.hv, nil
}

func (f *fakeStarter) Connect(string) (hypervisor.Hypervisor, error) { return f.hv, nil }

// launch starts a process to play the VMM.
func (f *fakeStarter) launch() (*process.Process, error) {
	vmm, err := process.Start(exec.Command("sleep", "60")) //nolint:noctx // a VMM outlives the request that started it
	if err != nil {
		return nil, err
	}
	f.vmms = append(f.vmms, vmm)
	return vmm, nil
}

// vmm returns the process most recently launched.
func (f *fakeStarter) vmm() *process.Process {
	if len(f.vmms) == 0 {
		return nil
	}
	return f.vmms[len(f.vmms)-1]
}

// terminateAll kills every process the starter launched.
func (f *fakeStarter) terminateAll() {
	for _, vmm := range f.vmms {
		vmm.Terminate()
	}
}

// fakeKernels resolves every kernel to the same path.
type fakeKernels struct{ path string }

func (f fakeKernels) Path(context.Context, types.Kernel) (string, error) { return f.path, nil }

// fakeInitrds prepares nothing and returns a fixed path.
type fakeInitrds struct{ path string }

func (f fakeInitrds) Prepare(context.Context) (string, error) { return f.path, nil }

// fakeVolumes locates volume disks under a directory, as volume.Manager does.
type fakeVolumes struct{ dir string }

func (f fakeVolumes) Path(id string) string {
	return filepath.Join(f.dir, id, "disk.raw")
}

// recordedOp is one call to the Metrics recorder.
type recordedOp struct {
	operation string
	failed    bool
}

// fakeMetrics is a Metrics that remembers what it was told.
type fakeMetrics struct {
	ops      []recordedOp
	restarts int
}

func (f *fakeMetrics) RecordInstanceRestart() { f.restarts++ }

func (f *fakeMetrics) RecordInstanceOperation(operation string, err error, _ time.Duration) {
	f.ops = append(f.ops, recordedOp{operation: operation, failed: err != nil})
}

// fakeProbe answers health check probes as a test says, and counts them.
type fakeProbe struct {
	mu      sync.Mutex
	healthy bool
	err     error
	probes  int
}

func (f *fakeProbe) probe(context.Context, string, types.HealthCheck) (probeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.probes++
	if f.err != nil {
		return probeResult{Output: f.err.Error(), At: time.Now()}, f.err
	}
	output := "ok"
	if !f.healthy {
		output = "connection refused"
	}
	return probeResult{Healthy: f.healthy, Output: output, At: time.Now()}, nil
}

func (f *fakeProbe) set(healthy bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthy, f.err = healthy, err
}

func (f *fakeProbe) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probes
}

// fakeEvents remembers the events it is given.
type fakeEvents struct {
	mu     sync.Mutex
	events []types.Event
}

func (f *fakeEvents) Record(e types.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

// undescribed returns the events recorded without a description.
func (f *fakeEvents) undescribed() []types.Event {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []types.Event
	for _, e := range f.events {
		if e.Message == "" {
			out = append(out, e)
		}
	}
	return out
}

// actions returns the actions recorded so far, in order.
func (f *fakeEvents) actions() []types.EventAction {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]types.EventAction, 0, len(f.events))
	for _, e := range f.events {
		out = append(out, e.Action)
	}
	return out
}

// last returns the last event with action, and whether there is one.
func (f *fakeEvents) last(action types.EventAction) (types.Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, e := range slices.Backward(f.events) {
		if e.Action == action {
			return e, true
		}
	}
	return types.Event{}, false
}
