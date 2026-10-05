// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package image pulls container images and converts them into read-only EROFS
// disks a guest boots from. Images are stored by manifest digest.
package image

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/registry"
	"github.com/konradasb/dicer/internal/types"
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

	// Metrics records pulls and garbage collection. Optional: when nil,
	// they are not recorded.
	Metrics Metrics

	// Events records what happens to images. Optional: when nil, it is not
	// recorded.
	Events Recorder

	// Logger receives the Manager's logs. Optional: when nil, slog.Default.
	Logger *slog.Logger
}

// registryClient is what the Manager needs of a registry: satisfied by
// registry.Client.
type registryClient interface {
	reference.Resolver

	// PullAndExport fetches the image with the manifest digest into the
	// layer cache, unless it is there already, unpacks its root filesystem
	// into exportDir and returns its metadata.
	PullAndExport(
		ctx context.Context, imageRef, digest, exportDir string, onProgress registry.ProgressFunc,
	) (*registry.Metadata, error)

	// PruneCache drops every image from the layer cache but those keep
	// names, by their manifest digests' hex, and returns the bytes
	// reclaimed.
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

	// RecordImageGCCollected records an image garbage collection removed,
	// and why: GCReasonUnused or GCReasonSize.
	RecordImageGCCollected(reason string)

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
func (discardMetrics) RecordImageGCCollected(string)               {}
func (discardMetrics) RecordImageGCReclaimed(int64)                {}

// packer packs a directory tree into a filesystem image at outputPath, and
// returns the image's size in bytes.
type packer interface {
	Pack(ctx context.Context, dir, outputPath string) (int64, error)
}

// Manager holds the images this host has pulled, keyed by manifest digest.
// It is safe for concurrent use.
type Manager struct {
	dataDir   string
	index     *index
	pullSlots *semaphore.Weighted // bounds concurrent pulls of different images
	registry  registryClient
	packer    packer
	metrics   Metrics
	events    Recorder
	logger    *slog.Logger

	// mu guards pulls: the pulls under way, by manifest digest.
	mu    sync.Mutex
	pulls map[string]*pull
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
		cfg.Events = discardRecorder{}
	}

	m := &Manager{
		dataDir:   cfg.DataDir,
		index:     newIndex(),
		pullSlots: semaphore.NewWeighted(int64(cfg.MaxConcurrentPulls)),
		registry:  cfg.Registry,
		packer:    erofs{},
		metrics:   cfg.Metrics,
		events:    cfg.Events,
		logger:    cfg.Logger.With("component", "image"),
		pulls:     make(map[string]*pull),
	}

	if err := m.createDirs(); err != nil {
		return nil, fmt.Errorf("initialise image storage: %w", err)
	}

	if err := m.loadExistingImages(); err != nil {
		m.logger.Warn("failed to load existing images", "error", err)
	}

	return m, nil
}

// Pull resolves a reference against its registry and returns the image,
// pulling and converting it if needed. Concurrent pulls of the same image
// share one download.
func (m *Manager) Pull(ctx context.Context, ref string, onProgress ProgressFunc) (*types.Image, error) {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}

	onProgress.report(types.PullProgress{Stage: types.PullStageResolving})

	resolved, err := reference.Resolve(ctx, m.registry, parsed)
	if err != nil {
		return nil, &PullError{Ref: ref, Cause: err}
	}

	digest := resolved.Digest()
	image, hit := m.index.get(digest)
	m.metrics.RecordImageCacheLookup(hit)
	if hit {
		// Pulled again, though nothing was fetched: that is a use.
		m.markUsed(digest, time.Now())
		return image, nil
	}

	// Callers pulling the same image share one download; see sharedPull.
	return m.sharedPull(ctx, resolved, onProgress)
}

// Image returns the locally held image ref names, without consulting a
// registry. A tag names the image most recently pulled under it. An image
// the host does not hold is an errdefs.ErrNotFound error.
func (m *Manager) Image(ref string) (*types.Image, error) {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidReference, err)
	}

	var (
		image *types.Image
		ok    bool
	)
	if parsed.HasDigest() {
		image, ok = m.index.get(parsed.Digest())
	} else {
		image, ok = m.index.latestByName(parsed.String())
	}
	if !ok {
		return nil, errdefs.NotFound("no image %q", ref)
	}

	return image, nil
}

