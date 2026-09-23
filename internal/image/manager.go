// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package image turns a container image into a disk a guest boots from.
//
// An image is pulled from a registry, unpacked, and packed into a read-only
// EROFS filesystem an instance mounts as its root. What is kept on the host
// is that disk and what the image said about itself -- its entrypoint, its
// environment, its health check -- which is dicer.Image.
//
// Images are held by digest, so two tags that resolve to the same image are
// one disk, and an instance keeps booting what it booted until its tag is
// pulled again. What is no longer used is removed by a prune, or by garbage
// collection under a policy.
package image

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/go-units"
	"golang.org/x/sync/semaphore"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/image/reference"
	"github.com/dicer-sh/dicer/internal/registry"
)

// defaultMaxConcurrentPulls bounds pulls when Config.MaxConcurrentPulls is
// unset.
const defaultMaxConcurrentPulls = 3

// Config configures a Manager.
type Config struct {
	// DataDir is the directory images are stored under.
	DataDir string

	// MaxConcurrentPulls bounds how many distinct images are pulled at once.
	// Defaults to 3.
	MaxConcurrentPulls int

	// Registry fetches images. It decides the platform pulled.
	Registry registryClient

	// Metrics records pulls. Optional: when nil, they are not recorded.
	Metrics Metrics

	// Events records what happens to images. Optional: when nil, it is not
	// recorded.
	Events Events

	Logger *slog.Logger
}

// registryClient defines the registry operations the Manager needs.
type registryClient interface {
	reference.Resolver
	PullAndExport(
		ctx context.Context, imageRef, digest, exportDir string, onEvent registry.EventFunc,
	) (*registry.PullResult, error)
	PruneCache(keep []string) (int64, error)
	// CacheSize returns what the layer cache occupies on disk.
	CacheSize() (int64, error)
}

// Metrics records how pulls went. It is declared here, and satisfied by
// internal/metrics, so this package measures itself without depending on a
// metrics library.
type Metrics interface {
	// RecordImagePull records a pull that went to a registry: its outcome,
	// how long it took and the compressed bytes it downloaded.
	RecordImagePull(err error, d time.Duration, downloadedBytes int64)

	// RecordImageConversion records the time spent packing an unpacked
	// image into the disk a guest boots from.
	RecordImageConversion(d time.Duration)

	// RecordImageCacheLookup records whether a requested image was already
	// held on this host.
	RecordImageCacheLookup(hit bool)

	// RecordImageCollected records an image garbage collection removed,
	// and why: GCReasonUnused or GCReasonSize.
	RecordImageCollected(reason string)

	// RecordImageGCReclaimed records the bytes garbage collection gave back.
	RecordImageGCReclaimed(bytes int64)
}

// discardMetrics is the Metrics used when none is configured, so that the
// pull path can record unconditionally. It is the same treatment
// Config.Logger gets.
type discardMetrics struct{}

func (discardMetrics) RecordImagePull(error, time.Duration, int64) {}
func (discardMetrics) RecordImageConversion(time.Duration)         {}
func (discardMetrics) RecordImageCacheLookup(bool)                 {}
func (discardMetrics) RecordImageCollected(string)                 {}
func (discardMetrics) RecordImageGCReclaimed(int64)                {}

// packer packs a directory tree into a filesystem image.
type packer interface {
	Pack(ctx context.Context, dir, outputPath string) (int64, error)
}

// Manager holds the images this host has pulled, each converted to a
// bootable disk and keyed by manifest digest.
//
// An image can always be fetched again, so the files here are replaceable:
// deleting one costs a pull, not data. The pull itself is deduplicated, so
// concurrent starts of the same image wait on one download.
type Manager struct {
	dataDir  string
	index    *index
	sem      *semaphore.Weighted // bounds concurrent pulls of different images
	registry registryClient
	packer   packer
	metrics  Metrics
	events   Events
	logger   *slog.Logger

	// pulls are the pulls under way, by manifest digest.
	pullsMu sync.Mutex
	pulls   map[string]*pull
}

// NewManager creates a Manager, loading any images already on disk.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.MaxConcurrentPulls <= 0 {
		cfg.MaxConcurrentPulls = defaultMaxConcurrentPulls
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = discardMetrics{}
	}
	if cfg.Events == nil {
		cfg.Events = discardEvents{}
	}

	m := &Manager{
		dataDir:  cfg.DataDir,
		index:    newIndex(),
		sem:      semaphore.NewWeighted(int64(cfg.MaxConcurrentPulls)),
		registry: cfg.Registry,
		packer:   erofs{},
		metrics:  cfg.Metrics,
		events:   cfg.Events,
		logger:   cfg.Logger.With("component", "image"),
		pulls:    make(map[string]*pull),
	}

	if err := m.initialize(); err != nil {
		return nil, fmt.Errorf("initialize image storage: %w", err)
	}

	if err := m.loadExistingImages(); err != nil {
		m.logger.Warn("failed to load existing images", "error", err)
	}

	return m, nil
}

