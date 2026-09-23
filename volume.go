// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import "time"

// VolumeAccessMode is how an instance may use a volume it mounts.
type VolumeAccessMode string

const (
	// AccessModeReadWriteOnce is exclusive read-write by one instance.
	AccessModeReadWriteOnce VolumeAccessMode = "ReadWriteOnce"

	// AccessModeReadOnlyMany is shared read-only across instances.
	AccessModeReadOnlyMany VolumeAccessMode = "ReadOnlyMany"
)

// Volume is persistent storage an instance can mount, outliving the
// instances that use it.
type Volume struct {
	ID        string    `yaml:"id"`
	Name      string    `yaml:"name"`
	Path      string    `yaml:"path"`
	SizeBytes int64     `yaml:"size_bytes"`
	CreatedAt time.Time `yaml:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at"`
}

// VolumeMount attaches a named volume to an instance at a mount path.
type VolumeMount struct {
	VolumeName string           `yaml:"volume_name" json:"volume_name"`
	MountPath  string           `yaml:"mount_path" json:"mount_path"`
	AccessMode VolumeAccessMode `yaml:"access_mode" json:"access_mode,omitempty"`
}

// FileMount copies a file from the host into the guest at
// /run/secrets/<Name>, on a tmpfs, mode 0400.
//
// Dicer does not store the contents: the file is read from the host at start
// and handed to the guest on its config disk. Whatever manages the file on
// the host -- sops, vault-agent, systemd credentials, a plain root-owned
// file -- remains in charge of it. A daemon that owns one machine has no
// business reimplementing a secret store whose encryption key would sit on
// the same disk as the ciphertext.
type FileMount struct {
	// Name is the filename inside the guest, under /run/secrets.
	Name string `yaml:"name" json:"name"`

	// HostPath is the file to read on the host.
	HostPath string `yaml:"host_path" json:"host_path"`
}
