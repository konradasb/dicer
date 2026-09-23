// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package vm implements the virtual machine lifecycle: starting, stopping,
// pausing and deleting instances.
//
// Operations are synchronous; a failed one unwinds what it allocated. Every
// host resource an instance holds is derived from its ID, so recovery after an
// unclean shutdown needs no journal. See recover.go.
package vm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
)

// Definitions stores the instance, network, volume and kernel definitions.
type Definitions interface {
	CreateInstance(inst types.InstanceSpec) error
	GetInstance(nameOrID string) (types.InstanceSpec, error)
	ListInstances() ([]types.InstanceSpec, error)
	UpdateInstance(inst types.InstanceSpec) error
	RenameInstance(nameOrID string, renamed types.InstanceSpec) error
	DeleteInstance(nameOrID string) error
	// InstanceDir is the persistent directory holding an instance's
	// definition and overlay disk.
	InstanceDir(name string) string

	GetNetwork(nameOrID string) (types.Network, error)
	ListNetworks() ([]types.Network, error)
	GetKernel(nameOrID string) (types.Kernel, error)
	GetVolume(nameOrID string) (types.Volume, error)
}

// Addresses assigns and releases guest addresses.
type Addresses interface {
	Allocate(n types.Network, instanceID, staticIP string) (types.NetworkAllocation, error)
	Get(networkName, instanceID string) (types.NetworkAllocation, error)
	Release(networkName, instanceID string) error
	Reconcile(networks []string, live map[string]struct{}) (int, error)
}

// Images provides the bootable disk for an image reference.
type Images interface {
	Get(ref string) (*types.Image, error)
	Pull(ctx context.Context, ref string, onProgress image.ProgressFunc) (*types.Image, error)
}

// Kernels provides the local path of a kernel, fetching it if needed.
type Kernels interface {
	Path(ctx context.Context, k types.Kernel) (string, error)
}

// Volumes locates the disk backing a volume.
type Volumes interface {
	Path(id string) string
}

// Initrds provides the initramfs guests boot from.
type Initrds interface {
	Prepare(ctx context.Context) (string, error)
}

// HostNetwork attaches an instance to its network on this host.
type HostNetwork interface {
	SetupBridge(ctx context.Context, nw *types.Network) error
	CreateTAP(ctx context.Context, nw *types.Network, alloc *types.NetworkAllocation, bw network.Bandwidth) error
	RemoveTAP(ctx context.Context, nw *types.Network, instanceID string)
	TeardownBridge(ctx context.Context, nw *types.Network)

	// PublishPorts forwards host ports to the instance's address, replacing
	// any it already published.
	PublishPorts(ctx context.Context, nw *types.Network, alloc *types.NetworkAllocation, ports []types.PortMapping) error
	// UnpublishPorts removes every port the instance published.
	UnpublishPorts(ctx context.Context, instanceID string)
}

// Config holds the dependencies for a Manager.
type Config struct {
	Definitions Definitions
	Addresses   Addresses

	// RunDir holds ephemeral runtime state. Defaults to defaults.RunDir.
	RunDir string

	Images      Images
	Kernels     Kernels
	Volumes     Volumes
	Initrds     Initrds
	HostNetwork HostNetwork
	Starters    map[types.HypervisorType][]hypervisor.Starter

	// Capacity limits the CPU and memory instances may be given. The zero
	// value is unlimited.
	Capacity types.Capacity

	// Metrics, Events and Logger are optional.
	Metrics Metrics
	Events  Events
	Logger  *slog.Logger
}

// Manager drives instance lifecycle operations and owns the runtime state
// that describes them.
type Manager struct {
	definitions Definitions
	addresses   Addresses
	runDir      string
	images      Images
	kernels     Kernels
	volumes     Volumes
	initrds     Initrds
	hostNetwork HostNetwork
	starters    map[types.HypervisorType][]hypervisor.Starter
	capacity    types.Capacity
	metrics     Metrics
	events      Events
	logger      *slog.Logger

	// Seams replaced by tests.
	provisionConfigDisk func(ctx context.Context, path string, cfg *guest.Config) error
	attach              func(pid int, arg string) (*process.Process, error)
	probe               func(ctx context.Context, vsockPath string, check types.HealthCheck) (probeResult, error)
	shutdownGuest       func(ctx context.Context, vsockPath string) error
	restartWait         func(at time.Time) time.Duration

	// shutdownTimeout is how long a VMM asked to exit has before it is
	// killed.
	shutdownTimeout time.Duration

	// stopGracePeriod is how long a guest asked to shut down has to do it.
	stopGracePeriod time.Duration

	// admissionMu serialises admission. See resources.go.
	admissionMu sync.Mutex

	// locks holds a mutex per instance ID. Locks are never removed, since
	// an operation may still be waiting on one.
	locks sync.Map

	// networkLocks holds a mutex per network name, serialising bridge
	// setup and teardown.
	networkLocks sync.Map

	// vmms supervises each active instance's VMM, by instance ID. Entries
	// change only under that instance's lock.
	vmmsMu sync.Mutex
	vmms   map[string]*supervised

	// restarts holds each Restarting instance's pending restart, by
	// instance ID; restarting counts restarts under way.
	restartsMu sync.Mutex
	restarts   map[string]*pendingRestart
	restarting sync.WaitGroup

	// closing is closed by Close to stop watchers and pending restarts.
	closing   chan struct{}
	closeOnce sync.Once
	watchers  sync.WaitGroup
}

const (
	defaultShutdownTimeout = 5 * time.Second
	defaultStopGracePeriod = 10 * time.Second
)

// NewManager creates a Manager.
func NewManager(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RunDir == "" {
		cfg.RunDir = defaults.RunDir
	}
	if cfg.Metrics == nil {
		cfg.Metrics = discardMetrics{}
	}
	if cfg.Events == nil {
		cfg.Events = discardEvents{}
	}

	return &Manager{
		definitions: cfg.Definitions,
		addresses:   cfg.Addresses,
		runDir:      cfg.RunDir,
		images:      cfg.Images,
		kernels:     cfg.Kernels,
		volumes:     cfg.Volumes,
		initrds:     cfg.Initrds,
		hostNetwork: cfg.HostNetwork,
		starters:    cfg.Starters,
		capacity:    cfg.Capacity,
		metrics:     cfg.Metrics,
		events:      cfg.Events,
		logger:      cfg.Logger.With("component", "vm"),

		provisionConfigDisk: provisionConfigDisk,
		attach:              process.Attach,
		probe:               probeGuest,

		shutdownTimeout: defaultShutdownTimeout,
		stopGracePeriod: defaultStopGracePeriod,
		shutdownGuest:   shutdownGuest,
		restartWait:     time.Until,

		vmms:     make(map[string]*supervised),
		restarts: make(map[string]*pendingRestart),
		closing:  make(chan struct{}),
	}
}

// lock returns the mutex that serialises operations on one instance.
func (m *Manager) lock(id string) *sync.Mutex {
	return mutexIn(&m.locks, id)
}

// networkLock returns the mutex that serialises changes to one network's
// bridge.
func (m *Manager) networkLock(name string) *sync.Mutex {
	return mutexIn(&m.networkLocks, name)
}

// mutexIn returns the mutex locks holds for key, adding one if there is none.
func mutexIn(locks *sync.Map, key string) *sync.Mutex {
	v, _ := locks.LoadOrStore(key, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		panic(fmt.Sprintf("lock for %q has type %T", key, v))
	}
	return mu
}
