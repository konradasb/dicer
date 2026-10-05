// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package initrd builds the initramfs a guest boots from, carrying dicer-init
// and dicer-agent. It is rebuilt only when the embedded binaries change.
package initrd

import (
	"cmp"
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

// Puller is the registry the base image of an initrd is pulled from.
type Puller interface {
	Resolve(ctx context.Context, ref *reference.Ref) (string, error)
	PullAndExport(
		ctx context.Context, imageRef, digest, exportDir string, onProgress registry.ProgressFunc,
	) (*registry.Metadata, error)
}

const (
	// defaultBaseImage is the OCI reference used when Config.BaseImage is unset.
	defaultBaseImage = "alpine:3.21"

	// initrdFilename is the name of the initrd in its architecture's
	// directory.
	initrdFilename = "initrd"

	// hashFilename stores the content hash of the last successful build.
	hashFilename = ".hash"
)

// Config configures an initrd Manager.
type Config struct {
	// Puller fetches the base image the initramfs is built from.
	Puller Puller

	// DataDir is the daemon's data directory; initrds are kept under its
	// initrd directory. Required.
	DataDir string

	// BaseImage is the image the initramfs is built from. Empty is
	// alpine:3.21.
	BaseImage string

	// Logger is where progress is logged. Nil is slog.Default.
	Logger *slog.Logger
}

// Manager builds the initrd and keeps it on disk. It is safe for concurrent
// use.
type Manager struct {
	puller    Puller
	dataDir   string
	baseImage string
	baseRef   *reference.Ref
	logger    *slog.Logger

	// builds lets one build run for an architecture at a time.
	builds singleflight.Group
}

// NewManager returns a Manager, refusing a Config without a Puller or a
// DataDir.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Puller == nil {
		return nil, errors.New("puller is required")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	baseImage := cmp.Or(cfg.BaseImage, defaultBaseImage)
	logger := cmp.Or(cfg.Logger, slog.Default())

	ref, err := reference.Parse(baseImage)
	if err != nil {
		return nil, fmt.Errorf("parse base image reference: %w", err)
	}

	return &Manager{
		puller:    cfg.Puller,
		dataDir:   cfg.DataDir,
		baseImage: baseImage,
		baseRef:   ref,
		logger:    logger.With("component", "initrd"),
	}, nil
}

// Prepare ensures the initrd for the host's architecture exists and matches
// the embedded binaries, building it if necessary, and returns its path. The
// path is the same for every build, so a hypervisor that reopens it -- as
// Cloud Hypervisor does on a guest reboot and on restoring a snapshot --
// always finds a complete initrd.
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

	initBinary, err := embeddedBinary("dicer-init")
	if err != nil {
		return "", fmt.Errorf("read embedded init binary: %w", err)
	}

	agentBinary, err := embeddedBinary("dicer-agent")
	if err != nil {
		return "", fmt.Errorf("read embedded agent binary: %w", err)
	}

	want := m.contentHash(initBinary, agentBinary)
	path := m.initrdPath(arch)
	if m.isCurrent(arch, want) {
		return path, nil
	}

	// Check again once in the build, in case one finished since.
	_, err, _ = m.builds.Do(arch, func() (any, error) {
		if m.isCurrent(arch, want) {
			return path, nil
		}
		return path, m.build(ctx, arch, want, initBinary, agentBinary)
	})
	if err != nil {
		return "", fmt.Errorf("build initrd: %w", err)
	}

	return path, nil
}

// isCurrent reports whether the initrd for arch exists and was built from
// the inputs hashing to want.
func (m *Manager) isCurrent(arch, want string) bool {
	if _, err := os.Stat(m.initrdPath(arch)); err != nil {
		return false
	}
	hash, err := os.ReadFile(m.hashPath(arch))
	return err == nil && string(hash) == want
}

// build pulls the base image, installs the init and agent binaries, and packs
// the tree into an archive beside the initrd. Only then is it renamed into
// place, so the path never holds a partial initrd, and a hypervisor that
// already opened the old one keeps reading it. The hash is written last: a
// crash in between leaves a stale hash, which rebuilds.
func (m *Manager) build(ctx context.Context, arch, hash string, initBinary, agentBinary []byte) error {
	if len(initBinary) == 0 {
		return errors.New("init binary must not be empty")
	}
	if len(agentBinary) == 0 {
		return errors.New("agent binary must not be empty")
	}

	if err := os.MkdirAll(m.archDir(arch), 0o750); err != nil {
		return fmt.Errorf("create initrd directory: %w", err)
	}

	m.logger.Info("building initrd", "arch", arch, "ref", m.baseImage)

	digest, err := m.puller.Resolve(ctx, m.baseRef)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", m.baseImage, err)
	}

	buildDir, err := os.MkdirTemp("", "dicer-initrd-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(buildDir) }()

	root := filepath.Join(buildDir, "fs")

	m.logger.Info("pulling base image", "ref", m.baseRef.String(), "digest", digest)

	// Nothing watches the initrd being built, so no progress is reported.
	if _, err := m.puller.PullAndExport(ctx, m.baseRef.String(), digest, root, nil); err != nil {
		return fmt.Errorf("pull %s: %w", m.baseImage, err)
	}

	for _, binary := range []struct {
		path string
		data []byte
	}{
		{filepath.Join(root, "init"), initBinary},
		{filepath.Join(root, "usr", "local", "bin", "dicer-agent"), agentBinary},
	} {
		if err := os.MkdirAll(filepath.Dir(binary.path), 0o750); err != nil {
			return fmt.Errorf("create directory for %s: %w", binary.path, err)
		}
		if err := os.WriteFile(binary.path, binary.data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", binary.path, err)
		}
	}

	path := m.initrdPath(arch)
	partial := path + ".tmp"
	defer func() { _ = os.Remove(partial) }()

	sizeBytes, err := writeCPIO(ctx, root, partial)
	if err != nil {
		return fmt.Errorf("pack initrd: %w", err)
	}

	if err := os.Rename(partial, path); err != nil {
		return fmt.Errorf("install initrd: %w", err)
	}

	if err := os.WriteFile(m.hashPath(arch), []byte(hash), 0o644); err != nil {
		return fmt.Errorf("write hash file: %w", err)
	}

	m.logger.Info("initrd built", "path", path, "size_bytes", sizeBytes)
	return nil
}

// contentHash returns a hex fingerprint of all build inputs so that any change
// to the base image, init binary, or agent binary triggers a fresh build.
func (m *Manager) contentHash(initBinary, agentBinary []byte) string {
	h := sha256.New()
	h.Write([]byte(m.baseImage))
	h.Write(initBinary)
	h.Write(agentBinary)
	return hex.EncodeToString(h.Sum(nil))
}
