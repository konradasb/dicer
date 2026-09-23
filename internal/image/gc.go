// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// GCPolicy decides when unused images are removed: once unused for longer
// than MaxUnusedAge, and while the store exceeds MaxSize. The zero policy
// removes nothing.
type GCPolicy struct {
	// MaxUnusedAge is how long an image may go unused. Zero means no
	// limit.
	MaxUnusedAge time.Duration

	// MaxSize is what the image store -- the bootable disks and the layer
	// cache -- may occupy. Over it, the least recently used images go
	// first. Zero means no limit.
	MaxSize int64
}

// Enabled reports whether the policy ever removes anything.
func (p GCPolicy) Enabled() bool {
	return p.MaxUnusedAge > 0 || p.MaxSize > 0
}

// GCReason is why garbage collection removed an image.
type GCReason string

// The reasons an image is collected.
const (
	// GCReasonUnused is an image unused for longer than MaxUnusedAge.
	GCReasonUnused GCReason = "unused"
	// GCReasonSize is an image removed to bring the store under MaxSize.
	GCReasonSize GCReason = "size"
)

// gcGracePeriod protects recently used images, such as one pulled for an
// instance not yet created.
const gcGracePeriod = 10 * time.Minute

// GCResult is what a garbage collection pass removed.
type GCResult struct {
	Removed        []GCRemoval
	ReclaimedBytes int64
}

// GCRemoval is one image garbage collection removed, and why.
type GCRemoval struct {
	Image  *types.Image
	Reason GCReason
}

// expired returns the images of images not in use and unused for longer than
// the policy allows at now.
func (p GCPolicy) expired(images []*types.Image, inUse map[string]struct{}, now time.Time) []*types.Image {
	if p.MaxUnusedAge <= 0 {
		return nil
	}

	var out []*types.Image
	for _, img := range collectable(images, inUse, now) {
		if now.Sub(img.LastUsedAt) > p.MaxUnusedAge {
			out = append(out, img)
		}
	}
	return out
}

// collectable returns the images of images garbage collection may remove at
// now -- not in use, nor used within gcGracePeriod -- least recently used
// first.
func collectable(images []*types.Image, inUse map[string]struct{}, now time.Time) []*types.Image {
	var out []*types.Image
	for _, img := range images {
		if _, used := inUse[img.Digest]; used || now.Sub(img.LastUsedAt) < gcGracePeriod {
			continue
		}
		out = append(out, img)
	}

	slices.SortFunc(out, func(a, b *types.Image) int {
		return cmp.Or(a.LastUsedAt.Compare(b.LastUsedAt), cmp.Compare(a.Digest, b.Digest))
	})
	return out
}

// CollectGarbage applies the policy once to the images not in inUse, and
// marks those in inUse as used now. Stale images go first, then the least
// recently used while the store is too large, re-measuring after each since
// layers may be shared.
func (m *Manager) CollectGarbage(p GCPolicy, inUse map[string]struct{}, now time.Time) (GCResult, error) {
	var result GCResult

	for digest := range inUse {
		m.markUsed(digest, now)
	}

	if err := m.collect(p, p.expired(m.index.list(), inUse, now), GCReasonUnused, &result); err != nil {
		return result, err
	}

	if p.MaxSize <= 0 {
		return result, nil
	}
	for _, img := range collectable(m.index.list(), inUse, now) {
		size, err := m.storeSize()
		if err != nil {
			return result, err
		}
		if size <= p.MaxSize {
			break
		}
		if err := m.collect(p, []*types.Image{img}, GCReasonSize, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// collect removes images for reason, under p, adding them to result.
func (m *Manager) collect(p GCPolicy, images []*types.Image, reason GCReason, result *GCResult) error {
	if len(images) == 0 {
		return nil
	}

	removed, err := m.remove(images)
	for _, img := range removed.Images {
		result.Removed = append(result.Removed, GCRemoval{Image: &img, Reason: reason})
		m.metrics.RecordImageCollected(string(reason))
		m.record(&img, types.ActionCollected, gcMessage(p, &img, reason), map[string]string{"reason": string(reason)})
	}
	result.ReclaimedBytes += removed.ReclaimedBytes
	m.metrics.RecordImageGCReclaimed(removed.ReclaimedBytes)
	return err
}

// gcMessage says why garbage collection under p removed img.
func gcMessage(p GCPolicy, img *types.Image, reason GCReason) string {
	removed := fmt.Sprintf("Garbage-collected image %s (%s)", img.Name, shortDigest(img.Digest))
	lastUsed := img.LastUsedAt.Local().Format(time.DateTime)
	if reason == GCReasonSize {
		return fmt.Sprintf("%s: image store over gc_max_size %s, and it was the least recently used (last used %s); %s boot disk removed",
			removed, humanSize(p.MaxSize), lastUsed, humanSize(img.SizeBytes))
	}
	return fmt.Sprintf("%s: unused since %s, longer than gc_max_unused_age %s; %s boot disk removed",
		removed, lastUsed, age(p.MaxUnusedAge), humanSize(img.SizeBytes))
}

// age is a duration as the configuration gives it: in days if it is whole
// days, "30d", or as Go writes it, trimmed, "36h" rather than "36h0m0s".
func age(d time.Duration) string {
	const day = 24 * time.Hour
	if d > 0 && d%day == 0 {
		return strconv.FormatInt(int64(d/day), 10) + "d"
	}
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// markUsed records that the image with digest was in use at at.
func (m *Manager) markUsed(digest string, at time.Time) {
	img, ok := m.index.markUsed(digest, at)
	if !ok {
		return
	}
	if err := m.saveMetadata(digestHex(digest), img); err != nil {
		m.logger.Warn("failed to save metadata", "digest", digest, "error", err)
	}
}

// storeSize returns what the image store occupies: the bootable disks, and
// the layer cache.
func (m *Manager) storeSize() (int64, error) {
	total, err := m.registry.CacheSize()
	if err != nil {
		return 0, fmt.Errorf("measure layer cache: %w", err)
	}
	for _, img := range m.index.list() {
		total += img.SizeBytes
	}
	return total, nil
}

// RunGC applies the policy now, and every interval after, until ctx is done.
// inUse reports the digests of the images in use; a pass that cannot learn
// them removes nothing, since without them any image could be one in use.
func (m *Manager) RunGC(
	ctx context.Context, p GCPolicy, interval time.Duration, inUse func() (map[string]struct{}, error),
) {
	m.logger.InfoContext(ctx, "image garbage collection enabled",
		"max_unused_age", p.MaxUnusedAge, "max_size", p.MaxSize, "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		m.gcPass(ctx, p, inUse)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// gcPass is one pass of RunGC.
func (m *Manager) gcPass(ctx context.Context, p GCPolicy, inUse func() (map[string]struct{}, error)) {
	keep, err := inUse()
	if err != nil {
		m.logger.WarnContext(ctx, "image garbage collection skipped: cannot tell which images are in use",
			"error", err)
		return
	}

	result, err := m.CollectGarbage(p, keep, time.Now())
	for _, r := range result.Removed {
		m.logger.InfoContext(ctx, "image collected", "ref", r.Image.Name, "digest", r.Image.Digest,
			"reason", r.Reason, "last_used", r.Image.LastUsedAt)
	}
	if len(result.Removed) > 0 {
		m.logger.InfoContext(ctx, "image garbage collection reclaimed space",
			"images", len(result.Removed), "bytes", result.ReclaimedBytes)
	}
	if err != nil {
		m.logger.WarnContext(ctx, "image garbage collection failed", "error", err)
	}
}
