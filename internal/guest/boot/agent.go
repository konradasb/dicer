// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package boot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
)

const agentServiceUnit = `[Unit]
Description=Dicer Agent
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/dicer-agent
EnvironmentFile=-/etc/dicer/env
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`

// installGuestAgent copies the dicer-agent binary from the initrd into the
// overlay rootfs. The copy is atomic (write to .tmp then rename) and
// idempotent.
//
// An already-installed copy is compared with the source, byte for byte,
// rather than merely looked for. The initrd carries the agent of the daemon
// booting the instance, which the installed copy must match, and two builds
// can easily be the same size. The installed copy may also be damaged: a
// guest whose VMM was killed without a clean shutdown, which is what happens
// whenever PID 1 ignores the ACPI signal, can leave a truncated binary
// behind. Reading both is cheap next to a boot.
func installGuestAgent(log *slog.Logger) error {
	const (
		src = "/usr/local/bin/dicer-agent"
		dst = overlayRoot + "/usr/local/bin/dicer-agent"
	)

	same, err := sameContents(src, dst)
	if err != nil {
		return err
	}
	if same {
		log.Debug("dicer-agent already installed, skipping copy")
		return nil
	}

	if err := os.MkdirAll(overlayRoot+"/usr/local/bin", 0o755); err != nil {
		return fmt.Errorf("mkdir /usr/local/bin: %w", err)
	}

	tmp := dst + ".tmp"
	if err := copyFile(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install dicer-agent: %w", err)
	}
	if err := syncDir(overlayRoot + "/usr/local/bin"); err != nil {
		return err
	}

	log.Info("dicer-agent installed", "path", "/usr/local/bin/dicer-agent")
	return nil
}

// injectGuestAgentUnit writes the systemd unit file and environment file into
// the overlay rootfs so that dicer-agent starts automatically under systemd.
func injectGuestAgentUnit(env map[string]string) error {
	const (
		unitDir  = overlayRoot + "/etc/systemd/system"
		wantsDir = unitDir + "/multi-user.target.wants"
		envDir   = overlayRoot + "/etc/dicer"
	)

	for _, d := range []string{unitDir, wantsDir, envDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	if err := os.WriteFile(envDir+"/env", []byte(buildEnvFile(env)), 0o644); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}

	unitPath := unitDir + "/dicer-agent.service"
	if err := os.WriteFile(unitPath, []byte(agentServiceUnit), 0o644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	// Enable by symlinking into the wants directory.
	link := wantsDir + "/dicer-agent.service"
	if err := os.Symlink("../dicer-agent.service", link); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("symlink unit: %w", err)
	}

	return nil
}

// sameContents reports whether the file at installed holds exactly what the
// one at src does. A missing installed file does not.
func sameContents(src, installed string) (bool, error) {
	want, err := os.ReadFile(src)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", src, err)
	}

	got, err := os.ReadFile(installed)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", installed, err)
	}

	return bytes.Equal(got, want), nil
}

// copyFile copies src to dst with 0755 permissions, verifying the Close error.
//
// The contents are flushed to the disk before the file is closed. Without
// that, only the rename that follows would be journalled, and a guest killed
// before the page cache was written back would come up with a zero-length
// file where a binary should be -- valid enough to skip reinstalling, and not
// valid enough to execute.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = srcFile.Close() }()

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = dstFile.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := dstFile.Sync(); err != nil {
		_ = dstFile.Close()
		return fmt.Errorf("sync %s: %w", dst, err)
	}
	if err := dstFile.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}

	return nil
}

// syncDir flushes a directory entry, so that a rename into it survives a
// guest that is killed rather than shut down.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = dir.Close() }()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}

	return nil
}
