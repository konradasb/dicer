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
	"sync/atomic"

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

// Client resolves image references against their registries, and pulls
// images through a layer cache: an OCI layout on disk, in which each image is
// named by its manifest digest's hex. It is safe for concurrent use.
type Client struct {
	// mu serialises pulls with pruning the layer cache, so that neither sees
	// the other's half-written changes to it.
	mu       sync.Mutex
	cacheDir string

	platform gcr.Platform
	keychain authn.Keychain
	logger   *slog.Logger
}

// Metadata is the part of an image's configuration an instance runs with.
type Metadata struct {
	Entrypoint []string
	Cmd        []string
	Env        map[string]string
	WorkingDir string

	// HealthCheck is the image's HEALTHCHECK as Docker records it, or nil.
	HealthCheck *gcr.HealthConfig
}

// NewClient returns a client whose layer cache is under dataDir.
func NewClient(dataDir string, opts ...Option) (*Client, error) {
	cacheDir := filepath.Join(dataDir, "oci-cache")
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("create the layer cache directory: %w", err)
	}

	c := &Client{
		cacheDir: cacheDir,
		platform: gcr.Platform{OS: "linux", Architecture: runtime.GOARCH},
		keychain: Keychain{},
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.logger == nil {
		c.logger = slog.Default()
	}

	setUmociLogger(c.logger)

	return c, nil
}

// Resolve returns the manifest digest a reference currently points to. It
// implements reference.Resolver.
func (c *Client) Resolve(ctx context.Context, ref *reference.Ref) (string, error) {
	parsed, err := name.ParseReference(ref.String())
	if err != nil {
		return "", fmt.Errorf("parse reference: %w", err)
	}

	image, err := remote.Image(parsed,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(c.keychain),
		remote.WithPlatform(c.platform))
	if err != nil {
		return "", fmt.Errorf("fetch manifest: %w", err)
	}

	digest, err := image.Digest()
	if err != nil {
		return "", fmt.Errorf("read digest: %w", err)
	}

	return digest.String(), nil
}

// PullAndExport pulls the image with the manifest digest from imageRef's
// repository into the layer cache, unless it is there already, and unpacks
// its root filesystem into exportDir. It reports progress to onProgress,
// which may be nil, and returns the image's metadata.
func (c *Client) PullAndExport(
	ctx context.Context, imageRef, digest, exportDir string, onProgress ProgressFunc,
) (*Metadata, error) {
	if exportDir == "" {
		return nil, errors.New("export directory is required")
	}
	hex, err := digestHex(digest)
	if err != nil {
		return nil, err
	}

	// Held until the layers are unpacked: PruneCache would otherwise be
	// free to drop an image fetched here but not yet in use, from under
	// the unpacking.
	c.mu.Lock()
	defer c.mu.Unlock()

	cached, err := c.isCached(ctx, hex)
	if err != nil {
		return nil, fmt.Errorf("check the layer cache: %w", err)
	}
	if !cached {
		if err := c.pullToCache(ctx, imageRef, digest, hex, onProgress); err != nil {
			return nil, fmt.Errorf("pull image: %w", err)
		}
	}

	image, err := c.cachedImage(hex)
	if err != nil {
		return nil, err
	}
	metadata, err := metadataOf(image)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	onProgress.report(Progress{Phase: PhaseUnpacking})
	if err := c.unpackLayers(ctx, image, exportDir); err != nil {
		return nil, fmt.Errorf("unpack layers: %w", err)
	}

	return metadata, nil
}

// metadataOf returns the metadata in an image's configuration.
func metadataOf(image gcr.Image) (*Metadata, error) {
	configFile, err := image.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	return &Metadata{
		Entrypoint:  configFile.Config.Entrypoint,
		Cmd:         configFile.Config.Cmd,
		Env:         parseEnv(configFile.Config.Env),
		WorkingDir:  configFile.Config.WorkingDir,
		HealthCheck: configFile.Config.Healthcheck,
	}, nil
}

// pullToCache pulls the image with the manifest digest from imageRef's
// repository into the layer cache, named hex. It pulls by digest since the
// tag may have moved. The caller must hold c.mu.
func (c *Client) pullToCache(ctx context.Context, imageRef, digest, hex string, onProgress ProgressFunc) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parse reference: %w", err)
	}

	image, err := remote.Image(ref.Context().Digest(digest),
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
	total, err := c.bytesToFetch(image)
	if err != nil {
		return err
	}

	var downloaded atomic.Int64 // layers are fetched in parallel
	onProgress.report(Progress{Phase: PhaseDownloading, Total: total})
	counted := countingImage{Image: image, count: func(n int64) {
		onProgress.report(Progress{Phase: PhaseDownloading, Downloaded: downloaded.Add(n), Total: total})
	}}

	err = path.AppendImage(counted, layout.WithAnnotations(map[string]string{
		ispec.AnnotationRefName: hex,
	}))
	if err != nil {
		return fmt.Errorf("write image: %w", err)
	}

	return nil
}

