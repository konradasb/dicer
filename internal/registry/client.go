// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package registry talks to OCI registries: resolving references, pulling
// manifests and layers, and exporting an unpacked root filesystem.
package registry

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	gcr "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ispec "github.com/opencontainers/image-spec/specs-go/v1"
	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/opencontainers/umoci/oci/cas/dir"
	"github.com/opencontainers/umoci/oci/casext"
	"github.com/opencontainers/umoci/oci/layer"

	"github.com/konradasb/dicer/internal/image/reference"
)

// Option configures a Client.
type Option func(*Client)

// WithLogger sets the logger for the client.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		c.logger = l
	}
}

// WithKeychain sets the credentials registries are logged in to with. Without
// it, every image is pulled anonymously.
func WithKeychain(k authn.Keychain) Option {
	return func(c *Client) {
		c.keychain = k
	}
}

// Client handles OCI image registry operations and local caching.
type Client struct {
	cacheDir string
	platform gcr.Platform
	keychain authn.Keychain
	layoutMu sync.Mutex
	logger   *slog.Logger
}

// PullResult contains the results of a successful image pull.
type PullResult struct {
	Metadata *Metadata
	Digest   string
}

// Metadata contains container image metadata.
type Metadata struct {
	Entrypoint []string
	Cmd        []string
	Env        map[string]string
	WorkingDir string
	// Healthcheck is the image's HEALTHCHECK as Docker records it, or nil.
	Healthcheck *gcr.HealthConfig
}

// NewClient creates a new registry client.
func NewClient(dataDir string, opts ...Option) (*Client, error) {
	cacheDir := filepath.Join(dataDir, "oci-cache")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	c := &Client{
		cacheDir: cacheDir,
		platform: gcr.Platform{OS: "linux", Architecture: runtime.GOARCH},
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.keychain == nil {
		c.keychain = NewKeychain(nil)
	}

	setUmociLogger(c.logger)

	return c, nil
}

// Resolve returns the manifest digest a reference currently points to. It
// implements reference.Resolver.
func (c *Client) Resolve(ctx context.Context, ref *reference.Ref) (string, error) {
	return c.inspectManifest(ctx, ref.String())
}

// inspectManifest fetches the manifest digest for an image reference.
func (c *Client) inspectManifest(ctx context.Context, imageRef string) (string, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("parse reference: %w", err)
	}

	img, err := remote.Image(ref,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(c.keychain),
		remote.WithPlatform(c.platform))
	if err != nil {
		return "", fmt.Errorf("fetch manifest: %w", err)
	}

	digest, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("get digest: %w", err)
	}

	return digest.String(), nil
}

// PullAndExport pulls an image and exports its layers to a directory,
// reporting progress to onEvent, which may be nil.
func (c *Client) PullAndExport(
	ctx context.Context, imageRef, digest, exportDir string, onEvent EventFunc,
) (*PullResult, error) {
	if digest == "" {
		return nil, errors.New("digest is required")
	}
	if exportDir == "" {
		return nil, errors.New("export directory is required")
	}

	layoutTag, err := digestToLayoutTag(digest)
	if err != nil {
		return nil, fmt.Errorf("invalid digest: %w", err)
	}

	// Held until the layers are unpacked: PruneCache would otherwise be
	// free to drop an image fetched here but not yet in use, from under
	// the unpacking.
	c.layoutMu.Lock()
	defer c.layoutMu.Unlock()

	exists, err := c.existsInLayout(ctx, layoutTag)
	if err != nil {
		return nil, fmt.Errorf("check cache: %w", err)
	}

	if !exists {
		if err := c.pullToOCILayout(ctx, imageRef, digest, layoutTag, onEvent); err != nil {
			return nil, fmt.Errorf("pull image: %w", err)
		}
	}

	// Extract metadata
	meta, err := c.metadata(layoutTag)
	if err != nil {
		return nil, fmt.Errorf("extract metadata: %w", err)
	}

	// Unpack layers to export directory
	onEvent.send(Event{Phase: PhaseUnpacking})
	if err := c.unpackLayers(ctx, layoutTag, exportDir); err != nil {
		return nil, fmt.Errorf("unpack layers: %w", err)
	}

	return &PullResult{
		Metadata: meta,
		Digest:   digest,
	}, nil
}

