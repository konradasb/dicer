// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package vm implements the virtual machine lifecycle: starting, stopping, pausing
// and deleting the virtual machines in the store.
//
// Operations are synchronous. There is no reconcile loop and no work queue,
// because there is nothing to reconcile against: the daemon owns the host, the
// RPC handler is the only writer, and a caller who wants to know whether a
// start succeeded is already waiting on the answer. A failed operation
// unwinds what it allocated and returns the error.
//
// Nothing is journalled. Every host resource an instance holds is derivable
// from its ID -- the TAP device from naming.TAPName, the disks and
// sockets from fixed paths under the runtime and instance directories -- so
// recovery after an unclean shutdown reconstructs the list rather than
// reading it back. See recover.go.
package vm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/guest"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	"github.com/dicer-sh/dicer/internal/image"
	"github.com/dicer-sh/dicer/internal/network"
	"github.com/dicer-sh/dicer/internal/process"
)

// The interfaces below are declared here, in the consumer, so that vm
// depends on what it needs rather than on the packages that happen to
// provide it -- and so tests can supply fakes. Each is named for what it
// provides, since most are implemented by a Manager of one kind or another.

// Definitions is what instances, networks, volumes and kernels are defined
// on this host.
type Definitions interface {
	CreateInstance(inst dicer.InstanceSpec) error
	GetInstance(nameOrID string) (dicer.InstanceSpec, error)
	ListInstances() ([]dicer.InstanceSpec, error)
	UpdateInstance(inst dicer.InstanceSpec) error
	RenameInstance(nameOrID string, renamed dicer.InstanceSpec) error
	DeleteInstance(nameOrID string) error
	// InstanceDir is the persistent directory holding an instance's
	// definition and overlay disk.
	InstanceDir(name string) string

	GetNetwork(nameOrID string) (dicer.Network, error)
	ListNetworks() ([]dicer.Network, error)
	GetKernel(nameOrID string) (dicer.Kernel, error)
	GetVolume(nameOrID string) (dicer.Volume, error)
}

// Addresses assigns and releases guest addresses. Address policy belongs to
// internal/network; this is only what the lifecycle needs of it.
type Addresses interface {
	Allocate(n dicer.Network, instanceID, staticIP string) (dicer.NetworkAllocation, error)
	Get(networkName, instanceID string) (dicer.NetworkAllocation, error)
	Release(networkName, instanceID string) error
	Reconcile(networks []string, live map[string]struct{}) (int, error)
}

// Images provides the bootable disk for an image reference: the one held on
// this host, or else a pull. Progress is of no interest here -- nothing is
// watching an instance start the way it watches a pull -- so it is always
// asked for without.
type Images interface {
	Get(ref string) (*dicer.Image, error)
	Pull(ctx context.Context, ref string, onProgress image.ProgressFunc) (*dicer.Image, error)
}

// Kernels provides the local path of a kernel, fetching it if needed.
type Kernels interface {
	Path(ctx context.Context, k dicer.Kernel) (string, error)
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
	SetupBridge(ctx context.Context, nw *dicer.Network) error
	CreateTAP(ctx context.Context, nw *dicer.Network, alloc *dicer.NetworkAllocation, bw network.Bandwidth) error
	RemoveTAP(ctx context.Context, nw *dicer.Network, instanceID string)
	TeardownBridge(ctx context.Context, nw *dicer.Network)

	// PublishPorts forwards host ports to the instance's address, replacing
	// any it already published.
	PublishPorts(ctx context.Context, nw *dicer.Network, alloc *dicer.NetworkAllocation, ports []dicer.PortMapping) error
	// UnpublishPorts removes every port the instance published. It is
	// derived from the instance ID alone, so it needs no record of what was
	// published.
	UnpublishPorts(ctx context.Context, instanceID string)
}

