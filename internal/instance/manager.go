// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package instance runs instances as virtual machines and manages their
// lifecycle: starting, stopping, pausing and deleting them.
//
// Operations are synchronous; a failed one unwinds what it allocated. Every
// host resource an instance holds is derived from its ID, so recovery after an
// unclean shutdown needs no journal. See recover.go.
package instance

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/prometheus/procfs"

	"github.com/konradasb/dicer/internal/defaults"
	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/health"
	"github.com/konradasb/dicer/internal/hostfs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/virtiofs"
	"github.com/konradasb/dicer/internal/volume"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// Store keeps the definitions of instances, snapshots, networks, volumes and
// kernels.
type Store interface {
	CreateInstance(instance Spec) error
	Instance(nameOrID string) (Spec, error)
	Instances() []Spec
	MatchingInstances(match func(Spec) bool) []Spec
	UpdateInstance(instance Spec) error
	RenameInstance(nameOrID string, renamed Spec) error
	DeleteInstance(nameOrID string) error
	// InstanceDir is the persistent directory holding an instance's
	// definition and overlay disk.
	InstanceDir(name string) string

	// StageSnapshot returns an empty directory to write a snapshot's files
	// in, which CreateSnapshot moves into place.
	StageSnapshot() (string, error)
	CreateSnapshot(snapshot Snapshot, staged string) error
	Snapshot(nameOrID string) (Snapshot, error)
	Snapshots() []Snapshot
	DeleteSnapshot(nameOrID string) error
	// SnapshotDir is the directory holding a snapshot's files.
	SnapshotDir(name string) string

	Network(nameOrID string) (network.Network, error)
	Networks() []network.Network
	Kernel(nameOrID string) (kernel.Kernel, error)
	Volume(nameOrID string) (volume.Volume, error)
}

// Networks assigns and releases guest addresses.
type Networks interface {
	Allocate(n network.Network, instanceID, staticIP string) (network.Allocation, error)
	Allocation(networkName, instanceID string) (network.Allocation, error)
	InstanceAt(networkName, ip string) (instanceID string, ok bool)
	Release(networkName, instanceID string) error
	Reconcile(networks []string, live map[string]struct{}) (int, error)
}

// Images provides the bootable disk for an image reference.
type Images interface {
	Image(ref string) (*image.Image, error)
	Ensure(ctx context.Context, ref string, policy image.PullPolicy) (*image.Image, error)
}

// Kernels finds a kernel's binary on the host.
type Kernels interface {
	Path(k kernel.Kernel) (string, error)
}

// Volumes locates the disk backing a volume.
type Volumes interface {
	Path(v volume.Volume) string
}

// Initrds provides the initramfs guests boot from.
type Initrds interface {
	Prepare(ctx context.Context) (string, error)
}

// HostNetwork attaches an instance to its network on this host.
type HostNetwork interface {
	SetupBridge(ctx context.Context, nw *network.Network) error
	CreateTAP(ctx context.Context, nw *network.Network, allocation *network.Allocation, bandwidth network.Bandwidth) error
	RemoveTAP(ctx context.Context, nw *network.Network, instanceID string)
	// DisconnectTAP detaches an instance's TAP device from the bridge, and
	// ConnectTAP attaches it again.
	DisconnectTAP(ctx context.Context, nw *network.Network, instanceID string) error
	ConnectTAP(ctx context.Context, nw *network.Network, instanceID string) error
	TeardownBridge(ctx context.Context, nw *network.Network)

	// PublishPorts forwards host ports to the instance's address, replacing
	// any it already published.
	PublishPorts(ctx context.Context, nw *network.Network, allocation *network.Allocation, ports []network.PortMapping) error
	// UnpublishPorts removes every port the instance published.
	UnpublishPorts(ctx context.Context, instanceID string)
}

// DNSServers answer the DNS queries of each network's guests, on the
// network's gateway address, which is the nameserver they are given while
// it is served.
type DNSServers interface {
	// Serve starts serving a network, unless it already is. The network's
	// bridge must be up.
	Serve(ctx context.Context, nw network.Network) error
	// Stop stops serving a network.
	Stop(network string)
}

// Shares shares host directories with guests: it starts virtiofsd for a
// share, on a socket the VMM connects to. The process ends when the VMM
// does.
type Shares interface {
	Start(ctx context.Context, s virtiofs.Share) (*process.Process, error)
}

// Config holds the dependencies for a Manager.
type Config struct {
	Store    Store
	Networks Networks

	// RunDir holds ephemeral runtime state. Defaults to defaults.RunDir.
	RunDir string

	Images      Images
	Kernels     Kernels
	Volumes     Volumes
	Initrds     Initrds
	HostNetwork HostNetwork
	Starters    map[hypervisor.Type][]hypervisor.Starter

	// DNSServers, if set, lets guests find each other by name. Without it,
	// they are given the network's upstream nameservers.
	DNSServers DNSServers
	// Shares, if set, lets instances mount host directories. Without it,
	// an instance that mounts one cannot start.
	Shares Shares
	// AllowedDirectories are the host directories instances may mount. An
	// instance that mounts any other cannot start.
	AllowedDirectories hostfs.AllowedDirectories

	// Capacity limits the CPU and memory instances may be given. The zero
	// value is unlimited.
	Capacity Capacity

	// Events and Logger are optional.
	Events Recorder
	Logger *slog.Logger
}