// Ensure returns the image ref names for an instance to boot from, pulling
// it as policy says. It marks the image used, so that garbage collection
// spares it until the instance is defined. With PullPolicyNever, an image
// the host does not hold is an errdefs.ErrNotFound error.
func (m *Manager) Ensure(ctx context.Context, ref string, policy types.PullPolicy) (*types.Image, error) {
	if policy == types.PullPolicyAlways {
		return m.Pull(ctx, ref, nil)
	}

	image, err := m.Image(ref)
	switch {
	case errors.Is(err, errdefs.ErrNotFound) && policy == types.PullPolicyNever:
		return nil, errdefs.NotFound("image %q is not on this host, and the pull policy is never: pull it first", ref)
	case errors.Is(err, errdefs.ErrNotFound):
		return m.Pull(ctx, ref, nil)
	case err != nil:
		return nil, err
	}

	m.markUsed(image.Digest, time.Now())

	return image, nil
}

// List returns every locally held image.
func (m *Manager) List() []*types.Image {
	return m.index.list()
}

// Delete removes a locally held image and its disk. Like Image, it does not
// consult a registry: deleting a tag removes the image pulled under it, even
// if the tag has since moved.
func (m *Manager) Delete(ref string) error {
	image, err := m.Image(ref)
	if err != nil {
		return err
	}

	if err := m.index.delete(image.Digest); err != nil && !errors.Is(err, errdefs.ErrNotFound) {
		return err
	}

	if err := m.deleteFiles(digestHex(image.Digest)); err != nil {
		m.logger.Warn("failed to delete image files", "digest", image.Digest, "error", err)
	}
	m.record(image, events.ActionDeleted, fmt.Sprintf("Deleted image %s (%s): %s boot disk removed",
		image.Name, reference.ShortDigest(image.Digest), humanize.Bytes(image.SizeBytes)), map[string]string{"by": "user"})

	return nil
}

// loadOrPull loads an image from disk or pulls it. It runs as a shared
// pull, so at most once concurrently per digest.
func (m *Manager) loadOrPull(
	ctx context.Context, resolved *reference.ResolvedRef, onProgress ProgressFunc,
) (*types.Image, error) {
	digest := resolved.Digest()
	digestHex := resolved.DigestHex()

	// Another goroutine may have finished pulling it while this one queued.
	if image, ok := m.index.get(digest); ok {
		return image, nil
	}

	// The disk may survive from a previous run whose metadata failed to load
	// at startup.
	if m.diskExists(digestHex) {
		image, err := m.loadFromDisk(digestHex)
		if err != nil {
			m.logger.Warn("failed to load existing image, will re-pull",
				"digest", digest, "error", err)
		} else {
			// Asked for again: that is a use.
			image.LastUsedAt = time.Now()
			if err := m.index.create(image); err == nil || errors.Is(err, errdefs.ErrExists) {
				return image, nil
			}
		}
	}

	if err := m.pullSlots.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("acquire pull slot: %w", err)
	}
	defer m.pullSlots.Release(1)

	// The record is only created once the image is usable, so a failed pull
	// leaves nothing behind for List to report.
	if err := m.pullFromRegistry(ctx, resolved, onProgress); err != nil {
		return nil, err
	}

	image, ok := m.index.get(digest)
	if !ok {
		return nil, errdefs.NotFound("no image %s", digest)
	}

	return image, nil
}

