// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"errors"
	"io"
	"time"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// HostInfo is what a daemon is: its version, the hypervisors it carries, and
// how it is reached.
type HostInfo struct {
	// Version is the daemon's version.
	Version string `json:"version,omitzero"`

	// Hostname is the host's name.
	Hostname string `json:"hostname,omitzero"`

	// Hypervisors are the hypervisors the daemon can start instances with.
	Hypervisors []HypervisorInfo `json:"hypervisors,omitzero"`

	// ListenerAddresses are the addresses clients reach the daemon's TCP
	// listener at. For a listener on all of the host's addresses, they are
	// the host's own, with the default route's first. It is empty if the API
	// is not served over TCP.
	ListenerAddresses []string `json:"listener_addresses,omitzero"`

	// Fingerprint is the fingerprint of the certificate the API is served
	// with over TCP: the SHA-256 of its public key, hex-encoded. It is empty
	// if the API is not served over TCP.
	Fingerprint string `json:"fingerprint,omitzero"`

	// Token is the name of the token the call was made with. It is empty
	// over the socket.
	Token string `json:"token,omitzero"`
}

// HypervisorInfo is a hypervisor the daemon carries, and the versions of it
// an instance may name.
type HypervisorInfo struct {
	// Type is the hypervisor.
	Type HypervisorType `json:"type,omitzero"`

	// Versions are the versions available, the one an instance gets by
	// default first.
	Versions []string `json:"versions,omitzero"`

	// IsDefault reports whether this is the hypervisor an instance gets when
	// it names none.
	IsDefault bool `json:"is_default,omitzero"`

	// DeprecatedVersions are the versions that are deprecated: every one but
	// the default. Each is kept only for what still uses it, and a later
	// release of Dicer removes it.
	DeprecatedVersions []string `json:"deprecated_versions,omitzero"`
}

// Resources are how much CPU and memory instances may be given, how much is
// committed to them, and how full the data directory's disk is.
type Resources struct {
	// CPU is how the host's CPUs are shared out, counted in vCPUs.
	CPU ResourceCapacity `json:"cpu,omitzero"`

	// Memory is how the host's memory is shared out, in bytes.
	Memory ResourceCapacity `json:"memory,omitzero"`

	// Disk is the filesystem holding the data directory. Its use is
	// reported, not enforced.
	Disk DiskUsage `json:"disk,omitzero"`

	// Instances are the instances CPU and memory are committed to: those
	// starting, running or paused, in name order.
	Instances []InstanceResources `json:"instances,omitzero"`
}

// ResourceCapacity is how much of one resource instances may be given, and
// how much is committed to them. A start that would take Allocated past
// Allocatable is refused.
type ResourceCapacity struct {
	// Host is what the host has: its logical CPUs, or its memory.
	Host int64 `json:"host,omitzero"`

	// Reserved is what is kept back from instances for the host itself.
	Reserved int64 `json:"reserved,omitzero"`

	// Overcommit is how far what remains is stretched: 4 lets four vCPUs
	// share each CPU.
	Overcommit float64 `json:"overcommit,omitzero"`

	// Allocatable is what instances may be given in total:
	// (Host - Reserved) * Overcommit.
	Allocatable int64 `json:"allocatable,omitzero"`

	// Allocated is what is committed to instances.
	Allocated int64 `json:"allocated,omitzero"`

	// Available is what is left for further instances: Allocatable -
	// Allocated, or 0.
	Available int64 `json:"available,omitzero"`
}

// DiskUsage is how full the filesystem holding the data directory is, and
// how much has been promised on it. Disks are sparse, so ProvisionedBytes
// may be far more than TotalBytes without anything being wrong, until the
// guests fill them.
type DiskUsage struct {
	// Path is the data directory.
	Path string `json:"path,omitzero"`

	// TotalBytes is the filesystem's size.
	TotalBytes int64 `json:"total_bytes,omitzero"`

	// FreeBytes is what can still be written.
	FreeBytes int64 `json:"free_bytes,omitzero"`

	// ProvisionedBytes is the sizes of every instance's overlay disk and
	// every volume, added up.
	ProvisionedBytes int64 `json:"provisioned_bytes,omitzero"`
}

// InstanceResources are what is committed to one instance.
type InstanceResources struct {
	// Name is the instance's name.
	Name string `json:"name,omitzero"`

	// State is the instance's state: starting, running or paused.
	State InstanceState `json:"state,omitzero"`

	// VCPUs is how many vCPUs are committed to the instance.
	VCPUs int `json:"vcpus,omitzero"`

	// MemoryBytes is how much memory is committed to the instance.
	MemoryBytes int64 `json:"memory_bytes,omitzero"`
}

