// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// The fakes below are why Definitions and Networks are interfaces declared in
// this package: the lifecycle can be tested without a filesystem or the
// storage implementation.

// fakeDefinitions is an in-memory Definitions.
//
// The manager writes to it from the goroutines that supervise a guest -- an
// instance started with --rm is deleted by the one that notices the VMM
// exit -- while the test that is waiting reads it. So every method takes the
// mutex, as filestore.Manager's own locking does.
type fakeDefinitions struct {
	mu        sync.Mutex
	instances map[string]types.InstanceSpec
	snapshots map[string]types.Snapshot
	networks  map[string]types.Network
	kernels   map[string]types.Kernel
	volumes   map[string]types.Volume
	dir       string
}

func newFakeDefinitions(dir string) *fakeDefinitions {
	return &fakeDefinitions{
		instances: make(map[string]types.InstanceSpec),
		snapshots: make(map[string]types.Snapshot),
		networks:  make(map[string]types.Network),
		kernels:   make(map[string]types.Kernel),
		volumes:   make(map[string]types.Volume),
		dir:       dir,
	}
}

func (f *fakeDefinitions) Instance(nameOrID string) (types.InstanceSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.instance(nameOrID)
}

// instance is Instance without the lock, for the methods that already
// hold it.
func (f *fakeDefinitions) instance(nameOrID string) (types.InstanceSpec, error) {
	if instance, ok := f.instances[nameOrID]; ok {
		return instance, nil
	}
	for _, instance := range f.instances {
		if instance.ID == nameOrID {
			return instance, nil
		}
	}
	return types.InstanceSpec{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

func (f *fakeDefinitions) Instances() []types.InstanceSpec {
	f.mu.Lock()
	defer f.mu.Unlock()

	names := slices.Sorted(maps.Keys(f.instances))
	out := make([]types.InstanceSpec, 0, len(names))
	for _, n := range names {
		out = append(out, f.instances[n])
	}
	return out
}

func (f *fakeDefinitions) MatchingInstances(match func(types.InstanceSpec) bool) []types.InstanceSpec {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []types.InstanceSpec
	for _, instance := range f.instances {
		if match(instance) {
			out = append(out, instance)
		}
	}
	return out
}

func (f *fakeDefinitions) CreateInstance(instance types.InstanceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.instances[instance.Name]; ok {
		return errdefs.Exists("instance %q already exists", instance.Name)
	}
	f.instances[instance.Name] = instance
	return nil
}

func (f *fakeDefinitions) UpdateInstance(instance types.InstanceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, err := f.instance(instance.ID); err != nil {
		return err
	}
	f.instances[instance.Name] = instance
	return nil
}

func (f *fakeDefinitions) RenameInstance(nameOrID string, renamed types.InstanceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	current, err := f.instance(nameOrID)
	if err != nil {
		return err
	}
	if _, taken := f.instances[renamed.Name]; taken && renamed.Name != current.Name {
		return errdefs.Exists("instance %q already exists", renamed.Name)
	}

	// The directory moves with the name, as filestore's does.
	if err := os.Rename(f.InstanceDir(current.Name), f.InstanceDir(renamed.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(f.instances, current.Name)
	f.instances[renamed.Name] = renamed

	return nil
}

func (f *fakeDefinitions) DeleteInstance(nameOrID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	instance, err := f.instance(nameOrID)
	if err != nil {
		return err
	}
	delete(f.instances, instance.Name)
	return nil
}

func (f *fakeDefinitions) InstanceDir(name string) string {
	return filepath.Join(f.dir, "instances", name)
}

func (f *fakeDefinitions) StageSnapshot() (string, error) {
	dir := filepath.Join(f.dir, "snapshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, ".staging-")
}

func (f *fakeDefinitions) CreateSnapshot(snapshot types.Snapshot, staged string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.snapshots[snapshot.Name]; ok {
		return fmt.Errorf("%q: %w", snapshot.Name, errdefs.ErrExists)
	}
	if err := os.Rename(staged, f.SnapshotDir(snapshot.Name)); err != nil {
		return err
	}
	f.snapshots[snapshot.Name] = snapshot
	return nil
}

func (f *fakeDefinitions) Snapshot(nameOrID string) (types.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.snapshot(nameOrID)
}

func (f *fakeDefinitions) snapshot(nameOrID string) (types.Snapshot, error) {
	for _, s := range f.snapshots {
		if s.Name == nameOrID || s.ID == nameOrID {
			return s, nil
		}
	}
	return types.Snapshot{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

func (f *fakeDefinitions) Snapshots() []types.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()

	names := slices.Sorted(maps.Keys(f.snapshots))
	out := make([]types.Snapshot, 0, len(names))
	for _, n := range names {
		out = append(out, f.snapshots[n])
	}
	return out
}

func (f *fakeDefinitions) DeleteSnapshot(nameOrID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	snapshot, err := f.snapshot(nameOrID)
	if err != nil {
		return err
	}
	delete(f.snapshots, snapshot.Name)
	return os.RemoveAll(f.SnapshotDir(snapshot.Name))
}

func (f *fakeDefinitions) SnapshotDir(name string) string {
	return filepath.Join(f.dir, "snapshots", name)
}

func (f *fakeDefinitions) Network(nameOrID string) (types.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if n, ok := f.networks[nameOrID]; ok {
		return n, nil
	}
	return types.Network{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

func (f *fakeDefinitions) Networks() []types.Network {
	f.mu.Lock()
	defer f.mu.Unlock()

	names := slices.Sorted(maps.Keys(f.networks))
	out := make([]types.Network, 0, len(names))
	for _, n := range names {
		out = append(out, f.networks[n])
	}
	return out
}

func (f *fakeDefinitions) Kernel(nameOrID string) (types.Kernel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if k, ok := f.kernels[nameOrID]; ok {
		return k, nil
	}
	return types.Kernel{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

func (f *fakeDefinitions) Volume(nameOrID string) (types.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if v, ok := f.volumes[nameOrID]; ok {
		return v, nil
	}
	return types.Volume{}, fmt.Errorf("%q: %w", nameOrID, errdefs.ErrNotFound)
}

// fakeNetworks is an in-memory Networks. It is safe for concurrent use, as
// a restart the manager schedules allocates from another goroutine.
type fakeNetworks struct {
	mu        sync.Mutex
	byNetwork map[string][]types.NetworkAllocation
	next      int
}

func newFakeNetworks() *fakeNetworks {
	return &fakeNetworks{byNetwork: make(map[string][]types.NetworkAllocation)}
}

func (f *fakeNetworks) Allocate(n types.Network, instanceID, staticIP string) (types.NetworkAllocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

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

	allocation := types.NetworkAllocation{
		NetworkID:  n.ID,
		InstanceID: instanceID,
		IP:         ip,
		MAC:        fmt.Sprintf("02:00:00:00:00:%02x", f.next),
	}
	f.byNetwork[n.Name] = append(f.byNetwork[n.Name], allocation)
	return allocation, nil
}

func (f *fakeNetworks) Allocation(networkName, instanceID string) (types.NetworkAllocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, allocation := range f.byNetwork[networkName] {
		if allocation.InstanceID == instanceID {
			return allocation, nil
		}
	}
	return types.NetworkAllocation{}, fmt.Errorf("%q: %w", instanceID, errdefs.ErrNotFound)
}

func (f *fakeNetworks) InstanceAt(networkName, ip string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, allocation := range f.byNetwork[networkName] {
		if allocation.IP == ip {
			return allocation.InstanceID, true
		}
	}
	return "", false
}

func (f *fakeNetworks) Release(networkName, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	kept := f.byNetwork[networkName][:0]
	for _, allocation := range f.byNetwork[networkName] {
		if allocation.InstanceID != instanceID {
			kept = append(kept, allocation)
		}
	}
	f.byNetwork[networkName] = kept
	return nil
}

// allocations returns a copy of the network's allocations.
func (f *fakeNetworks) allocations(networkName string) []types.NetworkAllocation {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.byNetwork[networkName])
}

func (f *fakeNetworks) Reconcile(networks []string, live map[string]struct{}) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var released int
	for _, n := range networks {
		kept := make([]types.NetworkAllocation, 0, len(f.byNetwork[n]))
		for _, allocation := range f.byNetwork[n] {
			if _, ok := live[allocation.InstanceID]; ok {
				kept = append(kept, allocation)
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

	// disconnected holds the instances whose TAP device is detached from
	// its bridge.
	disconnected map[string]bool

	// bandwidth is what the last TAP device was limited to.
	bandwidth network.Bandwidth

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

func (f *fakeHostNetwork) SetupBridge(_ context.Context, nw *types.Network) error {
	f.setUpBridges = append(f.setUpBridges, nw.Bridge)
	return nil
}

func (f *fakeHostNetwork) CreateTAP(
	_ context.Context, _ *types.Network, _ *types.NetworkAllocation, bandwidth network.Bandwidth,
) error {
	f.bandwidth = bandwidth
	return nil
}

func (f *fakeHostNetwork) RemoveTAP(_ context.Context, _ *types.Network, instanceID string) {
	f.removedTAPs = append(f.removedTAPs, instanceID)
}

func (f *fakeHostNetwork) DisconnectTAP(_ context.Context, _ *types.Network, instanceID string) error {
	if f.disconnected == nil {
		f.disconnected = make(map[string]bool)
	}
	f.disconnected[instanceID] = true
	return nil
}

func (f *fakeHostNetwork) ConnectTAP(_ context.Context, _ *types.Network, instanceID string) error {
	delete(f.disconnected, instanceID)
	return nil
}

func (f *fakeHostNetwork) PublishPorts(
	ctx context.Context, _ *types.Network, allocation *types.NetworkAllocation, ports []types.PortMapping,
) error {
	if f.publishErr != nil {
		return f.publishErr
	}
	// As the host does, refuse a port on a host address something listens
	// on, by binding it.
	for _, p := range ports {
		if p.HostIP == "" {
			continue
		}
		l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(p.HostIP, strconv.Itoa(int(p.HostPort))))
		if err != nil {
			return fmt.Errorf("port %d is in use: %w", p.HostPort, err)
		}
		_ = l.Close()
	}
	if f.published == nil {
		f.published = make(map[string]publishedPorts)
	}
	f.published[allocation.InstanceID] = publishedPorts{ip: allocation.IP, ports: ports}
	return nil
}

func (f *fakeHostNetwork) UnpublishPorts(ctx context.Context, instanceID string) {
	if ctx.Err() != nil {
		f.cancelledTeardowns.Add(1)
	}
	f.unpublished = append(f.unpublished, instanceID)
	delete(f.published, instanceID)
}

func (f *fakeHostNetwork) TeardownBridge(ctx context.Context, nw *types.Network) {
	if ctx.Err() != nil {
		f.cancelledTeardowns.Add(1)
	}
	f.tornDownBridges = append(f.tornDownBridges, nw.Bridge)
}

// fakeImages hands out a fixed image, standing in for the image store.
type fakeImages struct {
	diskPath string
	pulls    int

	// held, if set, is the image the host already has for every reference.
	held *types.Image
}

func (f *fakeImages) Image(ref string) (*types.Image, error) {
	if f.held == nil {
		return nil, errdefs.NotFound("no image %q", ref)
	}
	return f.held, nil
}

func (f *fakeImages) Ensure(_ context.Context, ref string, policy types.PullPolicy) (*types.Image, error) {
	if f.held != nil && policy != types.PullPolicyAlways {
		return f.held, nil
	}
	if policy == types.PullPolicyNever {
		return nil, errdefs.NotFound("no image %q", ref)
	}

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
	capabilities hypervisor.Capabilities

	paused, resumed int
	snapshotDirs    []string
	snapshotErr     error
	// restoringSnapshots is how many snapshots fail with
	// hypervisor.ErrRestoringMemory before one is taken.
	restoringSnapshots int
	// onSnapshot, if set, is called with the context a snapshot is taken in.
	onSnapshot func(ctx context.Context)

	// memoryBytes and vCPUs are what the guest was last resized to, and
	// resizeErr what a resize fails with.
	memoryBytes int64
	vCPUs       int
	resizeErr   error

	// onShutdown, if set, is what the VMM does when asked to exit.
	onShutdown func()
}

func newFakeHypervisor() *fakeHypervisor {
	return &fakeHypervisor{capabilities: hypervisor.Capabilities{SupportsSnapshot: true, SupportsPause: true}}
}

func (f *fakeHypervisor) Capabilities() hypervisor.Capabilities { return f.capabilities }

func (f *fakeHypervisor) PauseVM(context.Context) error {
	f.paused++
	return nil
}

func (f *fakeHypervisor) ResumeVM(context.Context) error {
	f.resumed++
	return nil
}

// SnapshotVM writes a file where a hypervisor would write the guest's state.
func (f *fakeHypervisor) SnapshotVM(ctx context.Context, destPath string) error {
	if f.onSnapshot != nil {
		f.onSnapshot(ctx)
	}
	if f.snapshotErr != nil {
		return f.snapshotErr
	}
	if f.restoringSnapshots > 0 {
		f.restoringSnapshots--
		return hypervisor.ErrRestoringMemory
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

func (f *fakeHypervisor) VMInfo(context.Context) (*hypervisor.VMInfo, error) {
	return &hypervisor.VMInfo{State: hypervisor.VMStateRunning}, nil
}

func (f *fakeHypervisor) ResizeVMMemory(_ context.Context, bytes int64) error {
	if f.resizeErr != nil {
		return f.resizeErr
	}
	f.memoryBytes = bytes
	return nil
}

func (f *fakeHypervisor) ResizeVMCPU(_ context.Context, count int) error {
	if f.resizeErr != nil {
		return f.resizeErr
	}
	f.vCPUs = count
	return nil
}

// fakeStarter hands back a fakeHypervisor, remembers what it was asked to
// restore, and stands in for the VMM with a real process -- a sleep -- so
// that supervision sees a real exit when a test kills it.
type fakeStarter struct {
	version      string
	hv           *fakeHypervisor
	restoredFrom []string
	restoredSpec hypervisor.RestoreSpec
	restoreErr   error
	startErr     error

	// spec is what the last guest was started with.
	spec hypervisor.VMSpec

	// mu guards vmms: a restart launches from its own goroutine while the
	// test reads them.
	mu sync.Mutex
	// vmms is every process the starter launched, the latest last.
	vmms []*process.Process
}

func (f *fakeStarter) Version() string           { return f.version }
func (f *fakeStarter) DefaultKernelArgs() string { return "console=ttyS0" }
func (f *fakeStarter) PowerOffEndsVM() bool      { return true }

func (f *fakeStarter) StartVM(
	_ context.Context, _ string, spec hypervisor.VMSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	f.spec = spec
	vmm, err := f.launch()
	if err != nil {
		return nil, nil, err
	}
	return vmm, f.hv, nil
}

func (f *fakeStarter) RestoreVM(
	_ context.Context, _ string, snapshotPath string, spec hypervisor.RestoreSpec,
) (*process.Process, hypervisor.Hypervisor, error) {
	if f.restoreErr != nil {
		return nil, nil, f.restoreErr
	}
	f.restoredFrom = append(f.restoredFrom, snapshotPath)
	f.restoredSpec = spec

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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vmms = append(f.vmms, vmm)
	return vmm, nil
}

// vmm returns the process most recently launched.
func (f *fakeStarter) vmm() *process.Process {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.vmms) == 0 {
		return nil
	}
	return f.vmms[len(f.vmms)-1]
}

// vmmCount returns how many processes the starter has launched.
func (f *fakeStarter) vmmCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.vmms)
}

// terminateAll kills every process the starter launched.
func (f *fakeStarter) terminateAll() {
	f.mu.Lock()
	vmms := slices.Clone(f.vmms)
	f.mu.Unlock()

	for _, vmm := range vmms {
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

// recordedOperation is one call to the Metrics recorder.
type recordedOperation struct {
	operation string
	failed    bool
}

// fakeMetrics is a Metrics that remembers what it was told.
type fakeMetrics struct {
	operations []recordedOperation
	restarts   int
}

func (f *fakeMetrics) RecordInstanceRestart() { f.restarts++ }

func (f *fakeMetrics) RecordInstanceOperation(operation string, err error, _ time.Duration) {
	f.operations = append(f.operations, recordedOperation{operation: operation, failed: err != nil})
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

// fakeRecorder remembers the events it is given.
type fakeRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (f *fakeRecorder) Record(e events.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

// undescribed returns the events recorded without a description.
func (f *fakeRecorder) undescribed() []events.Event {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []events.Event
	for _, e := range f.events {
		if e.Message == "" {
			out = append(out, e)
		}
	}
	return out
}

// actions returns the actions recorded so far, in order.
func (f *fakeRecorder) actions() []events.Action {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]events.Action, 0, len(f.events))
	for _, e := range f.events {
		out = append(out, e.Action)
	}
	return out
}

// last returns the last event with action, and whether there is one.
func (f *fakeRecorder) last(action events.Action) (events.Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, e := range slices.Backward(f.events) {
		if e.Action == action {
			return e, true
		}
	}
	return events.Event{}, false
}

// fakeGuestAgent stands in for a restored guest's agent, remembering what it
// was told.
type fakeGuestAgent struct {
	hostNetwork *fakeHostNetwork

	clockSets  int
	identities []*diceragentv1.SetIdentityRequest
	// identityErr is what SetIdentity fails with.
	identityErr error
	// connectedForIdentity is set if a guest was given its identity while
	// it could reach the network.
	connectedForIdentity bool
}

func (f *fakeGuestAgent) setClock(context.Context, string, time.Time) error {
	f.clockSets++
	return nil
}

func (f *fakeGuestAgent) setIdentity(_ context.Context, vsockPath string, req *diceragentv1.SetIdentityRequest) error {
	if f.identityErr != nil {
		return f.identityErr
	}
	f.identities = append(f.identities, req)
	// The vsock path is in the runtime directory, named for the instance.
	if !f.hostNetwork.disconnected[filepath.Base(filepath.Dir(vsockPath))] {
		f.connectedForIdentity = true
	}
	return nil
}