// bytesToFetch is the compressed size of the layers this pull will actually
// download: a layer already in the cache is written from there, and counting
// it would leave the progress short of its total.
func (c *Client) bytesToFetch(image gcr.Image) (int64, error) {
	layers, err := image.Layers()
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

// isCached reports whether the image named hex is in the layer cache. A
// cache that cannot be read as a layout, or that does not name the image,
// does not have it.
func (c *Client) isCached(ctx context.Context, hex string) (bool, error) {
	if _, err := os.Stat(filepath.Join(c.cacheDir, ispec.ImageLayoutFile)); err != nil {
		return false, nil
	}

	casEngine, err := dir.Open(c.cacheDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("open the layer cache: %w", err)
	}
	defer func() { _ = casEngine.Close() }()

	descriptorPaths, err := casext.NewEngine(casEngine).ResolveReference(ctx, hex)
	if err != nil {
		return false, nil
	}

	return len(descriptorPaths) > 0, nil
}

// unpackLayers unpacks a cached image's layers into a root filesystem at
// targetDir, owned by the daemon's user.
func (c *Client) unpackLayers(ctx context.Context, image gcr.Image, targetDir string) error {
	manifest, err := image.Manifest()
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	casEngine, err := dir.Open(c.cacheDir)
	if err != nil {
		return fmt.Errorf("open the layer cache: %w", err)
	}
	defer func() { _ = casEngine.Close() }()

	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}

	unpackOptions := &layer.UnpackOptions{
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

	if err := layer.UnpackRootfs(ctx, casEngine, targetDir, ociManifest(manifest), unpackOptions); err != nil {
		return fmt.Errorf("unpack rootfs: %w", err)
	}

	return nil
}

// cachedImage returns the image named hex in the layer cache.
func (c *Client) cachedImage(hex string) (gcr.Image, error) {
	path, err := layout.FromPath(c.cacheDir)
	if err != nil {
		return nil, fmt.Errorf("open layout: %w", err)
	}

	index, err := path.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("read index: %w", err)
	}
	indexManifest, err := index.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("read index manifest: %w", err)
	}

	for _, descriptor := range indexManifest.Manifests {
		if descriptor.Annotations[ispec.AnnotationRefName] == hex {
			return path.Image(descriptor.Digest)
		}
	}

	return nil, fmt.Errorf("no image %s in the layer cache", hex)
}

// digestHex returns the hex of a digest such as sha256:<hex>, which names
// its image in the layer cache.
func digestHex(digest string) (string, error) {
	_, hex, ok := strings.Cut(digest, ":")
	if !ok || hex == "" {
		return "", fmt.Errorf("invalid digest %q", digest)
	}
	return hex, nil
}

// parseEnv returns a list of KEY=value environment variables as a map.
func parseEnv(list []string) map[string]string {
	env := make(map[string]string, len(list))
	for _, variable := range list {
		key, value, _ := strings.Cut(variable, "=")
		env[key] = value
	}
	return env
}

// ociManifest returns a go-containerregistry manifest as an OCI one.
func ociManifest(gcrManifest *gcr.Manifest) ispec.Manifest {
	layers := make([]ispec.Descriptor, len(gcrManifest.Layers))
	for i, layer := range gcrManifest.Layers {
		layers[i] = ispec.Descriptor{
			MediaType:   ociMediaType(string(layer.MediaType)),
			Digest:      ociDigest(layer.Digest),
			Size:        layer.Size,
			Annotations: layer.Annotations,
		}
	}

	return ispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: int(gcrManifest.SchemaVersion)},
		MediaType: ociMediaType(string(gcrManifest.MediaType)),
		Config: ispec.Descriptor{
			MediaType:   ociMediaType(string(gcrManifest.Config.MediaType)),
			Digest:      ociDigest(gcrManifest.Config.Digest),
			Size:        gcrManifest.Config.Size,
			Annotations: gcrManifest.Config.Annotations,
		},
		Layers:      layers,
		Annotations: gcrManifest.Annotations,
	}
}

// ociMediaType returns the OCI media type equivalent to a Docker one, or
// mediaType itself if it has none.
func ociMediaType(mediaType string) string {
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

// ociDigest returns a go-containerregistry digest as an opencontainers one.
func ociDigest(d gcr.Hash) digest.Digest {
	return digest.NewDigestFromEncoded(digest.Algorithm(d.Algorithm), d.Hex)
}
