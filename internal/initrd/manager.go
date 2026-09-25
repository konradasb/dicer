// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package initrd builds the initramfs a guest boots from, carrying dicer-init
// and dicer-agent. It is rebuilt only when the embedded binaries change.
package initrd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sync/singleflight"

	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/registry"
)

// Puller abstracts the OCI registry operations needed to build an initrd.
type Puller interface {
	Resolve(ctx context.Context, ref *reference.Ref) (string, error)
	PullAndExport(
		ctx context.Context, imageRef, digest, exportDir string, onEvent registry.EventFunc,
	) (*registry.PullResult, error)
}

const (
	// defaultBaseImage is the OCI reference used when Config.BaseImage is unset.
	defaultBaseImage = "alpine:3.21"

	// initrdFilename is the name of the CPIO archive written to the build dir.
	initrdFilename = "initrd"

	// hashFilename stores the content hash of the last successful build.
	hashFilename = ".hash"
)

// Config configures an initrd Manager.
type Config struct {
	// Puller fetches the base image the initramfs is built from.
	Puller Puller

	// DataDir is the base directory for storing built initrd images.
	DataDir string

	// BaseImage is the OCI reference for the Alpine base image.
	// Defaults to "alpine:3.21".
	BaseImage string

	// Logger is an optional structured logger.
	Logger *slog.Logger
}

// Manager manages initrd images on disk.
type Manager struct {
	cfg     Config
	puller  Puller
	packer  cpioPacker
	baseRef *reference.Ref
	sf      singleflight.Group
	logger  *slog.Logger
}

// NewManager creates an initrd Manager.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Puller == nil {
		return nil, errors.New("puller is required")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if cfg.BaseImage == "" {
		cfg.BaseImage = defaultBaseImage
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	ref, err := reference.Parse(cfg.BaseImage)
	if err != nil {
		return nil, fmt.Errorf("parse base image reference: %w", err)
	}

	m := &Manager{
		cfg:     cfg,
		puller:  cfg.Puller,
		baseRef: ref,
		logger:  cfg.Logger.With("component", "initrd"),
	}

	return m, nil
}

// Prepare ensures the initrd for the current arch exists and matches the
// embedded binaries, building it if necessary, and returns its path. The path
// is the same for every build, so a hypervisor that reopens it -- as Cloud
// Hypervisor does on a guest reboot and on restoring a snapshot -- always finds
// a complete initrd. Safe for concurrent use.
func (m *Manager) Prepare(ctx context.Context) (string, error) {
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	default:
		return "", fmt.Errorf("unsupported architecture %q", runtime.GOARCH)
	}

	initBin, err := initBinary()
	if err != nil {
		return "", fmt.Errorf("read embedded init binary: %w", err)
	}

	agentBin, err := agentBinary()
	if err != nil {
		return "", fmt.Errorf("read embedded agent binary: %w", err)
	}

	want := m.contentHash(initBin, agentBin)
	path := m.initrdPath(arch)

	if m.current(arch, want) {
		return path, nil
	}

	// Build via singleflight to avoid redundant concurrent builds, checking
	// again inside it in case a build finished since the check above.
	_, err, _ = m.sf.Do("prepare-"+arch, func() (any, error) {
		if m.current(arch, want) {
			return path, nil
		}
		return path, m.build(ctx, arch, want, initBin, agentBin)
	})
	if err != nil {
		return "", fmt.Errorf("ensure initrd: %w", err)
	}

	return path, nil
}

// current reports whether the initrd for arch exists and was built from the
// inputs hashing to want.
func (m *Manager) current(arch, want string) bool {
	if _, err := os.Stat(m.initrdPath(arch)); err != nil {
		return false
	}
	h, err := os.ReadFile(m.hashPath(arch))
	return err == nil && string(h) == want
}

// build pulls the base image, installs the init and agent binaries, and packs
// the directory tree into a CPIO archive beside the initrd. Only then is it
// renamed into place, so the path never holds a partial initrd, and a
// hypervisor that already opened the old one keeps reading it. The hash is
// written last: a crash in between leaves a stale hash, which rebuilds.
func (m *Manager) build(ctx context.Context, arch, hash string, initBin, agentBin []byte) error {
	if len(initBin) == 0 {
		return errors.New("init binary must not be empty")
	}
	if len(agentBin) == 0 {
		return errors.New("agent binary must not be empty")
	}

	outDir := m.archDir(arch)
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	m.logger.Info("building initrd", "arch", arch, "base", m.cfg.BaseImage)

	digest, err := m.puller.Resolve(ctx, m.baseRef)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", m.cfg.BaseImage, err)
	}

	tmp, err := os.MkdirTemp("", "dicer-initrd-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	dir := filepath.Join(tmp, "fs")

	m.logger.Info("pulling base image", "ref", m.baseRef.String(), "digest", digest)

	// Nothing watches the initrd being built, so no progress is reported.
	if _, err := m.puller.PullAndExport(ctx, m.baseRef.String(), digest, dir, nil); err != nil {
		return fmt.Errorf("pull %s: %w", m.cfg.BaseImage, err)
	}

	// Install binaries into the initrd filesystem tree.
	for _, bin := range []struct {
		path string
		data []byte
	}{
		{filepath.Join(dir, "init"), initBin},
		{filepath.Join(dir, "usr", "local", "bin", "dicer-agent"), agentBin},
	} {
		if err := os.MkdirAll(filepath.Dir(bin.path), 0o750); err != nil {
			return fmt.Errorf("create dir for %s: %w", bin.path, err)
		}
		if err := os.WriteFile(bin.path, bin.data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", bin.path, err)
		}
	}

	outPath := m.initrdPath(arch)
	partial := outPath + ".tmp"
	defer func() { _ = os.Remove(partial) }()

	sizeBytes, err := m.packer.Pack(ctx, dir, partial)
	if err != nil {
		return fmt.Errorf("pack dir as cpio: %w", err)
	}

	if err := os.Rename(partial, outPath); err != nil {
		return fmt.Errorf("install initrd: %w", err)
	}

	if err := os.WriteFile(m.hashPath(arch), []byte(hash), 0o644); err != nil {
		return fmt.Errorf("write hash file: %w", err)
	}

	m.logger.Info("initrd built", "path", outPath, "size_bytes", sizeBytes)
	return nil
}

// contentHash returns a hex fingerprint of all build inputs so that any change
// to the base image, init binary, or agent binary triggers a fresh build.
func (m *Manager) contentHash(initBin, agentBin []byte) string {
	h := sha256.New()
	h.Write([]byte(m.cfg.BaseImage))
	h.Write(initBin)
	h.Write(agentBin)
	return hex.EncodeToString(h.Sum(nil))
}