// Config holds the dependencies for a Manager.
type Config struct {
	// Definitions is what instances exist and how they are configured. The
	// Manager owns their runtime state itself.
	Definitions Definitions

	// Addresses assigns guest addresses.
	Addresses Addresses

	// RunDir holds ephemeral runtime state. Defaults to DefaultRunDir.
	RunDir string

	Images      Images
	Kernels     Kernels
	Volumes     Volumes
	Initrds     Initrds
	HostNetwork HostNetwork
	Starters    map[dicer.HypervisorType][]hypervisor.Starter

	// Capacity is how much CPU and memory instances may be given; starts
	// that would exceed it are refused. Optional: when unset, nothing is.
	Capacity dicer.Capacity

	// Metrics records lifecycle operations. Optional: when nil, they are
	// not recorded.
	Metrics Metrics

	// Events records what happens to instances. Optional: when nil, it is
	// not recorded.
	Events Events

	// Logger is where the Manager logs. Optional: defaults to slog.Default.
	Logger *slog.Logger
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
	starters    map[dicer.HypervisorType][]hypervisor.Starter
	capacity    dicer.Capacity
	metrics     Metrics
	events      Events
	logger      *slog.Logger

	// provisionConfigDisk builds the disk dicer-init reads its
	// configuration from. It is a field so that tests need no mke2fs.
	provisionConfigDisk func(ctx context.Context, path string, cfg *guest.Config) error

	// attach adopts a VMM left running by a previous daemon. It is a field
	// so that tests need no pidfds.
	attach func(pid int, arg string) (*process.Process, error)

	// probe runs a health check probe in a guest. It is a field so that
	// tests need no guest agent.
	probe func(ctx context.Context, vsockPath string, check dicer.HealthCheck) (probeResult, error)

	// shutdownTimeout is how long a VMM asked to exit is given before it is
	// killed. It is a field so that tests need not wait it out.
	shutdownTimeout time.Duration

	// stopGracePeriod is how long a guest asked to shut down is given to do
	// it, and shutdownGuest asks it. They are fields so that tests need no
	// guest agent, nor to wait the period out.
	stopGracePeriod time.Duration
	shutdownGuest   func(ctx context.Context, vsockPath string) error

	// restartWait is how long a restart due at a given time waits for it.
	// It is a field so that tests need not wait out a backoff.
	restartWait func(at time.Time) time.Duration

	// admissionMu serialises admission, so that what one start is admitted
	// on includes every start admitted before it. See resources.go.
	admissionMu sync.Mutex

	// locks serialises operations per instance. Concurrent RPCs for the same
	// instance would otherwise race on its TAP device and disks. A lock is
	// kept after its instance is deleted: IDs are never reused, and removing
	// it would let an operation waiting on it run beside a new one.
	locks sync.Map

	// networkLocks serialise bringing a network's bridge up against taking
	// it down, by network name. See setupNetwork and teardownNetwork.
	networkLocks sync.Map

	// vmms holds the supervision of every active instance's VMM, by
	// instance ID. An entry is only added or removed under that instance's
	// lock. See supervise.go.
	vmmsMu sync.Mutex
	vmms   map[string]*supervised

	// restarts holds the restart each Restarting instance is waiting on,
	// by instance ID, and restarting counts the restarts under way. See
	// supervise.go.
	restartsMu sync.Mutex
	restarts   map[string]*pendingRestart
	restarting sync.WaitGroup

	// closing is closed by Close to stop the watchers in watchers, and the
	// restarts in restarts.
	closing   chan struct{}
	closeOnce sync.Once
	watchers  sync.WaitGroup
}

// defaultShutdownTimeout is the Manager's shutdownTimeout.
const defaultShutdownTimeout = 5 * time.Second

// defaultStopGracePeriod is the Manager's stopGracePeriod: what docker stop
// gives a container.
const defaultStopGracePeriod = 10 * time.Second

// NewManager creates a Manager.
func NewManager(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RunDir == "" {
		cfg.RunDir = DefaultRunDir
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
		// Only mutexIn stores into these maps, so this cannot happen.
		panic(fmt.Sprintf("lock for %q has type %T", key, v))
	}
	return mu
}
