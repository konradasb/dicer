// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package guest defines the contract between the host and a guest: the
// Config written to the config disk, the Status read from the status disk,
// and the agent's port.
package guest

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/konradasb/dicer/internal/types"
)

// AgentPort is the vsock port dicer-agent listens on inside the guest.
const AgentPort = 2222

// hostnameRe is a regex for validating hostnames according to RFC 1123.
var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// defaultInit is what a guest boots when neither the image nor the instance
// names a command: the init system, as a machine would.
const defaultInit = "/sbin/init"

// Argv is the command the workload runs: the entrypoint and its arguments,
// or the machine's init if there is none.
func (c *Config) Argv() []string {
	argv := append(append([]string{}, c.Entrypoint...), c.Cmd...)
	if len(argv) == 0 {
		return []string{defaultInit}
	}
	return argv
}

// Halt is how dicer-init ends the virtual machine: whichever way makes the
// hypervisor it runs on end its VMM, so that the host sees the guest go.
type Halt string

// The ways a guest can end its virtual machine.
const (
	// HaltPowerOff powers the machine off, which ends a VMM that emulates a
	// power button.
	HaltPowerOff Halt = "poweroff"
	// HaltReset resets the machine, which ends a VMM that does not reboot
	// guests.
	HaltReset Halt = "reset"
)

// ConfigFile is the name of the file on the config disk that holds Config.
const ConfigFile = "config.json"

// Config is the configuration passed to the guest init binary via ConfigFile.
// It is serialized by the host (internal/vm) and deserialized by the guest
// init binary (internal/guest/boot).
type Config struct {
	Entrypoint        []string          `json:"entrypoint"`
	Cmd               []string          `json:"cmd"`
	Workdir           string            `json:"workdir"`
	Env               map[string]string `json:"env"`
	Hostname          string            `json:"hostname,omitempty"`
	Mode              types.InitMode    `json:"init_mode"`
	Mounts            []Mount           `json:"mounts,omitempty"`
	Network           NetworkConfig     `json:"network,omitzero"`
	SkipKernelHeaders bool              `json:"skip_kernel_headers,omitempty"`

	// StatusDevice is the raw disk dicer-init reports how the guest ended
	// on. See Status.
	StatusDevice string `json:"status_device,omitempty"`
	// Halt is how dicer-init ends the machine once the workload has exited.
	Halt Halt `json:"halt,omitempty"`
}

// Mount is one entry of the guest's mount table: what to mount at Target.
// Exactly one of Volume, File and Tmpfs is set.
type Mount struct {
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`

	Volume *VolumeSource `json:"volume,omitempty"`
	File   *FileSource   `json:"file,omitempty"`
	Tmpfs  *TmpfsSource  `json:"tmpfs,omitempty"`
}

// VolumeSource is a filesystem on a disk the VMM attached.
type VolumeSource struct {
	Device string `json:"device"`
	Fstype string `json:"fstype,omitempty"` // defaults to "ext4"
}

// FileSource is a file's contents, with the permissions and owner it had on
// the host.
type FileSource struct {
	Data []byte `json:"data"` // base64-encoded in JSON
	Mode uint32 `json:"mode"` // permission bits
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
}

// TmpfsSource is an empty in-memory filesystem.
type TmpfsSource struct{}

// NetworkConfig holds guest network configuration: interfaces, routes, and DNS.
type NetworkConfig struct {
	Interfaces []NetworkInterface `json:"interfaces,omitempty"`
	Routes     []NetworkRoute     `json:"routes,omitempty"`
	DNS        DNSConfig          `json:"dns,omitzero"`
}

// DNSConfig holds DNS resolver settings for the guest.
type DNSConfig struct {
	Nameservers   []string `json:"nameservers,omitempty"`
	SearchDomains []string `json:"search_domains,omitempty"`
}

// NetworkInterface describes a single guest network interface.
type NetworkInterface struct {
	Interface string   `json:"interface,omitempty"`
	Addresses []string `json:"addresses,omitempty"` // CIDR notation, e.g. "192.168.1.1/24"
	MTU       int      `json:"mtu,omitempty"`
}

// NetworkRoute describes a routing table entry for the guest.
type NetworkRoute struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	Dev         string `json:"dev,omitempty"`
	Table       string `json:"table,omitempty"`
}

// ApplyDefaults fills in zero-value fields with sensible defaults.
// Call this before Validate when deserializing config from JSON.
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = types.ModeAuto
	}
	if c.Env == nil {
		c.Env = make(map[string]string)
	}
	for _, m := range c.Mounts {
		if m.Volume != nil && m.Volume.Fstype == "" {
			m.Volume.Fstype = "ext4"
		}
	}
}

// Validate checks the configuration for correctness. Call ApplyDefaults first
// when deserializing from JSON.
func (c *Config) Validate() error {
	if err := c.validateProcess(); err != nil {
		return err
	}
	if err := ValidateHostname(c.Hostname); err != nil {
		return err
	}
	for i, m := range c.Mounts {
		if err := m.validate(); err != nil {
			return fmt.Errorf("mounts[%d]: %w", i, err)
		}
	}
	switch c.Halt {
	case "", HaltPowerOff, HaltReset:
	default:
		return fmt.Errorf("invalid halt %q", c.Halt)
	}
	return c.Network.validate()
}

// validateProcess checks the init mode and what it runs.
func (c *Config) validateProcess() error {
	switch c.Mode {
	case types.ModeExec:
		if len(c.Entrypoint) == 0 && len(c.Cmd) == 0 {
			return errors.New("exec mode requires at least one of entrypoint or cmd")
		}
	case types.ModeAuto, types.ModeSystemd:
	default:
		return fmt.Errorf("invalid init mode %q", c.Mode)
	}

	if c.Workdir != "" && c.Workdir[0] != '/' {
		return fmt.Errorf("workdir %q must be absolute", c.Workdir)
	}
	return nil
}

// ValidateHostname checks a hostname, if one is set, against RFC 1123.
func ValidateHostname(name string) error {
	switch {
	case name == "":
		return nil
	case len(name) > 253:
		return fmt.Errorf("hostname %q exceeds 253 characters", name)
	case !hostnameRe.MatchString(name):
		return fmt.Errorf("hostname %q is invalid", name)
	}
	return nil
}

func (m Mount) validate() error {
	switch {
	case m.Target == "":
		return errors.New("target not set")
	case m.Target[0] != '/':
		return fmt.Errorf("target %q must be absolute", m.Target)
	}

	sources := 0
	for _, set := range []bool{m.Volume != nil, m.File != nil, m.Tmpfs != nil} {
		if set {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("%s: want exactly one of volume, file and tmpfs, got %d", m.Target, sources)
	}

	if m.Volume != nil && m.Volume.Device == "" {
		return fmt.Errorf("%s: volume device not set", m.Target)
	}
	return nil
}

func (n NetworkConfig) validate() error {
	for i, iface := range n.Interfaces {
		switch {
		case iface.Interface == "":
			return fmt.Errorf("network.interfaces[%d]: interface name not set", i)
		case len(iface.Addresses) == 0:
			return fmt.Errorf("network.interfaces[%d]: at least one address required", i)
		}
	}
	for i, r := range n.Routes {
		if r.Destination == "" {
			return fmt.Errorf("network.routes[%d]: destination not set", i)
		}
	}
	return nil
}
