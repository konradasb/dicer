// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package doctor checks that a host can run Dicer's instances and reach
// them, for dicer doctor. It checks the host first: KVM, IPv4 forwarding,
// the firewall, the tools the daemon runs, its uplink and its free disk.
// Those checks only read, and change nothing. It can then boot a small test
// instance, have it run a command and reach the internet, and delete it.
package doctor

import (
	"cmp"
	"context"
	"iter"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/instance"
)

// Status is what a check found.
type Status string

// The statuses.
const (
	// StatusOK means all is well.
	StatusOK Status = "ok"

	// StatusWarning means something may go wrong, such as little free disk.
	StatusWarning Status = "warning"

	// StatusFailed means instances cannot boot, or cannot be reached, until
	// it is fixed.
	StatusFailed Status = "failed"
)

// Group is which part of the doctor's work a result belongs to.
type Group string

// The groups, in the order their results come.
const (
	// GroupHost holds the checks of the host itself.
	GroupHost Group = "host"

	// GroupInstances holds what the test instance showed, and whether it
	// reached the internet.
	GroupInstances Group = "instances"
)

// The test instance's defaults: small, and quick to pull and boot.
const (
	// DefaultTestInstanceImage is the test instance's image when Options names
	// none.
	DefaultTestInstanceImage = "docker.io/library/busybox:1.37"

	// DefaultTestInstanceTimeout is how long a test instance may take to run
	// its command when Options gives no time.
	DefaultTestInstanceTimeout = 2 * time.Minute
)

// Result is what one check found.
type Result struct {
	// Group is which part of the doctor's work the result belongs to.
	Group Group

	// Name is what was checked. For the host it is kvm, ip_forwarding,
	// firewall, tools, uplink or disk. For the test instance it is its
	// hypervisor's type, or internet.
	Name string

	// Status is what the check found.
	Status Status

	// Detail is what was found, in a line.
	Detail string

	// Hint is what to do about it. It is empty for a check that passed.
	Hint string

	// Console is a failed test instance's last console lines, which say why it
	// failed.
	Console []string
}

// Options say whether Check boots a test instance, and how.
type Options struct {
	// TestInstance boots a test instance after the host is checked. It
	// creates and deletes an instance, and pulls its image if the host does
	// not have it.
	TestInstance bool

	// TestInstanceImage is the test instance's image: one with sh and wget,
	// such as busybox. Empty is DefaultTestInstanceImage.
	TestInstanceImage string

	// TestInstanceHypervisor is the hypervisor the test instance boots on.
	// Empty is hypervisor.DefaultType.
	TestInstanceHypervisor hypervisor.Type

	// TestInstanceHypervisorVersion is the version of the hypervisor the test
	// instance boots on. Empty is the hypervisor's default version.
	TestInstanceHypervisorVersion string

	// TestInstanceTimeout is how long a test instance may take to run its
	// command. Zero is DefaultTestInstanceTimeout.
	TestInstanceTimeout time.Duration

	// KeepFailedTestInstance keeps a test instance that failed, to look into,
	// rather than deleting it.
	KeepFailedTestInstance bool
}

// Validate returns an errdefs.ErrInvalidArgument error if the test instance's
// image is not an image reference, its hypervisor is not one Dicer knows, or
// its timeout is negative.
func (o Options) Validate() error {
	if o.TestInstanceImage != "" {
		if _, err := reference.Parse(o.TestInstanceImage); err != nil {
			return errdefs.InvalidArgument("invalid test instance image %q: %v", o.TestInstanceImage, err)
		}
	}
	if o.TestInstanceHypervisor != "" && !o.TestInstanceHypervisor.Valid() {
		return errdefs.InvalidArgument("unknown test instance hypervisor %q: give cloud-hypervisor or firecracker",
			o.TestInstanceHypervisor)
	}
	if o.TestInstanceTimeout < 0 {
		return errdefs.InvalidArgument("test instance timeout %s is negative: give 0 for the default", o.TestInstanceTimeout)
	}
	return nil
}

// Config is what the checks need to know of the daemon.
type Config struct {
	// DataDir is the daemon's data directory, whose disk is checked.
	DataDir string

	// UplinkInterface is network.uplink_interface, or empty to find the
	// uplink by the default route.
	UplinkInterface string

	// InstanceManager creates, boots and deletes the test instance.
	InstanceManager *instance.Manager
}

// Doctor checks the host, and boots a test instance on it.
type Doctor struct {
	cfg Config

	// What the checks read and run, which tests replace.
	kvmPath       string
	ipForwardPath string
	routesPath    string
	goarch        string
	readFile      func(path string) ([]byte, error)
	openRDWR      func(path string) error
	run           func(ctx context.Context, name string, args ...string) (string, error)
	lookPath      func(file string) (string, error)
	freeBytes     func(path string) (int64, error)
	interfaces    func() ([]string, error)
	instances     instances
}

// New returns a Doctor of this host.
func New(cfg Config) *Doctor {
	d := &Doctor{
		cfg:           cfg,
		kvmPath:       "/dev/kvm",
		ipForwardPath: "/proc/sys/net/ipv4/ip_forward",
		routesPath:    "/proc/net/route",
		goarch:        runtime.GOARCH,
		readFile:      os.ReadFile,
		openRDWR:      openRDWR,
		run:           run,
		lookPath:      exec.LookPath,
		freeBytes:     freeBytes,
		interfaces:    interfaceNames,
	}
	if cfg.InstanceManager != nil {
		d.instances = managerInstances{cfg.InstanceManager}
	}

	return d
}

// Check checks the host, then boots a test instance if opts asks for one,
// and yields each result as it is found: the host's first, then the test
// instance's, then whether it reached the internet. opts must be valid.
func (d *Doctor) Check(ctx context.Context, opts Options) iter.Seq[Result] {
	opts.TestInstanceImage = cmp.Or(opts.TestInstanceImage, DefaultTestInstanceImage)
	opts.TestInstanceHypervisor = cmp.Or(opts.TestInstanceHypervisor, hypervisor.DefaultType)
	opts.TestInstanceTimeout = cmp.Or(opts.TestInstanceTimeout, DefaultTestInstanceTimeout)

	return func(yield func(Result) bool) {
		for _, r := range d.hostResults(ctx) {
			if !yield(r) {
				return
			}
		}
		if opts.TestInstance && d.instances != nil {
			d.testInstanceResults(ctx, opts, yield)
		}
	}
}
