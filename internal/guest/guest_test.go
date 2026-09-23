// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package guest

import (
	"strings"
	"testing"

	"github.com/dicer-sh/dicer"
)

func TestConfig_ApplyDefaults(t *testing.T) {
	t.Run("empty mode defaults to auto", func(t *testing.T) {
		c := Config{}
		c.ApplyDefaults()
		if c.Mode != dicer.ModeAuto {
			t.Errorf("dicer.InitMode = %q, want %q", c.Mode, dicer.ModeAuto)
		}
	})

	t.Run("mode already set is preserved", func(t *testing.T) {
		c := Config{Mode: dicer.ModeSystemd}
		c.ApplyDefaults()
		if c.Mode != dicer.ModeSystemd {
			t.Errorf("dicer.InitMode = %q, want %q", c.Mode, dicer.ModeSystemd)
		}
	})

	t.Run("nil env is initialized", func(t *testing.T) {
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

	t.Run("volume mount defaults", func(t *testing.T) {
		c := Config{
			VolumeMounts: []VolumeMount{
				{Device: "/dev/vdd", Path: "/data"},
				{Device: "/dev/vde", Path: "/logs", Fstype: "xfs", Mode: MountModeRO},
			},
		}
		c.ApplyDefaults()

		if c.VolumeMounts[0].Fstype != "ext4" {
			t.Errorf("VolumeMounts[0].Fstype = %q, want %q", c.VolumeMounts[0].Fstype, "ext4")
		}
		if c.VolumeMounts[0].Mode != MountModeRW {
			t.Errorf("VolumeMounts[0].Mode = %q, want %q", c.VolumeMounts[0].Mode, MountModeRW)
		}

		// Pre-set values must not be overwritten.
		if c.VolumeMounts[1].Fstype != "xfs" {
			t.Errorf("VolumeMounts[1].Fstype = %q, want %q", c.VolumeMounts[1].Fstype, "xfs")
		}
		if c.VolumeMounts[1].Mode != MountModeRO {
			t.Errorf("VolumeMounts[1].Mode = %q, want %q", c.VolumeMounts[1].Mode, MountModeRO)
		}
	})
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		// --- dicer.InitMode ---
		{
			name:    "invalid mode",
			cfg:     Config{Mode: "bad"},
			wantErr: "invalid init mode",
		},
		{
			name:    "exec mode without entrypoint or cmd",
			cfg:     Config{Mode: dicer.ModeExec},
			wantErr: "exec mode requires at least one of entrypoint or cmd",
		},
		{
			name: "exec mode with entrypoint only",
			cfg:  Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}},
		},
		{
			name: "exec mode with cmd only",
			cfg:  Config{Mode: dicer.ModeExec, Cmd: []string{"echo", "hi"}},
		},
		{
			name: "systemd mode without entrypoint",
			cfg:  Config{Mode: dicer.ModeSystemd},
		},
		{
			// It boots the machine's init.
			name: "auto mode without entrypoint",
			cfg:  Config{Mode: dicer.ModeAuto},
		},

		// --- Halt ---
		{
			name: "power off",
			cfg:  Config{Mode: dicer.ModeSystemd, Halt: HaltPowerOff},
		},
		{
			name: "reset",
			cfg:  Config{Mode: dicer.ModeSystemd, Halt: HaltReset},
		},
		{
			name:    "invalid halt",
			cfg:     Config{Mode: dicer.ModeSystemd, Halt: "explode"},
			wantErr: "invalid halt",
		},

		// --- Workdir ---
		{
			name:    "relative workdir",
			cfg:     Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Workdir: "relative"},
			wantErr: "workdir",
		},
		{
			name: "absolute workdir",
			cfg:  Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Workdir: "/app"},
		},

		// --- Volume mounts ---
		{
			name: "volume empty device",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				VolumeMounts: []VolumeMount{{Path: "/data", Mode: MountModeRW}},
			},
			wantErr: "device not set",
		},
		{
			name: "volume empty path",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				VolumeMounts: []VolumeMount{{Device: "/dev/vdd", Mode: MountModeRW}},
			},
			wantErr: "path not set",
		},
		{
			name: "volume relative path",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				VolumeMounts: []VolumeMount{{Device: "/dev/vdd", Path: "data", Mode: MountModeRW}},
			},
			wantErr: "must be absolute",
		},
		{
			name: "volume invalid mode",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				VolumeMounts: []VolumeMount{{Device: "/dev/vdd", Path: "/data", Mode: "bad"}},
			},
			wantErr: "invalid mode",
		},
		{
			name: "valid ro mount",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				VolumeMounts: []VolumeMount{{Device: "/dev/vdd", Path: "/data", Mode: MountModeRO}},
			},
		},

		// --- Injected files ---
		{
			name: "injected file with empty name",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Files: []FileMount{
					{Name: "", Value: []byte("secret")},
				},
			},
			wantErr: "name not set",
		},
		{
			name: "injected file with empty value",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Files: []FileMount{
					{Name: "mysecret", Value: []byte("")},
				},
			},
			wantErr: "value not set",
		},
		{
			name: "valid injected file",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Files: []FileMount{
					{Name: "mysecret", Value: []byte("secret")},
				},
			},
		},

		// --- Network interfaces ---
		{
			name: "interface without name",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Addresses: []string{"10.0.0.2/24"}}},
				},
			},
			wantErr: "interface name not set",
		},
		{
			name: "interface without addresses",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Interface: "eth0"}},
				},
			},
			wantErr: "at least one address required",
		},
		{
			name: "valid interface",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Interface: "eth0", Addresses: []string{"10.0.0.2/24"}}},
				},
			},
		},

		// --- Network routes ---
		{
			name: "route without destination",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Routes: []NetworkRoute{{Gateway: "10.0.0.1"}},
				},
			},
			wantErr: "destination not set",
		},
		{
			name: "valid route",
			cfg: Config{
				Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"},
				Network: NetworkConfig{
					Routes: []NetworkRoute{{Destination: "default", Gateway: "10.0.0.1"}},
				},
			},
		},

		// --- Hostname ---
		{
			name: "valid simple hostname",
			cfg:  Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "myhost"},
		},
		{
			name: "valid fqdn hostname",
			cfg:  Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "my-host.example.com"},
		},
		{
			name: "empty hostname is allowed",
			cfg:  Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: ""},
		},
		{
			name:    "hostname starts with hyphen",
			cfg:     Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "-bad"},
			wantErr: "hostname \"-bad\" is invalid",
		},
		{
			name:    "hostname ends with hyphen",
			cfg:     Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "bad-"},
			wantErr: "hostname \"bad-\" is invalid",
		},
		{
			name:    "hostname label starts with hyphen",
			cfg:     Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "good.-bad.com"},
			wantErr: "hostname \"good.-bad.com\" is invalid",
		},
		{
			name:    "hostname contains invalid character",
			cfg:     Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: "bad_host"},
			wantErr: "hostname \"bad_host\" is invalid",
		},
		{
			name:    "hostname too long",
			cfg:     Config{Mode: dicer.ModeExec, Entrypoint: []string{"/bin/sh"}, Hostname: strings.Repeat("a", 254)},
			wantErr: "exceeds 253 characters",
		},

		// --- Full valid config ---
		{
			name: "full valid config",
			cfg: Config{
				Mode:       dicer.ModeExec,
				Entrypoint: []string{"/bin/sh"},
				Cmd:        []string{"-c", "echo hello"},
				Workdir:    "/app",
				Env:        map[string]string{"PATH": "/usr/bin"},
				VolumeMounts: []VolumeMount{
					{Device: "/dev/vdd", Path: "/data", Mode: MountModeRW, Fstype: "ext4"},
				},
				Network: NetworkConfig{
					Interfaces: []NetworkInterface{{Interface: "eth0", Addresses: []string{"10.0.0.2/24"}, MTU: 1500}},
					Routes:     []NetworkRoute{{Destination: "default", Gateway: "10.0.0.1", Dev: "eth0"}},
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

func TestParseMode(t *testing.T) {
	for in, want := range map[string]dicer.InitMode{"": dicer.ModeAuto, "auto": dicer.ModeAuto, "exec": dicer.ModeExec, "systemd": dicer.ModeSystemd} {
		if got, err := dicer.ParseInitMode(in); err != nil || got != want {
			t.Errorf("dicer.ParseInitMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := dicer.ParseInitMode("openrc"); err == nil {
		t.Error("dicer.ParseInitMode accepted an unknown mode")
	}
}

func TestArgv(t *testing.T) {
	tests := []struct {
		cfg  Config
		want []string
	}{
		{Config{Entrypoint: []string{"/init"}, Cmd: []string{"serve"}}, []string{"/init", "serve"}},
		{Config{Cmd: []string{"nginx"}}, []string{"nginx"}},
		{Config{}, []string{"/sbin/init"}},
	}
	for _, tt := range tests {
		if got := tt.cfg.Argv(); strings.Join(got, " ") != strings.Join(tt.want, " ") {
			t.Errorf("Argv() = %q, want %q", got, tt.want)
		}
	}
}