// pullFromRegistry downloads an image, unpacks it and packs it into a disk.
func (m *Manager) pullFromRegistry(
	ctx context.Context, resolved *reference.ResolvedRef, onProgress ProgressFunc,
) (err error) {
	digest := resolved.Digest()
	digestHex := resolved.DigestHex()

	// Downloaded bytes are counted from the registry's own progress, so the
	// number reported is what crossed the network rather than the size of
	// the disk it became.
	var downloaded downloadCounter
	started := time.Now()
	defer func() { m.metrics.RecordImagePull(err, time.Since(started), downloaded.total()) }()

	m.logger.InfoContext(ctx, "pulling image", "ref", resolved.String(), "digest", digest)

	if err := m.ensureImageDir(digestHex); err != nil {
		return m.discardFailedPull(digestHex, err)
	}
	if err := m.ensureRootfsDir(digestHex); err != nil {
		return m.discardFailedPull(digestHex, err)
	}
	defer func() {
		if err := m.removeUnpackDir(digestHex); err != nil {
			m.logger.Warn("failed to remove unpacked image", "digest", digest, "error", err)
		}
	}()

	rootfsDir := m.rootfsDir(digestHex)
	onRegistryProgress := downloaded.tap(onProgress.fromRegistry())
	metadata, err := m.registry.PullAndExport(ctx, resolved.String(), digest, rootfsDir, onRegistryProgress)
	if err != nil {
		return m.discardFailedPull(digestHex, &PullError{Ref: resolved.String(), Cause: err})
	}

	onProgress.report(types.PullProgress{Stage: types.PullStageConverting})

	diskPath := m.diskPath(digestHex)
	convertStarted := time.Now()
	sizeBytes, err := m.packer.Pack(ctx, rootfsDir, diskPath)
	m.metrics.RecordImageConversion(time.Since(convertStarted))
	if err != nil {
		return m.discardFailedPull(digestHex, &ConvertError{Digest: digest, Format: "erofs", Cause: err})
	}

	now := time.Now()
	image := &types.Image{
		Name:       resolved.String(),
		Digest:     digest,
		DiskPath:   diskPath,
		SizeBytes:  sizeBytes,
		Entrypoint: metadata.Entrypoint,
		Cmd:        metadata.Cmd,
		Env:        metadata.Env,
		WorkingDir: metadata.WorkingDir,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastUsedAt: now,
	}

	// An image whose HEALTHCHECK Dicer cannot run is still an image: it is
	// pulled without one.
	if image.HealthCheck, err = healthCheckFromDocker(metadata.HealthCheck); err != nil {
		m.logger.WarnContext(ctx, "ignoring the image's health check", "ref", resolved.String(), "error", err)
	}
	if err := m.index.create(image); err != nil && !errors.Is(err, errdefs.ErrExists) {
		return err
	}

	if err := m.saveMetadata(digestHex, image); err != nil {
		m.logger.Warn("failed to save metadata", "digest", digest, "error", err)
	}

	fetched := "layers already cached"
	if n := downloaded.total(); n > 0 {
		fetched = "downloaded " + humanize.Bytes(n)
	}
	m.record(image, events.ActionPulled, fmt.Sprintf("Pulled image %s (%s) in %s: %s, %s boot disk",
		resolved.String(), reference.ShortDigest(digest), humanize.Duration(time.Since(started)), fetched, humanize.Bytes(sizeBytes)),
		map[string]string{"size_bytes": strconv.FormatInt(sizeBytes, 10)})
	m.logger.InfoContext(ctx, "image ready",
		"ref", resolved.String(),
		"digest", digest,
		"size_bytes", sizeBytes,
		"path", diskPath)

	return nil
}

// discardFailedPull removes what a failed pull wrote, and returns err. No
// record is kept: an image that failed to pull does not exist.
func (m *Manager) discardFailedPull(digestHex string, err error) error {
	if deleteErr := m.deleteFiles(digestHex); deleteErr != nil {
		m.logger.Warn("failed to delete partial image files", "dir", m.imageDir(digestHex), "error", deleteErr)
	}

	return err
}

// loadFromDisk loads an image's recorded metadata, checking its disk is
// still present.
func (m *Manager) loadFromDisk(digestHex string) (*types.Image, error) {
	image, err := m.loadMetadata(digestHex)
	if err != nil {
		return nil, fmt.Errorf("load metadata: %w", err)
	}

	if _, err := os.Stat(image.DiskPath); err != nil {
		return nil, fmt.Errorf("disk file missing: %w", err)
	}

	return image, nil
}

// loadExistingImages indexes every image already on disk.
func (m *Manager) loadExistingImages() error {
	digestHexes, err := m.storedDigestHexes()
	if err != nil {
		return err
	}

	for _, digestHex := range digestHexes {
		image, err := m.loadFromDisk(digestHex)
		if err != nil {
			m.logger.Warn("failed to load image", "dir", m.imageDir(digestHex), "error", err)
			continue
		}

		// An image recorded before its use was is counted as used now, so
		// that garbage collection does not take it for one unused forever.
		if image.LastUsedAt.IsZero() {
			image.LastUsedAt = time.Now()
			if err := m.saveMetadata(digestHex, image); err != nil {
				m.logger.Warn("failed to save metadata", "digest", image.Digest, "error", err)
			}
		}

		if err := m.index.create(image); err != nil {
			m.logger.Warn("failed to add image to index", "digest", image.Digest, "error", err)
			continue
		}

		m.logger.Debug("loaded existing image", "digest", image.Digest, "name", image.Name)
	}

	return nil
}