// Manager drives instance lifecycle operations and owns the status that
// describes them.
type Manager struct {
	store       Store
	networks    Networks
	runDir      string
	images      Images
	kernels     Kernels
	volumes     Volumes
	initrds     Initrds
	hostNetwork HostNetwork
	starters    map[hypervisor.Type][]hypervisor.Starter
	dnsServers  DNSServers
	shares      Shares
	capacity    Capacity
	metrics     metrics
	events      Recorder
	logger      *slog.Logger

	allowedDirectories hostfs.AllowedDirectories

	// procDir is where procfs is mounted, from which stats are read.
	procDir string

	// Seams replaced by tests.
	provisionConfigDisk func(ctx context.Context, path string, cfg *guest.Config) error
	attach              func(pid int, arg string) (*process.Process, error)
	probe               func(ctx context.Context, vsockPath string, check health.Check) (health.Result, error)
	shutdownGuest       func(ctx context.Context, vsockPath string) error
	setGuestClock       func(ctx context.Context, vsockPath string, t time.Time) error
	setGuestIdentity    func(ctx context.Context, vsockPath string, req *diceragentv1.SetIdentityRequest) error
	restartWait         func(at time.Time) time.Duration
	dialGuest           func(ctx context.Context, address string) (net.Conn, error)
	awaitAgent          func(ctx context.Context, vsockPath string) error

	// shutdownTimeout is how long a VMM asked to exit has before it is
	// killed.
	shutdownTimeout time.Duration

	// stopGracePeriod is how long a guest asked to shut down has to do it.
	stopGracePeriod time.Duration

	// bootTimeout is how long a guest has to boot. See boot_watch.go.
	bootTimeout time.Duration

	// admissionMu serialises admission. See admission.go.
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

	// wakers listen on the ports of instances on standby to wake them, by
	// instance ID. Entries change only under that instance's lock. See
	// wake.go.
	wakersMu sync.Mutex
	wakers   map[string]*waker

	// waiters are those waiting for each instance to stop, by instance ID.
	// See wait.go.
	waitersMu sync.Mutex
	waiters   map[string]map[*Waiter]struct{}

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
	if cfg.Events == nil {
		cfg.Events = discardRecorder{}
	}

	return &Manager{
		store:       cfg.Store,
		networks:    cfg.Networks,
		runDir:      cfg.RunDir,
		images:      cfg.Images,
		kernels:     cfg.Kernels,
		volumes:     cfg.Volumes,
		initrds:     cfg.Initrds,
		hostNetwork: cfg.HostNetwork,
		starters:    cfg.Starters,
		dnsServers:  cfg.DNSServers,
		shares:      cfg.Shares,
		capacity:    cfg.Capacity,
		metrics:     newMetrics(),
		events:      cfg.Events,
		logger:      cfg.Logger.With("component", "instance"),
		procDir:     procfs.DefaultMountPoint,

		allowedDirectories: cfg.AllowedDirectories,

		provisionConfigDisk: provisionConfigDisk,
		attach:              process.Attach,
		probe:               probeGuest,

		shutdownTimeout:  defaultShutdownTimeout,
		stopGracePeriod:  defaultStopGracePeriod,
		bootTimeout:      defaultBootTimeout,
		awaitAgent:       awaitAgent,
		shutdownGuest:    shutdownGuest,
		setGuestClock:    setGuestClock,
		setGuestIdentity: setGuestIdentity,
		restartWait:      time.Until,
		dialGuest:        dialGuest,

		vmms:     make(map[string]*supervised),
		wakers:   make(map[string]*waker),
		waiters:  make(map[string]map[*Waiter]struct{}),
		restarts: make(map[string]*pendingRestart),
		closing:  make(chan struct{}),
	}
}

// Instance returns an instance's definition by name or ID.
func (m *Manager) Instance(nameOrID string) (Spec, error) {
	return m.store.Instance(nameOrID)
}

// Instances returns every instance's definition, sorted by name.
func (m *Manager) Instances() []Spec {
	return m.store.Instances()
}

// lock returns the mutex that serialises operations on one instance.
func (m *Manager) lock(id string) *sync.Mutex {
	return mutexIn(&m.locks, id)
}

// rereadDefinition replaces *instance with its definition as it is now. An
// operation calls it once it holds the instance's lock, so that it acts on
// that definition, not on one its caller read before taking the lock.
func (m *Manager) rereadDefinition(instance *Spec) error {
	current, err := m.store.Instance(instance.ID)
	if err != nil {
		return err
	}
	*instance = current
	return nil
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