// metadata reads the container configuration of a cached image.
func (c *Client) metadata(layoutTag string) (*Metadata, error) {
	path, err := layout.FromPath(c.cacheDir)
	if err != nil {
		return nil, fmt.Errorf("open layout: %w", err)
	}

	img, err := c.imageByAnnotation(path, layoutTag)
	if err != nil {
		return nil, err
	}

	configFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	return &Metadata{
		Entrypoint:  configFile.Config.Entrypoint,
		Cmd:         configFile.Config.Cmd,
		Env:         parseEnvVars(configFile.Config.Env),
		WorkingDir:  configFile.Config.WorkingDir,
		Healthcheck: configFile.Config.Healthcheck,
	}, nil
}

// pullToOCILayout pulls the image with the given manifest digest from
// imageRef's repository into the OCI layout cache. It pulls by digest since
// the tag may have moved. The caller must hold layoutMu.
func (c *Client) pullToOCILayout(ctx context.Context, imageRef, digest, layoutTag string, onEvent EventFunc) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parse reference: %w", err)
	}

	img, err := remote.Image(ref.Context().Digest(digest),
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(c.keychain),
		remote.WithPlatform(c.platform))
	if err != nil {
		return fmt.Errorf("fetch image: %w", err)
	}

	path, err := layout.FromPath(c.cacheDir)
	if err != nil {
		path, err = layout.Write(c.cacheDir, empty.Index)
		if err != nil {
			return fmt.Errorf("create layout: %w", err)
		}
	}

	// Only the layers that are not already in the cache will be fetched, so
	// they alone are what the progress is measured against.
	total, err := c.bytesToFetch(img)
	if err != nil {
		return err
	}

	var downloaded counter
	onEvent.send(Event{Phase: PhaseDownloading, Total: total})
	progress := progressImage{Image: img, report: func(n int64) {
		onEvent.send(Event{Phase: PhaseDownloading, Downloaded: downloaded.add(n), Total: total})
	}}

	err = path.AppendImage(progress, layout.WithAnnotations(map[string]string{
		"org.opencontainers.image.ref.name": layoutTag,
	}))
	if err != nil {
		return fmt.Errorf("write image: %w", err)
	}

	return nil
}

// bytesToFetch is the compressed size of the layers this pull will actually
// download: a layer already in the cache is written from there, and counting
// it would leave the progress short of its total.
func (c *Client) bytesToFetch(img gcr.Image) (int64, error) {
	layers, err := img.Layers()
	if err != nil {
		return 0, fmt.Errorf("read layers: %w", err)
	}

	var total int64
	for _, l := range layers {
		digest, err := l.Digest()
		if err != nil {
			return 0, fmt.Errorf("read layer digest: %w", err)
		}
		if _, err := os.Stat(c.blobPath(digest)); err == nil {
			continue
		}

		size, err := l.Size()
		if err != nil {
			return 0, fmt.Errorf("read layer size: %w", err)
		}
		total += size
	}

	return total, nil
}

// blobPath is where the OCI layout keeps a blob.
func (c *Client) blobPath(digest gcr.Hash) string {
	return filepath.Join(c.cacheDir, "blobs", digest.Algorithm, digest.Hex)
}

// existsInLayout checks if an image exists in the local OCI cache.
func (c *Client) existsInLayout(ctx context.Context, layoutTag string) (bool, error) {
	// If the cache dir doesn't have an OCI layout yet, nothing is cached
	if _, err := os.Stat(filepath.Join(c.cacheDir, "oci-layout")); err != nil {
		return false, nil
	}

	casEngine, err := dir.Open(c.cacheDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("open cache: %w", err)
	}
	defer func() { _ = casEngine.Close() }()

	engine := casext.NewEngine(casEngine)
	descriptorPaths, err := engine.ResolveReference(ctx, layoutTag)
	if err != nil {
		return false, nil
	}

	return len(descriptorPaths) > 0, nil
}

