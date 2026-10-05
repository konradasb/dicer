// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package boot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/types"
)

// rootfs builds a guest root filesystem: files, each executable, and
// symlinks, from the guest's own paths to their targets as the guest sees
// them.
func rootfs(t *testing.T, files []string, links map[string]string) string {
	t.Helper()

	root := t.TempDir()
	for _, f := range files {
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range links {
		path := filepath.Join(root, link)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolveMode(t *testing.T) {
	debian := []string{"lib/systemd/systemd", "bin/bash"}

	tests := []struct {
		name  string
		files []string
		links map[string]string
		cfg   guest.Config
		want  types.InitMode
	}{
		{
			name: "systemd itself", files: debian,
			cfg: guest.Config{Entrypoint: []string{"/lib/systemd/systemd"}}, want: types.InitModeSystemd,
		},
		{
			name: "init linked to systemd", files: debian, links: map[string]string{"sbin/init": "/lib/systemd/systemd"},
			cfg: guest.Config{Entrypoint: []string{"/sbin/init"}}, want: types.InitModeSystemd,
		},
		{
			name: "a relative link", files: debian, links: map[string]string{"sbin/init": "../lib/systemd/systemd"},
			cfg: guest.Config{Cmd: []string{"/sbin/init"}}, want: types.InitModeSystemd,
		},
		{
			name: "a chain of links", files: []string{"usr/lib/systemd/systemd"},
			links: map[string]string{"usr/sbin/init": "/sbin/init", "sbin/init": "/usr/lib/systemd/systemd"},
			cfg:   guest.Config{Entrypoint: []string{"/usr/sbin/init"}}, want: types.InitModeSystemd,
		},
		{
			name: "found on the PATH", files: debian, links: map[string]string{"usr/sbin/init": "/lib/systemd/systemd"},
			cfg: guest.Config{Entrypoint: []string{"init"}}, want: types.InitModeSystemd,
		},
		{
			name: "no command boots the machine's init", files: debian, links: map[string]string{"sbin/init": "/lib/systemd/systemd"},
			cfg: guest.Config{}, want: types.InitModeSystemd,
		},
		{
			name: "OpenRC's init is not systemd", files: []string{"sbin/openrc-init"},
			links: map[string]string{"sbin/init": "/sbin/openrc-init"},
			cfg:   guest.Config{Entrypoint: []string{"/sbin/init"}}, want: types.InitModeExec,
		},
		{
			name: "s6-overlay", files: []string{"init", "package/admin/s6-overlay/libexec/stage0"},
			cfg: guest.Config{Entrypoint: []string{"/init"}}, want: types.InitModeExec,
		},
		{
			name: "a replaced command is what runs", files: debian, links: map[string]string{"sbin/init": "/lib/systemd/systemd"},
			cfg: guest.Config{Entrypoint: []string{"/bin/bash"}}, want: types.InitModeExec,
		},
		{
			// Resolved from the guest's root, the link finds nothing: the
			// host's systemd is no business of the guest's.
			name: "a link out of the root", links: map[string]string{"sbin/init": "../../../../../lib/systemd/systemd"},
			cfg: guest.Config{Entrypoint: []string{"/sbin/init"}}, want: types.InitModeExec,
		},
		{
			name: "a dangling link", links: map[string]string{"sbin/init": "/lib/systemd/systemd"},
			cfg: guest.Config{Entrypoint: []string{"/sbin/init"}}, want: types.InitModeExec,
		},
		{
			name: "nothing there", cfg: guest.Config{Entrypoint: []string{"missing"}}, want: types.InitModeExec,
		},
		{
			name: "a mode asked for is kept", files: debian, links: map[string]string{"sbin/init": "/lib/systemd/systemd"},
			cfg: guest.Config{Mode: types.InitModeExec, Entrypoint: []string{"/sbin/init"}}, want: types.InitModeExec,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := rootfs(t, tt.files, tt.links)
			cfg := tt.cfg
			if cfg.Mode == "" {
				cfg.Mode = types.InitModeAuto
			}
			if got := resolveMode(root, &cfg); got != tt.want {
				t.Errorf("resolveMode = %s, want %s", got, tt.want)
			}
		})
	}
}
