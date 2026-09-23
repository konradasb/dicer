// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package guest defines the contract between the host and a virtual machine.
//
// The host serialises a Config onto a small read-only disk; dicer-init reads
// it as PID 1 inside the guest and uses it to set up networking, mount
// volumes, install injected files and start the workload. Both sides depend
// on this package and nothing else, so the contract has exactly one
// definition.
package guest

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/dicer-sh/dicer"
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

// MountMode defines how a volume is mounted in the guest.
type MountMode string

// How a volume is mounted inside the guest.
const (
	MountModeRO MountMode = "ro" // read-only
	MountModeRW MountMode = "rw" // read-write
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
	Mode              dicer.InitMode    `json:"init_mode"`
	VolumeMounts      []VolumeMount     `json:"volume_mounts,omitempty"`
	Files             []FileMount       `json:"files,omitempty"`
	Network           NetworkConfig     `json:"network,omitzero"`
	SkipKernelHeaders bool              `json:"skip_kernel_headers,omitempty"`

	// StatusDevice is the raw disk dicer-init reports how the guest ended
	// on. See Status.
	StatusDevice string `json:"status_device,omitempty"`
	// Halt is how dicer-init ends the machine once the workload has exited.
	Halt Halt `json:"halt,omitempty"`
}

// VolumeMount represents a volume mount configuration.
type VolumeMount struct {
	Device string    `json:"device"`
	Path   string    `json:"path"`
	Mode   MountMode `json:"mode"`
	Fstype string    `json:"fstype,omitempty"` // defaults to "ext4"
}

// FileMount describes a file to be written into the VM at
// /run/secrets/<Name>, on a tmpfs, mode 0400.
//
// Value holds the file's contents. The daemon reads them from a host path each
// time the instance starts and transports them on the config disk; it stores
// no copy of its own. The /run/secrets location is the conventional one that
// images already look in, not evidence of a secret store.
type FileMount struct {
	Name  string `json:"name"`
	Value []byte `json:"value"` // base64-encoded in JSON
}

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
		c.Mode = dicer.ModeAuto
	}
	if c.Env == nil {
		c.Env = make(map[string]string)
	}
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Fstype == "" {
			c.VolumeMounts[i].Fstype = "ext4"
		}
		if c.VolumeMounts[i].Mode == "" {
			c.VolumeMounts[i].Mode = MountModeRW
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
	for i, v := range c.VolumeMounts {
		if err := v.validate(); err != nil {
			return fmt.Errorf("volume_mounts[%d]: %w", i, err)
		}
	}
	for i, f := range c.Files {
		if err := f.validate(); err != nil {
			return fmt.Errorf("files[%d]: %w", i, err)
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
	case dicer.ModeExec:
		if len(c.Entrypoint) == 0 && len(c.Cmd) == 0 {
			return errors.New("exec mode requires at least one of entrypoint or cmd")
		}
	case dicer.ModeAuto, dicer.ModeSystemd:
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

func (v VolumeMount) validate() error {
	switch {
	case v.Device == "":
		return errors.New("device not set")
	case v.Path == "":
		return errors.New("path not set")
	case v.Path[0] != '/':
		return fmt.Errorf("path %q must be absolute", v.Path)
	}

	switch v.Mode {
	case MountModeRO, MountModeRW:
	default:
		return fmt.Errorf("invalid mode %q: want ro or rw", v.Mode)
	}
	return nil
}

func (f FileMount) validate() error {
	switch {
	case f.Name == "":
		return errors.New("name not set")
	case len(f.Value) == 0:
		return errors.New("value not set")
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
