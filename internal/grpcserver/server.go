// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package grpcserver implements the DaemonService gRPC service and the
// interceptors in front of it: those that authenticate a call's token,
// authorize it by the token's scopes, and audit it. It builds the gRPC
// servers the daemon serves the API with, on its Unix socket and over TCP.
//
// Handlers validate the request, call the store or the instance manager, and
// convert the result. They take their dependencies as concrete types.
package grpcserver

import (
	"context"
	"crypto/tls"
	"iter"
	"log/slog"
	"net/netip"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/konradasb/dicer/internal/doctor"
	"github.com/konradasb/dicer/internal/event"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/instance"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/token"
	"github.com/konradasb/dicer/internal/volume"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// Config holds the dependencies for creating a Server.
type Config struct {
	NetworkManager  *network.Manager
	InstanceManager *instance.Manager

	Starters      map[hypervisor.Type][]hypervisor.Starter
	ImageManager  *image.Manager
	KernelManager *kernel.Manager
	VolumeManager *volume.Manager
	TokenManager  *token.Manager

	// Events is the event log GetEvents reads.
	Events *event.Log

	// ListenAddress is the address the TCP listener is bound to, such as
	// [::]:9000, or empty if the API is not served over TCP.
	ListenAddress string

	// HostAddresses returns the host's own addresses, which a TCP listener
	// on all of them is reached at. Nil reports the listener's address as it
	// is.
	HostAddresses func() ([]netip.Addr, error)

	// Fingerprint is the fingerprint of the certificate the daemon is served
	// with over TCP, or empty if it is not.
	Fingerprint string

	// TokenFingerprint is the fingerprint every token carries, for clients
	// to check the daemon by: Fingerprint, or empty for a certificate
	// clients verify for themselves.
	TokenFingerprint string

	// DataDir is the data directory, whose disk GetResources reports on.
	DataDir string

	// CheckHost checks the host for CheckHost. Nil checks nothing.
	CheckHost func(ctx context.Context, opts doctor.Options) iter.Seq[doctor.Result]

	// Version is the daemon's version, as GetHostInfo reports it.
	Version string

	// Keepalive is how the server finds clients that have gone without
	// closing their connection, and how often clients may ping it.
	Keepalive KeepaliveConfig

	// Logger is where the audit log and the calls refused for their token
	// are written. Nil is slog.Default().
	Logger *slog.Logger
}

// KeepaliveConfig is how the server and its clients check that the other is
// still there. A zero field is gRPC's default.
type KeepaliveConfig struct {
	// Interval is how long a connection may carry nothing before the server
	// pings the client.
	Interval time.Duration

	// Timeout is how long the server waits for the answer to a ping before
	// closing the connection.
	Timeout time.Duration

	// MinClientInterval is how often a client may ping the server at most,
	// even with no call in flight. A client pinging more often is
	// disconnected.
	MinClientInterval time.Duration
}

// Server implements dicerdv1.DaemonServiceServer through its embedded
// handlers.
type Server struct {
	instanceHandler
	snapshotHandler
	networkHandler
	volumeHandler
	kernelHandler
	tokenHandler
	imageHandler
	hostHandler
	resourceHandler
	eventsHandler

	metrics        metrics
	auditLog       auditLog
	authentication *authentication
	keepalive      KeepaliveConfig
}

// NewServer creates a Server with the given configuration.
func NewServer(cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Server{
		instanceHandler: instanceHandler{
			instanceManager: cfg.InstanceManager,

			statsInterval: instanceStatsInterval,
		},
		snapshotHandler: snapshotHandler{instanceManager: cfg.InstanceManager},
		networkHandler: networkHandler{
			networkManager:  cfg.NetworkManager,
			instanceManager: cfg.InstanceManager,
		},
		volumeHandler: volumeHandler{volumeManager: cfg.VolumeManager},
		kernelManager: cfg.KernelManager,
		tokenHandler: tokenHandler{
			tokenManager: cfg.TokenManager,
			servesTCP:    cfg.ListenAddress != "",
			fingerprint:  cfg.TokenFingerprint,
		},
		imageHandler: imageHandler{
			instanceManager: cfg.InstanceManager,
			imageManager:    cfg.ImageManager,
		},
		resourceHandler: resourceHandler{
			volumeManager:   cfg.VolumeManager,
			instanceManager: cfg.InstanceManager,
			dataDir:         cfg.DataDir,
		},
		hostHandler: hostHandler{
			version:       cfg.Version,
			starters:      cfg.Starters,
			listenAddress: cfg.ListenAddress,
			hostAddresses: cfg.HostAddresses,
			fingerprint:   cfg.Fingerprint,
			checkHost:     cfg.CheckHost,
		},
		events:         cfg.Events,
		metrics:        newMetrics(),
		auditLog:       newAuditLog(logger),
		authentication: newAuthentication(cfg.TokenManager, logger),
		keepalive:      cfg.Keepalive,
	}
}

// NewTCPServer returns a gRPC server for server's handlers, over TLS as
// tlsConfig says, to calls made with a token whose scopes allow them.
func NewTCPServer(server *Server, tlsConfig *tls.Config) *grpc.Server {
	return newGRPCServer(server, server.authentication, credentials.NewTLS(tlsConfig))
}

// newGRPCServer returns a gRPC server for server's handlers, with creds.
// Its metrics and audit log interceptors come first, so that they see the
// status each call ends with, and the conversion of handlers' errors to
// statuses comes inside them. With authentication, only calls with a token
// reach the audit log, and innermost the token's scopes must allow the
// call, so that a refused call is audited too. A nil authentication lets
// every call through.
func newGRPCServer(
	server *Server, authentication *authentication, creds credentials.TransportCredentials,
) *grpc.Server {
	unary := []grpc.UnaryServerInterceptor{server.unaryMetricsInterceptor()}
	stream := []grpc.StreamServerInterceptor{server.streamMetricsInterceptor()}
	if authentication != nil {
		unary = append(unary, authentication.unaryInterceptor())
		stream = append(stream, authentication.streamInterceptor())
	}
	unary = append(unary, server.auditLog.unaryInterceptor(), unaryStatusInterceptor)
	stream = append(stream, server.auditLog.streamInterceptor(), streamStatusInterceptor)
	if authentication != nil {
		unary = append(unary, unaryAuthorizationInterceptor)
		stream = append(stream, streamAuthorizationInterceptor)
	}

	s := grpc.NewServer(
		grpc.Creds(creds),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    server.keepalive.Interval,
			Timeout: server.keepalive.Timeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             server.keepalive.MinClientInterval,
			PermitWithoutStream: true,
		}),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	)
	dicerdv1.RegisterDaemonServiceServer(s, server)

	return s
}
