// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package grpcapi implements the gRPC services Dicer exposes: DaemonService,
// which manages the host, and AccessService, which decides who may use it
// over the network.
//
// It also provides what every server the services run on needs in front of
// them: Authentication, which is chosen per transport, and Audit.
//
// Handlers are thin: they validate the request, call the store or the
// lifecycle manager, and convert the result. There is no forwarding layer and
// no leader check, because there is exactly one daemon and it is this one.
//
// Unlike internal/vm, the handlers take their dependencies as concrete types.
// A consumer-side interface would need most of the store's surface -- every
// create, get, list and delete across four resource types -- and an interface
// that large abstracts nothing. This is the composition-adjacent layer; the
// domain packages below it are the ones that stay decoupled.
package grpcapi

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
	"github.com/dicer-sh/dicer/internal/events"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/hypervisor"
	"github.com/dicer-sh/dicer/internal/image"
	"github.com/dicer-sh/dicer/internal/image/reference"
	"github.com/dicer-sh/dicer/internal/kernel"
	"github.com/dicer-sh/dicer/internal/network"
	"github.com/dicer-sh/dicer/internal/vm"
	"github.com/dicer-sh/dicer/internal/volume"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// Config holds the dependencies for creating a Server.
type Config struct {
	Definitions *filestore.Manager
	Addresses   *network.Manager
	Instances   *vm.Manager

	// Hypervisors are the starters the daemon built, by type. Reported by
	// GetHostInfo so a client knows what it may ask for.
	Hypervisors map[dicer.HypervisorType][]hypervisor.Starter
	Images      *image.Manager
	Kernels     *kernel.Manager
	Volumes     *volume.Manager
	Access      *access.Manager
	Events      *events.Log

	// DataDir is where the daemon keeps its data, whose disk GetResources
	// reports on.
	DataDir string

	// Defaults are the kernel and network an instance gets when it names
	// none. Either may be empty, and then the only one there is is used.
	Defaults Defaults

	Version string
	Logger  *slog.Logger
}

// Server implements dicerdv1.DaemonServiceServer and
// dicerdv1.AccessServiceServer.
//
// The handler groups are embedded rather than delegated to: their methods are
// promoted, so the service interface is satisfied without a screenful of
// one-line forwarding functions. Adding an RPC to the proto is a compile
// error until some handler implements it, which is the point.
type Server struct {
	instanceHandler
	snapshotHandler
	networkHandler
	volumeHandler
	kernelHandler
	imageHandler
	hostHandler
	resourceHandler
	accessHandler
	eventsHandler
}

// NewServer creates a Server with the given configuration.
func NewServer(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	defaults := defaultResolver{definitions: cfg.Definitions, configured: cfg.Defaults}

	return &Server{
		instanceHandler: instanceHandler{
			definitions: cfg.Definitions,
			instances:   cfg.Instances,
			defaults:    defaults,
			logger:      cfg.Logger,
		},
		snapshotHandler: snapshotHandler{definitions: cfg.Definitions, instances: cfg.Instances, logger: cfg.Logger},
		networkHandler:  networkHandler{definitions: cfg.Definitions, addresses: cfg.Addresses},
		volumeHandler:   volumeHandler{definitions: cfg.Definitions, volumes: cfg.Volumes},
		kernelHandler:   kernelHandler{definitions: cfg.Definitions, kernels: cfg.Kernels},
		imageHandler: imageHandler{
			definitions: cfg.Definitions,
			instances:   cfg.Instances,
			images:      cfg.Images,
		},
		resourceHandler: resourceHandler{
			definitions: cfg.Definitions,
			instances:   cfg.Instances,
			dataDir:     cfg.DataDir,
		},
		hostHandler: hostHandler{
			version:     cfg.Version,
			hypervisors: cfg.Hypervisors,
			access:      cfg.Access,
			defaults:    defaults,
		},
		accessHandler: accessHandler{access: cfg.Access},
		eventsHandler: eventsHandler{events: cfg.Events},
	}
}

// Register registers the server's services with a gRPC server.
func (s *Server) Register(gs *grpc.Server) {
	dicerdv1.RegisterDaemonServiceServer(gs, s)
	dicerdv1.RegisterAccessServiceServer(gs, s)
}

// toStatus maps domain errors onto gRPC status codes so clients can act on
// them without matching on strings. The classes and their codes live in the
// root package, where the client reads the same table backwards.
func toStatus(err error) error {
	if err == nil {
		return nil
	}

	var pullErr *image.PullError
	switch {
	case errors.As(err, &pullErr):
		return pullStatus(pullErr)
	case errors.Is(err, access.ErrInvalidToken), errors.Is(err, access.ErrDenied):
		return status.Error(codes.Unauthenticated, err.Error())
	default:
		return status.Error(dicer.StatusCode(err), err.Error())
	}
}

// pullStatus says why an image could not be pulled, in terms of the image
// and its registry rather than of the HTTP exchange that failed: "image
// "nginx:9" not found on docker.io", not "resolve manifest: fetch manifest:
// GET https://index.docker.io/v2/...: MANIFEST_UNKNOWN: ...".
func pullStatus(err *image.PullError) error {
	registry := registryOf(err.Ref)

	var httpErr *transport.Error
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusNotFound:
			return status.Errorf(codes.NotFound, "image %q not found on %s", err.Ref, registry)
		case http.StatusUnauthorized, http.StatusForbidden:
			// A registry answers so for a repository that does not exist
			// as much as for one it will not show: Docker Hub does both.
			return status.Errorf(codes.NotFound, "image %q not found on %s (or it is private)", err.Ref, registry)
		case http.StatusTooManyRequests:
			return status.Errorf(codes.Unavailable, "%s is limiting how often this host may pull; try again later",
				registry)
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return status.Errorf(codes.Unavailable, "cannot reach %s: %v", registry, innermost(err))
	}

	return status.Errorf(codes.Unavailable, "cannot pull image %q: %v", err.Ref, innermost(err))
}

// registryOf returns the registry an image reference names, "docker.io" if
// it names none.
func registryOf(ref string) string {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return "its registry"
	}
	host, _, ok := strings.Cut(parsed.Repository(), "/")
	if !ok || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "docker.io"
	}
	return host
}

// innermost returns the error at the bottom of err's chain: the cause
// itself, without every layer's account of what it was doing at the time.
func innermost(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// refuseInUse returns a FailedPrecondition status naming the first instance
// inUse reports true for, or nil if there is none. what describes the
// resource and how it is used, as in `kernel "k" is in use`.
func refuseInUse(definitions *filestore.Manager, what string, inUse func(dicer.InstanceSpec) bool) error {
	instances, err := definitions.ListInstances()
	if err != nil {
		return toStatus(err)
	}
	for _, inst := range instances {
		if inUse(inst) {
			return status.Errorf(codes.FailedPrecondition, "%s by instance %q", what, inst.Name)
		}
	}
	return nil
}
