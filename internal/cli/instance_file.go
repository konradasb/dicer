// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Instance file: the YAML form of an instance definition, for
// 'dicer instance create -f vm.yaml'.

package cli

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/dicer-sh/dicer"
)

// instanceFile is the YAML form of an instance definition, for
// `dicer instance create -f vm.yaml`. It is the closest thing Dicer has to a
// template: a file you can keep in version control and apply to any host.
type instanceFile struct {
	Name              string            `yaml:"name"`
	Image             string            `yaml:"image"`
	Kernel            string            `yaml:"kernel"`
	KernelArgs        string            `yaml:"kernel_args,omitempty"`
	HypervisorType    string            `yaml:"hypervisor_type,omitempty"`
	HypervisorVersion string            `yaml:"hypervisor_version,omitempty"`
	VCPUs             int32             `yaml:"vcpus"`
	Memory            string            `yaml:"memory"`
	Disk              string            `yaml:"disk"`
	Network           string            `yaml:"network"`
	StaticIP          string            `yaml:"static_ip,omitempty"`
	Ports             []string          `yaml:"ports,omitempty"` // [hostIP:]hostPort:guestPort[/protocol]
	Hostname          string            `yaml:"hostname,omitempty"`
	Volumes           []volumeMountFile `yaml:"volumes,omitempty"`
	Files             []fileMountFile   `yaml:"files,omitempty"`
	Env               map[string]string `yaml:"env,omitempty"`
	Command           []string          `yaml:"command,omitempty"` // replaces ENTRYPOINT and CMD
	Labels            map[string]string `yaml:"labels,omitempty"`
	Restart           string            `yaml:"restart,omitempty"` // no | on-failure[:N] | unless-stopped | always
	HealthCheck       *healthCheckFile  `yaml:"healthcheck,omitempty"`
	InitMode          string            `yaml:"init_mode,omitempty"`      // auto | exec | systemd
	RemoveOnExit      bool              `yaml:"remove_on_exit,omitempty"` // delete it once it stops
}

type volumeMountFile struct {
	Volume     string `yaml:"volume"`
	MountPath  string `yaml:"mount_path"`
	AccessMode string `yaml:"access_mode,omitempty"`
}

// fileMountFile exposes a host file to the guest at /run/secrets/<name>.
// Dicer stores nothing: whatever manages the file on the host stays in charge
// of it, and the contents are re-read on every start.
type fileMountFile struct {
	Name     string `yaml:"name"`
	HostPath string `yaml:"host_path"`
}

// readInstanceFile reads a definition from path, or from stdin if path is
// "-".
func readInstanceFile(stdin io.Reader, path string) (*instanceFile, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, osCause(err))
	}

	var spec instanceFile
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", path, err)
	}

	return &spec, nil
}

// spec converts a definition file into the instance it describes.
func (f *instanceFile) spec() (dicer.InstanceSpec, error) {
	spec := dicer.InstanceSpec{
		Name:              f.Name,
		ImageRef:          f.Image,
		KernelName:        f.Kernel,
		KernelArgs:        f.KernelArgs,
		HypervisorType:    dicer.HypervisorType(f.HypervisorType),
		HypervisorVersion: f.HypervisorVersion,
		VCPUs:             int(f.VCPUs),
		NetworkName:       f.Network,
		StaticIP:          f.StaticIP,
		Hostname:          f.Hostname,
		Env:               f.Env,
		Cmd:               f.Command,
		Labels:            f.Labels,
		InitMode:          dicer.InitMode(f.InitMode),
		RemoveOnExit:      f.RemoveOnExit,
	}

	if f.Restart != "" {
		p, err := parseRestartPolicy(f.Restart)
		if err != nil {
			return spec, err
		}
		spec.Restart = *p
	}
	if f.HealthCheck != nil {
		hc, err := f.HealthCheck.check()
		if err != nil {
			return spec, fmt.Errorf("healthcheck: %w", err)
		}
		spec.HealthCheck = hc
	}

	if f.Memory != "" {
		bytes, err := parseMemoryBytes(f.Memory)
		if err != nil {
			return spec, err
		}
		spec.MemoryBytes = bytes
	}
	if f.Disk != "" {
		bytes, err := parseDiskBytes(f.Disk)
		if err != nil {
			return spec, err
		}
		spec.DiskBytes = bytes
	}

	ports, err := parsePortFlags(f.Ports)
	if err != nil {
		return spec, err
	}
	spec.Ports = ports

	for _, file := range f.Files {
		spec.Files = append(spec.Files, dicer.FileMount{Name: file.Name, HostPath: file.HostPath})
	}

	for _, v := range f.Volumes {
		spec.VolumeMounts = append(spec.VolumeMounts, dicer.VolumeMount{
			VolumeName: v.Volume,
			MountPath:  v.MountPath,
			AccessMode: dicer.VolumeAccessMode(v.AccessMode),
		})
	}

	return spec, nil
}
