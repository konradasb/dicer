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
	"time"

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

// Initrd describes a built initrd image on disk.
type Initrd struct {
	ID        string
	Arch      string
	Path      string
	SizeBytes int64
	Hash      string
}

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

// Get returns an initrd by ID and arch.
func (m *Manager) Get(id, arch string) (*Initrd, error) {
	p := m.initrdPath(id, arch)
	info, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("initrd %s/%s not found: %w", id, arch, err)
	}

	var hash string
	if h, err := os.ReadFile(m.hashPath(id, arch)); err == nil {
		hash = string(h)
	}

	initrd := &Initrd{
		ID:        id,
		Arch:      arch,
		Path:      p,
		SizeBytes: info.Size(),
		Hash:      hash,
	}

	return initrd, nil
}

// GetLatest follows the latest symlink for the given arch.
func (m *Manager) GetLatest(arch string) (*Initrd, error) {
	link := m.latestLink(arch)
	target, err := os.Readlink(link)
	if err != nil {
		return nil, fmt.Errorf("read latest link for %s: %w", arch, err)
	}

	// The target is a relative path; resolve it.
	resolved := filepath.Join(filepath.Dir(link), target)
	info, err := os.Stat(filepath.Join(resolved, initrdFilename))
	if err != nil {
		return nil, fmt.Errorf("latest initrd not found: %w", err)
	}

	// Extract ID from the resolved path (parent of arch dir).
	id := filepath.Base(filepath.Dir(resolved))

	var hash string
	if h, err := os.ReadFile(filepath.Join(resolved, hashFilename)); err == nil {
		hash = string(h)
	}

	initrd := &Initrd{
		ID:        id,
		Arch:      arch,
		Path:      filepath.Join(resolved, initrdFilename),
		SizeBytes: info.Size(),
		Hash:      hash,
	}

	return initrd, nil
}

// Prepare ensures the initrd exists and is up to date for the current arch,
// building it if necessary. Returns the path to the initrd.
// Safe for concurrent use.
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

	// Check if latest exists and hash matches.
	if latest, err := m.GetLatest(arch); err == nil && latest.Hash == want {
		m.logger.Info("initrd already exists and is up to date", "path", latest.Path)
		return latest.Path, nil
	}

	// Build via singleflight to avoid redundant concurrent builds.
	result, err, _ := m.sf.Do("prepare-"+arch, func() (any, error) {
		id := time.Now().UTC().Format("20060102T150405Z")
		return m.build(ctx, id, arch, initBin, agentBin)
	})
	if err != nil {
		return "", fmt.Errorf("ensure initrd: %w", err)
	}

	initrd, ok := result.(*Initrd)
	if !ok {
		return "", fmt.Errorf("unexpected result type %T from initrd build", result)
	}

	m.logger.Info("initrd ready", "path", initrd.Path, "size_bytes", initrd.SizeBytes)
	return initrd.Path, nil
}

// build pulls the base image, installs the init and agent binaries, packs the
// directory tree into a CPIO archive, records the content hash, and updates
// the latest symlink.
func (m *Manager) build(ctx context.Context, id, arch string, initBin, agentBin []byte) (*Initrd, error) {
	if len(initBin) == 0 {
		return nil, errors.New("init binary must not be empty")
	}
	if len(agentBin) == 0 {
		return nil, errors.New("agent binary must not be empty")
	}

	outDir := filepath.Join(m.buildDir(id), arch)
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	m.logger.Info("building initrd", "id", id, "arch", arch, "base", m.cfg.BaseImage)

	digest, err := m.puller.Resolve(ctx, m.baseRef)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", m.cfg.BaseImage, err)
	}

	tmp, err := os.MkdirTemp("", "dicer-initrd-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	dir := filepath.Join(tmp, "fs")

	m.logger.Info("pulling base image", "ref", m.baseRef.String(), "digest", digest)

	// Nothing watches the initrd being built, so no progress is reported.
	if _, err := m.puller.PullAndExport(ctx, m.baseRef.String(), digest, dir, nil); err != nil {
		return nil, fmt.Errorf("pull %s: %w", m.cfg.BaseImage, err)
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
			return nil, fmt.Errorf("create dir for %s: %w", bin.path, err)
		}
		if err := os.WriteFile(bin.path, bin.data, 0o755); err != nil {
			return nil, fmt.Errorf("write %s: %w", bin.path, err)
		}
	}

	outPath := filepath.Join(outDir, initrdFilename)

	sizeBytes, err := m.packer.Pack(ctx, dir, outPath)
	if err != nil {
		return nil, fmt.Errorf("pack dir as cpio: %w", err)
	}

	hash := m.contentHash(initBin, agentBin)
	hashPath := filepath.Join(outDir, hashFilename)
	if err := os.WriteFile(hashPath, []byte(hash), 0o644); err != nil {
		return nil, fmt.Errorf("write hash file: %w", err)
	}

	// Update the latest symlink.
	if err := m.updateLatestLink(id, arch); err != nil {
		m.logger.Warn("failed to update latest symlink", "error", err)
	}

	m.logger.Info("initrd built", "path", outPath, "size_bytes", sizeBytes)

	initrd := &Initrd{
		ID:        id,
		Arch:      arch,
		Path:      outPath,
		SizeBytes: sizeBytes,
		Hash:      hash,
	}

	return initrd, nil
}

// updateLatestLink updates the latest symlink for an arch to point to a build.
func (m *Manager) updateLatestLink(id, arch string) error {
	link := m.latestLink(arch)
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		return fmt.Errorf("create latest link dir: %w", err)
	}

	// Target is relative: ../{id}/{arch}
	target := filepath.Join("..", id, arch)

	// Remove old symlink if it exists.
	_ = os.Remove(link)

	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}

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
