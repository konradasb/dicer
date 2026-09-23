// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package grpcapi implements the DaemonService gRPC service and the audit
// interceptors in front of it.
//
// Handlers validate the request, call the store or the lifecycle manager, and
// convert the result. They take their dependencies as concrete types.
package grpcapi

import (
	"log/slog"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	"github.com/konradasb/dicer/internal/volume"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// Config holds the dependencies for creating a Server.
type Config struct {
	Definitions *filestore.Manager
	Addresses   *network.Manager
	Instances   *vm.Manager

	Hypervisors map[types.HypervisorType][]hypervisor.Starter
	Images      *image.Manager
	Kernels     *kernel.Manager
	Volumes     *volume.Manager
	Events      *events.Log

	// APIAddress is the daemon's TCP address, or empty.
	APIAddress string

	// DataDir is the data directory, whose disk GetResources reports on.
	DataDir string

	// Defaults are the configured default kernel and network.
	Defaults Defaults

	Version string
	Logger  *slog.Logger
}

// Server implements dicerdv1.DaemonServiceServer through its embedded
// handlers.
type Server struct {
	instanceHandler
	snapshotHandler
	networkHandler
	volumeHandler
	kernelHandler
	imageHandler
	hostHandler
	resourceHandler
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
			apiAddress:  cfg.APIAddress,
			defaults:    defaults,
		},
		eventsHandler: eventsHandler{events: cfg.Events},
	}
}

// Register registers the server's services with a gRPC server.
func (s *Server) Register(gs *grpc.Server) {
	dicerdv1.RegisterDaemonServiceServer(gs, s)
}

// refuseInUse returns an ErrInvalidState error naming the first instance
// for which inUse is true, or nil. what reads like `kernel "k" is in use`.
func refuseInUse(definitions *filestore.Manager, what string, inUse func(types.InstanceSpec) bool) error {
	instances, err := definitions.ListInstances()
	if err != nil {
		return err
	}
	for _, inst := range instances {
		if inUse(inst) {
			return errdefs.InvalidState("%s by instance %q", what, inst.Name)
		}
	}
	return nil
}