// HostInfo returns what the daemon is.
func (c *Client) HostInfo(ctx context.Context) (HostInfo, error) {
	resp, err := c.api.GetHostInfo(ctx, &dicerdv1.GetHostInfoRequest{})
	if err != nil {
		return HostInfo{}, fromStatus(err)
	}
	return hostInfoFromProto(resp), nil
}

// Resources returns how much of the host instances may be given, and how
// much is committed to them.
func (c *Client) Resources(ctx context.Context) (Resources, error) {
	resp, err := c.api.GetResources(ctx, &dicerdv1.GetResourcesRequest{})
	if err != nil {
		return Resources{}, fromStatus(err)
	}
	return resourcesFromProto(resp), nil
}

// CheckHostOptions say whether CheckHost boots a test instance, and how.
type CheckHostOptions struct {
	// TestInstance boots a test instance after the host is checked. It
	// creates and deletes an instance, pulls its image if the host does not
	// have it, and needs a token with instances:write.
	TestInstance bool

	// TestInstanceImage is the test instance's image: one with sh and wget,
	// such as busybox. Empty is docker.io/library/busybox:1.37.
	TestInstanceImage string

	// TestInstanceHypervisor is the hypervisor the test instance boots on.
	// Empty is cloud-hypervisor.
	TestInstanceHypervisor HypervisorType

	// TestInstanceHypervisorVersion is the version of the hypervisor the test
	// instance boots on. Empty is the hypervisor's default version, which
	// HostInfo lists first.
	TestInstanceHypervisorVersion string

	// TestInstanceTimeout is how long a test instance may take to run its
	// command. Zero is 2 minutes.
	TestInstanceTimeout time.Duration

	// KeepFailedTestInstance keeps a test instance that failed, to look into,
	// rather than deleting it.
	KeepFailedTestInstance bool
}

// HostCheckResult is what one of CheckHost's checks found.
type HostCheckResult struct {
	// Group is which part of the checks the result belongs to.
	Group HostCheckGroup `json:"group,omitzero"`

	// Name is what was checked. For the host it is kvm, ip_forwarding,
	// firewall, tools, uplink or disk. For the test instance it is its
	// hypervisor's type, or internet.
	Name string `json:"name,omitzero"`

	// Status is what the check found.
	Status HostCheckStatus `json:"status,omitzero"`

	// Detail is what was found, in a line, for a person.
	Detail string `json:"detail,omitzero"`

	// Hint is what to do about it. It is empty for a check that passed.
	Hint string `json:"hint,omitzero"`

	// Console is a failed test instance's last console lines, which say why it
	// failed.
	Console []string `json:"console,omitzero"`
}

// HostCheckGroup is which part of CheckHost's checks a result belongs to.
type HostCheckGroup string

// The host check groups, in the order their results come.
const (
	// HostCheckGroupHost holds the checks of the host itself.
	HostCheckGroupHost HostCheckGroup = "host"

	// HostCheckGroupInstances holds what the test instance showed, and
	// whether it reached the internet.
	HostCheckGroupInstances HostCheckGroup = "instances"
)

var hostCheckGroups = enum[HostCheckGroup, dicerdv1.HostCheckGroup]{"host check group", map[HostCheckGroup]dicerdv1.HostCheckGroup{
	HostCheckGroupHost:      dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_HOST,
	HostCheckGroupInstances: dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_INSTANCES,
}}

// HostCheckStatus is what a HostCheckResult found.
type HostCheckStatus string

// The host check statuses.
const (
	// HostCheckStatusOK means all is well.
	HostCheckStatusOK HostCheckStatus = "ok"

	// HostCheckStatusWarning means something may go wrong, such as little
	// free disk.
	HostCheckStatusWarning HostCheckStatus = "warning"

	// HostCheckStatusFailed means instances cannot boot, or cannot be
	// reached, until it is fixed.
	HostCheckStatusFailed HostCheckStatus = "failed"
)

var hostCheckStatuses = enum[HostCheckStatus, dicerdv1.HostCheckStatus]{"host check status", map[HostCheckStatus]dicerdv1.HostCheckStatus{
	HostCheckStatusOK:      dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_OK,
	HostCheckStatusWarning: dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_WARNING,
	HostCheckStatusFailed:  dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_FAILED,
}}