// unpackLayers extracts image layers to a target directory.
func (c *Client) unpackLayers(ctx context.Context, layoutTag, targetDir string) error {
	path, err := layout.FromPath(c.cacheDir)
	if err != nil {
		return fmt.Errorf("open layout: %w", err)
	}

	img, err := c.imageByAnnotation(path, layoutTag)
	if err != nil {
		return err
	}

	gcrManifest, err := img.Manifest()
	if err != nil {
		return fmt.Errorf("get manifest: %w", err)
	}

	casEngine, err := dir.Open(c.cacheDir)
	if err != nil {
		return fmt.Errorf("open layout: %w", err)
	}
	defer func() { _ = casEngine.Close() }()

	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	unpackOpts := &layer.UnpackOptions{
		OnDiskFormat: layer.DirRootfs{
			MapOptions: layer.MapOptions{
				Rootless: true,
				UIDMappings: []rspec.LinuxIDMapping{
					{HostID: uint32(os.Getuid()), ContainerID: 0, Size: 1},
				},
				GIDMappings: []rspec.LinuxIDMapping{
					{HostID: uint32(os.Getgid()), ContainerID: 0, Size: 1},
				},
			},
		},
	}

	ociManifest := convertToOCIManifest(gcrManifest)
	if err := layer.UnpackRootfs(ctx, casEngine, targetDir, ociManifest, unpackOpts); err != nil {
		return fmt.Errorf("unpack rootfs: %w", err)
	}

	return nil
}

// imageByAnnotation finds an image in the layout by annotation tag.
func (c *Client) imageByAnnotation(path layout.Path, layoutTag string) (gcr.Image, error) {
	index, err := path.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("get index: %w", err)
	}

	indexManifest, err := index.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("get manifest: %w", err)
	}

	for _, desc := range indexManifest.Manifests {
		if refName := desc.Annotations["org.opencontainers.image.ref.name"]; refName == layoutTag {
			return path.Image(desc.Digest)
		}
	}

	return nil, fmt.Errorf("no image %s in the layout", layoutTag)
}

// digestToLayoutTag converts a digest to a layout tag.
func digestToLayoutTag(d string) (string, error) {
	parts := strings.SplitN(d, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("invalid digest %q", d)
	}
	return parts[1], nil
}

// parseEnvVars converts environment variable list to a map.
func parseEnvVars(envList []string) map[string]string {
	env := make(map[string]string, len(envList))
	for _, e := range envList {
		key, val, _ := strings.Cut(e, "=")
		env[key] = val
	}
	return env
}

// convertToOCIManifest converts go-containerregistry manifest to OCI spec manifest.
func convertToOCIManifest(gcrManifest *gcr.Manifest) ispec.Manifest {
	layers := make([]ispec.Descriptor, len(gcrManifest.Layers))
	for i, layer := range gcrManifest.Layers {
		layers[i] = ispec.Descriptor{
			MediaType:   convertToOCIMediaType(string(layer.MediaType)),
			Digest:      convertToOCIDigest(layer.Digest),
			Size:        layer.Size,
			Annotations: layer.Annotations,
		}
	}

	return ispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: int(gcrManifest.SchemaVersion)},
		MediaType: convertToOCIMediaType(string(gcrManifest.MediaType)),
		Config: ispec.Descriptor{
			MediaType:   convertToOCIMediaType(string(gcrManifest.Config.MediaType)),
			Digest:      convertToOCIDigest(gcrManifest.Config.Digest),
			Size:        gcrManifest.Config.Size,
			Annotations: gcrManifest.Config.Annotations,
		},
		Layers:      layers,
		Annotations: gcrManifest.Annotations,
	}
}

// convertToOCIMediaType converts Docker media types to OCI equivalents.
func convertToOCIMediaType(mediaType string) string {
	switch mediaType {
	case "application/vnd.docker.distribution.manifest.v2+json":
		return ispec.MediaTypeImageManifest
	case "application/vnd.docker.container.image.v1+json":
		return ispec.MediaTypeImageConfig
	case "application/vnd.docker.image.rootfs.diff.tar.gzip":
		return ispec.MediaTypeImageLayerGzip
	case "application/vnd.docker.image.rootfs.diff.tar":
		return ispec.MediaTypeImageLayer
	default:
		return mediaType
	}
}

// convertToOCIDigest converts go-containerregistry digest to opencontainers digest.
func convertToOCIDigest(d gcr.Hash) digest.Digest {
	return digest.NewDigestFromEncoded(digest.Algorithm(d.Algorithm), d.Hex)
}