// Pull returns the image a reference names, pulling and converting it first
// if this host does not hold it yet. It blocks until the image is ready or
// the pull fails. Concurrent pulls of the same image share one download.
//
// A tag is always resolved against the registry, so pulling a tag that has
// moved fetches the image it now points to.
func (m *Manager) Pull(ctx context.Context, ref string, onProgress ProgressFunc) (*dicer.Image, error) {
	if _, err := reference.Parse(ref); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}

	onProgress.send(dicer.PullProgress{Stage: dicer.StageResolving})

	resolved, err := reference.Resolve(ctx, m.registry, ref)
	if err != nil {
		return nil, &PullError{Ref: ref, Cause: err}
	}

	digest := resolved.ManifestDigest()
	img, hit := m.index.get(digest)
	m.metrics.RecordImageCacheLookup(hit)
	if hit {
		return img, nil
	}

	// Callers pulling the same image share one download; see sharedPull.
	return m.sharedPull(ctx, resolved, onProgress)
}

// Get returns a locally held image without consulting a registry. A tag
// matches the image it was pulled as.
func (m *Manager) Get(ref string) (*dicer.Image, error) {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}

	var (
		img *dicer.Image
		ok  bool
	)
	if parsed.HasDigest() {
		img, ok = m.index.get(parsed.Digest())
	} else {
		img, ok = m.index.findByName(parsed.String())
	}
	if !ok {
		return nil, dicer.NotFound("no image %q", ref)
	}

	return img, nil
}

// List returns every locally held image.
func (m *Manager) List() []*dicer.Image {
	return m.index.list()
}

// Delete removes a locally held image and its disk. Like Get, it does not
// consult a registry: deleting a tag removes the image pulled under it, even
// if the tag has since moved.
func (m *Manager) Delete(ref string) error {
	img, err := m.Get(ref)
	if err != nil {
		return err
	}

	if err := m.index.delete(img.Digest); err != nil && !errors.Is(err, dicer.ErrNotFound) {
		return err
	}

	if err := m.deleteImage(digestHex(img.Digest)); err != nil {
		m.logger.Warn("failed to delete image files", "digest", img.Digest, "error", err)
	}
	m.record(img, dicer.ActionDeleted, fmt.Sprintf("Deleted image %s (%s): %s boot disk removed",
		img.Name, shortDigest(img.Digest), humanSize(img.SizeBytes)), map[string]string{"by": "user"})

	return nil
}

// ensureImageReady loads an image from disk or pulls it. It runs as a
// shared pull, so at most once concurrently per digest.
func (m *Manager) ensureImageReady(
	ctx context.Context, resolved *reference.ResolvedRef, onProgress ProgressFunc,
) (*dicer.Image, error) {
	digest := resolved.ManifestDigest()
	digestHex := resolved.DigestHex()

	// Another goroutine may have finished pulling it while this one queued.
	if img, ok := m.index.get(digest); ok {
		return img, nil
	}

	// The disk may survive from a previous run whose metadata failed to load
	// at startup.
	if m.diskExists(digestHex) {
		img, err := m.loadImageFromDisk(digestHex)
		if err != nil {
			m.logger.Warn("failed to load existing image, will re-pull",
				"digest", digestHex, "error", err)
		} else {
			// Asked for again: that is a use.
			img.LastUsedAt = time.Now()
			if err := m.index.create(img); err == nil || errors.Is(err, dicer.ErrExists) {
				return img, nil
			}
		}
	}

	if err := m.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("acquire pull slot: %w", err)
	}
	defer m.sem.Release(1)

	// The record is only created once the image is usable, so a failed pull
	// leaves nothing behind for List to report.
	if err := m.executePull(ctx, resolved, onProgress); err != nil {
		return nil, err
	}

	img, ok := m.index.get(digest)
	if !ok {
		return nil, dicer.NotFound("no image %s", digest)
	}

	return img, nil
}