// CheckHost checks that the host can run instances and reach them: KVM,
// IPv4 forwarding, the firewall, the tools the daemon runs, its uplink and
// its free disk. Those checks change nothing. With opts.TestInstance, it
// then boots a small test instance, has it run a command and reach the
// internet, and deletes it. onResult, if not nil, is called with each
// result as it is found. CheckHost returns every result: the host's first,
// then the test instance's, then whether it reached the internet.
func (c *Client) CheckHost(
	ctx context.Context, opts CheckHostOptions, onResult func(HostCheckResult),
) ([]HostCheckResult, error) {
	req, err := checkHostRequest(opts)
	if err != nil {
		return nil, err
	}
	stream, err := c.api.CheckHost(ctx, req)
	if err != nil {
		return nil, fromStatus(err)
	}

	var results []HostCheckResult
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return results, nil
		}
		if err != nil {
			return nil, fromStatus(err)
		}

		r := hostCheckResultFromProto(msg)
		if onResult != nil {
			onResult(r)
		}
		results = append(results, r)
	}
}

// checkHostRequest returns the request for the checks opts asks for.
func checkHostRequest(opts CheckHostOptions) (*dicerdv1.CheckHostRequest, error) {
	hypervisorType, err := hypervisorTypes.toProto(opts.TestInstanceHypervisor)
	if err != nil {
		return nil, err
	}
	return &dicerdv1.CheckHostRequest{
		TestInstance:                  opts.TestInstance,
		TestInstanceImage:             opts.TestInstanceImage,
		TestInstanceHypervisor:        hypervisorType,
		TestInstanceHypervisorVersion: opts.TestInstanceHypervisorVersion,
		TestInstanceTimeout:           durationToProto(opts.TestInstanceTimeout),
		KeepFailedTestInstance:        opts.KeepFailedTestInstance,
	}, nil
}

// hostCheckResultFromProto returns the result p reports.
func hostCheckResultFromProto(p *dicerdv1.HostCheckResult) HostCheckResult {
	return HostCheckResult{
		Group:   hostCheckGroups.fromProto(p.GetGroup()),
		Name:    p.GetName(),
		Status:  hostCheckStatuses.fromProto(p.GetStatus()),
		Detail:  p.GetDetail(),
		Hint:    p.GetHint(),
		Console: p.GetConsole(),
	}
}

// hostInfoFromProto returns what p says the daemon is.
func hostInfoFromProto(p *dicerdv1.GetHostInfoResponse) HostInfo {
	return HostInfo{
		Version:           p.GetVersion(),
		Hostname:          p.GetHostname(),
		Hypervisors:       convertAll(p.GetHypervisors(), hypervisorInfoFromProto),
		ListenerAddresses: p.GetListenerAddresses(),
		Fingerprint:       p.GetFingerprint(),
		Token:             p.GetToken(),
	}
}

// hypervisorInfoFromProto returns the hypervisor p describes.
func hypervisorInfoFromProto(p *dicerdv1.HypervisorInfo) HypervisorInfo {
	return HypervisorInfo{
		Type:               hypervisorTypes.fromProto(p.GetType()),
		Versions:           p.GetVersions(),
		IsDefault:          p.GetIsDefault(),
		DeprecatedVersions: p.GetDeprecatedVersions(),
	}
}

// resourcesFromProto returns the resources p reports.
func resourcesFromProto(p *dicerdv1.GetResourcesResponse) Resources {
	return Resources{
		CPU:       resourceCapacityFromProto(p.GetCpu()),
		Memory:    resourceCapacityFromProto(p.GetMemory()),
		Disk:      diskUsageFromProto(p.GetDisk()),
		Instances: convertAll(p.GetInstances(), instanceResourcesFromProto),
	}
}

// diskUsageFromProto returns the disk usage p reports.
func diskUsageFromProto(p *dicerdv1.DiskUsage) DiskUsage {
	return DiskUsage{
		Path:             p.GetPath(),
		TotalBytes:       p.GetTotalBytes(),
		FreeBytes:        p.GetFreeBytes(),
		ProvisionedBytes: p.GetProvisionedBytes(),
	}
}

// resourceCapacityFromProto returns the capacity p reports.
func resourceCapacityFromProto(p *dicerdv1.ResourceCapacity) ResourceCapacity {
	return ResourceCapacity{
		Host:        p.GetHost(),
		Reserved:    p.GetReserved(),
		Overcommit:  p.GetOvercommit(),
		Allocatable: p.GetAllocatable(),
		Allocated:   p.GetAllocated(),
		Available:   p.GetAvailable(),
	}
}

// instanceResourcesFromProto returns what p says is committed to an
// instance.
func instanceResourcesFromProto(p *dicerdv1.InstanceResources) InstanceResources {
	return InstanceResources{
		Name:        p.GetName(),
		State:       instanceStates.fromProto(p.GetState()),
		VCPUs:       int(p.GetVcpus()),
		MemoryBytes: p.GetMemoryBytes(),
	}
}
