// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/konradasb/dicer/internal/grpcapi"
	"github.com/konradasb/dicer/internal/version"
)

// The transports the API is served on.
const (
	transportUnix = "unix"
	transportTCP  = "tcp"
)

// apiListener is one transport the API is served on, and the server behind
// it.
type apiListener struct {
	transport string
	address   string
	listener  net.Listener
	server    *grpc.Server
}

// listenAPI opens every configured transport, each with its own server since
// credentials are per server.
func (d *daemon) listenAPI(ctx context.Context) (listeners []apiListener, err error) {
	defer func() {
		if err != nil {
			for _, l := range listeners {
				_ = l.listener.Close()
			}
		}
	}()

	socket, err := listenSocket(ctx, d.cfg.API.Socket)
	if err != nil {
		return nil, err
	}

	var apiAddress string
	var tcp net.Listener

	if d.cfg.API.TCP.Enabled() {
		tcp, err = (&net.ListenConfig{}).Listen(ctx, "tcp", d.cfg.API.TCP.Listen)
		if err != nil {
			return nil, fmt.Errorf("listen on %s: %w", d.cfg.API.TCP.Listen, err)
		}
		apiAddress = tcp.Addr().String()
	}

	api := grpcapi.NewServer(grpcapi.Config{
		Hypervisors: d.hypervisors,
		Definitions: d.definitions,
		APIAddress:  apiAddress,
		Addresses:   d.addresses,
		Instances:   d.instances,
		Images:      d.images,
		Kernels:     d.kernels,
		Volumes:     d.volumes,
		Events:      d.events,
		DataDir:     d.cfg.DataDir,
		Defaults:    grpcapi.Defaults{Kernel: d.cfg.Defaults.Kernel, Network: d.cfg.Defaults.Network},
		Version:     version.Version,
		Logger:      d.logger,
	})

	listeners = make([]apiListener, 0, 2)
	//nolint:contextcheck // interceptors run with each call's own context
	listeners = append(listeners, apiListener{
		transport: transportUnix,
		address:   d.cfg.API.Socket.Path,
		listener:  socket,
		server:    d.newGRPCServer(api),
	})

	if tcp == nil {
		return listeners, nil
	}

	creds, err := d.apiCredentials()
	if err != nil {
		_ = tcp.Close()
		return listeners, err
	}

	return append(listeners, apiListener{
		transport: transportTCP,
		address:   tcp.Addr().String(),
		listener:  tcp,
		server:    d.newGRPCServer(api, creds...), //nolint:contextcheck // interceptors run with each call's own context
	}), nil
}

// apiCredentials returns the network listener's credentials and logs how
// callers are authenticated.
func (d *daemon) apiCredentials() ([]grpc.ServerOption, error) {
	tcp := d.cfg.API.TCP

	if !tcp.TLS.Enabled() {
		d.logger.Warn("the API is served over the network with no TLS and no authentication; "+
			"anyone who can reach it has full control of this host",
			"listen", tcp.Listen)
		return nil, nil
	}

	cfg, err := serverTLSConfig(tcp.TLS, d.logger)
	if err != nil {
		return nil, err
	}

	if tcp.TLS.RequiresClientCert() {
		d.logger.Info("the API is served over the network with TLS, and requires a client certificate",
			"listen", tcp.Listen, "client_ca_file", tcp.TLS.ClientCAFile)
	} else {
		d.logger.Warn("the API is served over the network with TLS but no client authentication; "+
			"anyone who can reach it has full control of this host",
			"listen", tcp.Listen)
	}

	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(cfg))}, nil
}

// newGRPCServer builds an API server with metrics and audit interceptors,
// which see the status each call ends with, and innermost the conversion of
// handlers' errors to statuses.
func (d *daemon) newGRPCServer(api *grpcapi.Server, opts ...grpc.ServerOption) *grpc.Server {
	audit := grpcapi.NewAudit(d.logger)

	s := grpc.NewServer(append(opts,
		grpc.ChainUnaryInterceptor(
			d.metrics.UnaryServerInterceptor(),
			audit.UnaryInterceptor(),
			grpcapi.UnaryStatusInterceptor,
		),
		grpc.ChainStreamInterceptor(
			d.metrics.StreamServerInterceptor(),
			audit.StreamInterceptor(),
			grpcapi.StreamStatusInterceptor,
		),
	)...)
	api.Register(s)

	return s
}

// listenSocket opens the API socket, removing a stale one left by a previous
// run.
func listenSocket(ctx context.Context, cfg SocketConfig) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o750); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	if err := removeStaleSocket(ctx, cfg.Path); err != nil {
		return nil, err
	}

	// Create the socket owner-only, then chmod, so nobody can connect
	// before its mode is set.
	oldUmask := unix.Umask(0o177)
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", cfg.Path)
	unix.Umask(oldUmask)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Path, err)
	}

	mode := cfg.Mode
	if mode == 0 {
		mode = defaultSocketMode
	}
	if err := os.Chmod(cfg.Path, os.FileMode(mode)); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}

	return listener, nil
}

// removeStaleSocket removes a socket left by an unclean shutdown. It refuses
// to remove a non-socket or a socket another daemon still answers on.
func removeStaleSocket(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}

	dialCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", path); err == nil {
		_ = conn.Close()
		return fmt.Errorf("another daemon is already serving on %s", path)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}