// executePull downloads an image, unpacks it and converts it to a disk.
func (m *Manager) executePull(
	ctx context.Context, resolved *reference.ResolvedRef, onProgress ProgressFunc,
) (err error) {
	digest := resolved.ManifestDigest()
	digestHex := resolved.DigestHex()

	// Downloaded bytes are counted from the registry's own events, so the
	// number reported is what crossed the network rather than the size of
	// the disk it became.
	var downloaded downloadCounter
	started := time.Now()
	defer func() { m.metrics.RecordImagePull(err, time.Since(started), downloaded.total()) }()

	m.logger.InfoContext(ctx, "pulling image", "ref", resolved.String(), "digest", digest)

	if err := m.ensureImageDir(digestHex); err != nil {
		return m.handlePullError(digest, digestHex, err)
	}
	if err := m.ensureTmpDir(digestHex); err != nil {
		return m.handlePullError(digest, digestHex, err)
	}
	defer func() {
		if err := m.cleanupTmpDir(digestHex); err != nil {
			m.logger.Warn("failed to clean up tmp dir", "digest", digestHex, "error", err)
		}
	}()

	tmpRootfs := m.tmpRootfsPath(digestHex)
	onEvent := downloaded.tap(onProgress.fromRegistry())
	result, err := m.registry.PullAndExport(ctx, resolved.String(), digest, tmpRootfs, onEvent)
	if err != nil {
		return m.handlePullError(digest, digestHex, &PullError{Ref: resolved.String(), Cause: err})
	}

	onProgress.send(dicer.PullProgress{Stage: dicer.StageConverting})

	diskPath := m.diskPath(digestHex)
	convertStarted := time.Now()
	sizeBytes, err := m.packer.Pack(ctx, tmpRootfs, diskPath)
	m.metrics.RecordImageConversion(time.Since(convertStarted))
	if err != nil {
		return m.handlePullError(digest, digestHex, &ConvertError{Digest: digest, Format: "erofs", Cause: err})
	}

	now := time.Now()
	img := &dicer.Image{
		Name:       resolved.String(),
		Digest:     digest,
		DiskPath:   diskPath,
		SizeBytes:  sizeBytes,
		Entrypoint: result.Metadata.Entrypoint,
		Cmd:        result.Metadata.Cmd,
		Env:        result.Metadata.Env,
		WorkingDir: result.Metadata.WorkingDir,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastUsedAt: now,
	}

	// An image whose HEALTHCHECK Dicer cannot run is still an image: it is
	// pulled without one.
	if img.HealthCheck, err = healthCheckFromDocker(result.Metadata.Healthcheck); err != nil {
		m.logger.WarnContext(ctx, "ignoring the image's health check", "ref", resolved.String(), "error", err)
	}
	if err := m.index.create(img); err != nil && !errors.Is(err, dicer.ErrExists) {
		return err
	}

	if err := m.saveMetadata(digestHex, img); err != nil {
		m.logger.Warn("failed to save metadata", "digest", digestHex, "error", err)
	}

	fetched := "layers already cached"
	if n := downloaded.total(); n > 0 {
		fetched = "downloaded " + humanSize(n)
	}
	m.record(img, dicer.ActionPulled, fmt.Sprintf("Pulled image %s (%s) in %s: %s, %s boot disk",
		resolved.String(), shortDigest(digest), roundDuration(time.Since(started)), fetched, humanSize(sizeBytes)),
		map[string]string{"size_bytes": strconv.FormatInt(sizeBytes, 10)})
	m.logger.InfoContext(ctx, "image ready",
		"ref", resolved.String(),
		"digest", digest,
		"size", sizeBytes,
		"path", diskPath)

	return nil
}

// handlePullError logs the failure and removes any partial files. No record
// is kept: an image that failed to pull does not exist.
func (m *Manager) handlePullError(digest, digestHex string, err error) error {
	m.logger.Error("pull failed", "digest", digest, "error", err)

	if cleanupErr := m.deleteImage(digestHex); cleanupErr != nil {
		m.logger.Warn("failed to clean up partial image", "error", cleanupErr)
	}

	return err
}

// loadImageFromDisk loads an image's recorded metadata, checking its disk
// is still present.
func (m *Manager) loadImageFromDisk(digestHex string) (*dicer.Image, error) {
	img, err := m.loadMetadata(digestHex)
	if err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}

	if _, err := os.Stat(img.DiskPath); err != nil {
		return nil, fmt.Errorf("disk file missing: %w", err)
	}

	return img, nil
}

// loadExistingImages indexes every image already on disk.
func (m *Manager) loadExistingImages() error {
	digestHexes, err := m.listImageDirs()
	if err != nil {
		return err
	}

	for _, digestHex := range digestHexes {
		img, err := m.loadImageFromDisk(digestHex)
		if err != nil {
			m.logger.Warn("failed to load image", "digest", digestHex, "error", err)
			continue
		}

		// An image recorded before its use was is counted as used now, so
		// that garbage collection does not take it for one unused forever.
		if img.LastUsedAt.IsZero() {
			img.LastUsedAt = time.Now()
			if err := m.saveMetadata(digestHex, img); err != nil {
				m.logger.Warn("failed to save metadata", "digest", digestHex, "error", err)
			}
		}

		if err := m.index.create(img); err != nil {
			m.logger.Warn("failed to add image to index", "digest", digestHex, "error", err)
			continue
		}

		m.logger.Debug("loaded existing image", "digest", img.Digest, "name", img.Name)
	}

	return nil
}

// shortDigest abbreviates a digest for a person, as git does a commit:
// "sha256:1a2b3c4d5e6f".
func shortDigest(digest string) string {
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok || len(hex) <= 12 {
		return digest
	}
	return algo + ":" + hex[:12]
}

// digestHex returns a digest without its algorithm prefix.
func digestHex(digest string) string {
	if _, hex, ok := strings.Cut(digest, ":"); ok {
		return hex
	}
	return digest
}

// roundDuration is d rounded as an event says it: to the millisecond under
// a second, to a tenth of a second after.
func roundDuration(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
}

// humanSize is a size in bytes as a person reads it, in the binary units
// sizes are given in: 5.01 MiB.
func humanSize(n int64) string {
	return units.CustomSize("%.3g %s", float64(n), 1024, []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"})
}
