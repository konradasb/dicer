// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import (
	"slices"
	"strings"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

func TestConfig_ApplyDefaults(t *testing.T) {
	t.Run("empty mode defaults to auto", func(t *testing.T) {
		c := Config{}
		c.ApplyDefaults()
		if c.Mode != types.InitModeAuto {
			t.Errorf("Mode = %q, want %q", c.Mode, types.InitModeAuto)
		}
	})

	t.Run("mode already set is preserved", func(t *testing.T) {
		c := Config{Mode: types.InitModeSystemd}
		c.ApplyDefaults()
		if c.Mode != types.InitModeSystemd {
			t.Errorf("Mode = %q, want %q", c.Mode, types.InitModeSystemd)
		}
	})

	t.Run("nil env is initialised", func(t *testing.T) {
		c := Config{}
		c.ApplyDefaults()
		if c.Env == nil {
			t.Fatal("Env is nil after ApplyDefaults")
		}
	})

	t.Run("existing env is preserved", func(t *testing.T) {
		c := Config{Env: map[string]string{"KEY": "val"}}
		c.ApplyDefaults()
		if c.Env["KEY"] != "val" {
			t.Errorf("Env[KEY] = %q, want %q", c.Env["KEY"], "val")
		}
	})

	t.Run("volume fstype defaults to ext4", func(t *testing.T) {
		c := Config{
			Mounts: []Mount{
				{Target: "/data", Volume: &VolumeSource{Device: "/dev/vde"}},
				{Target: "/logs", Volume: &VolumeSource{Device: "/dev/vdf", FilesystemType: "xfs"}},
				{Target: "/scratch", Tmpfs: &TmpfsSource{}},
			},
		}
		c.ApplyDefaults()

		if got := c.Mounts[0].Volume.FilesystemType; got != "ext4" {
			t.Errorf("Mounts[0].Volume.FilesystemType = %q, want %q", got, "ext4")
		}
		// A pre-set value must not be overwritten.
		if got := c.Mounts[1].Volume.FilesystemType; got != "xfs" {
			t.Errorf("Mounts[1].Volume.FilesystemType = %q, want %q", got, "xfs")
		}
	})
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "invalid mode",
			cfg:     Config{Mode: "bad"},
			wantErr: "invalid init mode",
		},
		{
			name:    "exec mode without entrypoint or cmd",
			cfg:     Config{Mode: types.InitModeExec},
			wantErr: "exec mode requires at least one of entrypoint or cmd",
		},
		{
			name: "exec mode with entrypoint only",
			cfg:  Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}},
		},
		{
			name: "exec mode with cmd only",
			cfg:  Config{Mode: types.InitModeExec, Cmd: []string{"echo", "hi"}},
		},
		{
			name: "systemd mode without entrypoint",
			cfg:  Config{Mode: types.InitModeSystemd},
		},
		{
			// It boots the machine's init.
			name: "auto mode without entrypoint",
			cfg:  Config{Mode: types.InitModeAuto},
		},

		{
			name: "power off",
			cfg:  Config{Mode: types.InitModeSystemd, Halt: HaltPowerOff},
		},
		{
			name: "reset",
			cfg:  Config{Mode: types.InitModeSystemd, Halt: HaltReset},
		},
		{
			name:    "invalid halt",
			cfg:     Config{Mode: types.InitModeSystemd, Halt: "explode"},
			wantErr: "invalid halt",
		},

		{
			name:    "relative workdir",
			cfg:     Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Workdir: "relative"},
			wantErr: "workdir",
		},
		{
			name: "absolute workdir",
			cfg:  Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Workdir: "/app"},
		},

		{
			name: "mount without target",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Mounts: []Mount{{Tmpfs: &TmpfsSource{}}},
			},
			wantErr: "target not set",
		},
		{
			name: "mount with relative target",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Mounts: []Mount{{Target: "data", Tmpfs: &TmpfsSource{}}},
			},
			wantErr: "must be absolute",
		},
		{
			name: "mount without source",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Mounts: []Mount{{Target: "/data"}},
			},
			wantErr: "exactly one",
		},
		{
			name: "mount with two sources",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Mounts: []Mount{{
					Target: "/data", Tmpfs: &TmpfsSource{}, File: &FileSource{Data: []byte("x")},
				}},
			},
			wantErr: "exactly one",
		},
		{
			name: "volume without device",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Mounts: []Mount{{Target: "/data", Volume: &VolumeSource{}}},
			},
			wantErr: "device not set",
		},
		{
			name: "valid mounts",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Mounts: []Mount{
					{Target: "/data", ReadOnly: true, Volume: &VolumeSource{Device: "/dev/vde"}},
					{Target: "/etc/app.conf", File: &FileSource{Data: []byte("k=v"), Mode: 0o644}},
					{Target: "/run/secrets/empty", File: &FileSource{Mode: 0o400}},
					{Target: "/scratch", Tmpfs: &TmpfsSource{}},
				},
			},
		},

		{
			name: "interface without name",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Addresses: []string{"10.0.0.2/24"}}},
				},
			},
			wantErr: "interface name not set",
		},
		{
			name: "interface without addresses",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Name: "eth0"}},
				},
			},
			wantErr: "at least one address required",
		},
		{
			name: "valid interface",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Name: "eth0", Addresses: []string{"10.0.0.2/24"}}},
				},
			},
		},

		{
			name: "route without destination",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Routes: []NetworkRoute{{Gateway: "10.0.0.1"}},
				},
			},
			wantErr: "destination not set",
		},
		{
			name: "valid route",
			cfg: Config{
				Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Routes: []NetworkRoute{{Destination: "default", Gateway: "10.0.0.1"}},
				},
			},
		},

		{
			name: "valid simple hostname",
			cfg:  Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "myhost"},
		},
		{
			name: "valid fqdn hostname",
			cfg:  Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "my-host.example.com"},
		},
		{
			name: "empty hostname is allowed",
			cfg:  Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: ""},
		},
		{
			name:    "hostname starts with hyphen",
			cfg:     Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "-bad"},
			wantErr: "hostname \"-bad\" is invalid",
		},
		{
			name:    "hostname ends with hyphen",
			cfg:     Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "bad-"},
			wantErr: "hostname \"bad-\" is invalid",
		},
		{
			name:    "hostname label starts with hyphen",
			cfg:     Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "good.-bad.com"},
			wantErr: "hostname \"good.-bad.com\" is invalid",
		},
		{
			name:    "hostname contains invalid character",
			cfg:     Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "bad_host"},
			wantErr: "hostname \"bad_host\" is invalid",
		},
		{
			name:    "hostname too long",
			cfg:     Config{Mode: types.InitModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: strings.Repeat("a", 254)},
			wantErr: "exceeds 253 characters",
		},

		{
			name: "full valid config",
			cfg: Config{
				Mode:       types.InitModeExec,
				Entrypoint: []string{"/bin/sh"},
				Cmd:        []string{"-c", "echo hello"},
				Workdir:    "/app",
				Env:        map[string]string{"PATH": "/usr/bin"},
				Mounts: []Mount{
					{Target: "/data", Volume: &VolumeSource{Device: "/dev/vde", FilesystemType: "ext4"}},
				},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Name: "eth0", Addresses: []string{"10.0.0.2/24"}, MTU: 1500}},
					Routes:     []NetworkRoute{{Destination: "default", Gateway: "10.0.0.1", Device: "eth0"}},
					DNS:        DNSConfig{Nameservers: []string{"8.8.8.8"}, SearchDomains: []string{"local"}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q missing substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestArgv(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{
			name: "entrypoint and cmd",
			cfg:  Config{Entrypoint: []string{"/init"}, Cmd: []string{"serve"}},
			want: []string{"/init", "serve"},
		},
		{name: "cmd only", cfg: Config{Cmd: []string{"nginx"}}, want: []string{"nginx"}},
		{name: "neither boots init", cfg: Config{}, want: []string{"/sbin/init"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Argv(); !slices.Equal(got, tt.want) {
				t.Errorf("Argv() = %q, want %q", got, tt.want)
			}
		})
	}
}
