// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"fmt"

	"github.com/docker/go-units"
)

// Resources is an amount of the host's CPU and memory.
type Resources struct {
	VCPUs       int   `json:"vcpus"`
	MemoryBytes int64 `json:"memory_bytes"`
}

// Add returns r and o together.
func (r Resources) Add(o Resources) Resources {
	return Resources{VCPUs: r.VCPUs + o.VCPUs, MemoryBytes: r.MemoryBytes + o.MemoryBytes}
}

// Sub returns what is left of r once o is taken from it, which is nothing
// rather than less than nothing: overcommit can be lowered below what is
// already committed.
func (r Resources) Sub(o Resources) Resources {
	return Resources{
		VCPUs:       max(r.VCPUs-o.VCPUs, 0),
		MemoryBytes: max(r.MemoryBytes-o.MemoryBytes, 0),
	}
}

// Fits reports whether r is within limit in every resource.
func (r Resources) Fits(limit Resources) bool {
	return r.VCPUs <= limit.VCPUs && r.MemoryBytes <= limit.MemoryBytes
}

// String describes r for a person, e.g. "2 vCPU, 4 GiB".
func (r Resources) String() string {
	return fmt.Sprintf("%d vCPU, %s", r.VCPUs,
		units.CustomSize("%.4g %s", float64(r.MemoryBytes), 1024, binarySizeUnits))
}

// binarySizeUnits are the units String writes memory in: the binary ones
// that sizes are given in, as --memory 512MiB.
var binarySizeUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// Capacity is how much of the host instances may be given.
//
// vCPUs are overcommitted by default: a vCPU is a thread, and idle ones cost
// the host nothing. Memory is not, since there is nothing to take it back
// from a guest that uses it; some of it is reserved besides, for the host
// itself and for the VMMs' own overhead above their guests' memory.
type Capacity struct {
	// Host is what the host has: its logical CPUs and its memory.
	Host Resources `json:"host"`

	// ReservedMemoryBytes is withheld from instances altogether.
	ReservedMemoryBytes int64 `json:"reserved_memory_bytes"`

	// CPUOvercommit and MemoryOvercommit scale what the host has into what
	// instances may be given: 4 lets four vCPUs share each CPU.
	CPUOvercommit    float64 `json:"cpu_overcommit"`
	MemoryOvercommit float64 `json:"memory_overcommit"`
}

// Allocatable returns what instances may be given in total.
func (c Capacity) Allocatable() Resources {
	return Resources{
		VCPUs:       int(float64(c.Host.VCPUs) * c.CPUOvercommit),
		MemoryBytes: int64(float64(c.Host.MemoryBytes-c.ReservedMemoryBytes) * c.MemoryOvercommit),
	}
}

// Unlimited reports whether the capacity was left unset, in which case
// nothing is refused.
func (c Capacity) Unlimited() bool {
	return c == Capacity{}
}

// Usage is what the instances on a host hold of it.
//
// It is computed on demand from the specs and their status rather than kept
// as a running total, because the daemon is not the only thing that changes
// what is running: a VMM can die on its own, and recovery re-adopts whatever
// it finds. A total maintained alongside the truth would eventually disagree
// with it.
type Usage struct {
	// ByState counts instances per state, and names every state even when
	// no instance is in it.
	ByState map[InstanceState]int `json:"by_state"`

	// ByHealth counts the instances whose health is being checked, by what
	// their check has found, and names every verdict even when no instance
	// has it.
	ByHealth map[HealthStatus]int `json:"by_health"`

	// Capacity is what instances may be given, and how that was arrived at.
	Capacity Capacity `json:"capacity"`

	// Allocated is what the instances listed in Holders hold between them.
	Allocated Resources `json:"allocated"`

	// Holders are the instances holding resources, in name order.
	Holders []Holder `json:"holders"`
}

// Holder is an instance holding some of the host's resources.
type Holder struct {
	Name      string        `json:"name"`
	State     InstanceState `json:"state"`
	Resources Resources     `json:"resources"`
}

// Available returns what is left for further instances.
func (u Usage) Available() Resources {
	return u.Capacity.Allocatable().Sub(u.Allocated)
}

// DiskUsage is the size of a filesystem and how much of it is free.
type DiskUsage struct {
	// TotalBytes is the filesystem's size.
	TotalBytes int64 `json:"total_bytes"`

	// FreeBytes is what an unprivileged user could still write: blocks
	// reserved for root are not counted, since the daemon's files should not
	// be what eats into them.
	FreeBytes int64 `json:"free_bytes"`
}
