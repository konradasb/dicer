// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package daemon

import (
	"context"
	"crypto/tls"
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

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/access"
	"github.com/dicer-sh/dicer/internal/certificate"
	"github.com/dicer-sh/dicer/internal/grpcapi"
)

// tlsDir holds the daemon's own key and certificate, under the data
// directory: the fingerprint every enrolled client has pinned must survive a
// reboot.
const tlsDir = "tls"

// The transports the API is served on.
const (
	transportUnix = "unix"
	transportTCP  = "tcp"
)

// apiServer is the API served on one transport.
//
// Each transport gets a gRPC server of its own because it authenticates
// differently, and gRPC fixes a server's transport credentials when it is
// made. The services behind them are the same.
type apiServer struct {
	transport string
	address   string
	listener  net.Listener
	server    *grpc.Server
}

// listenAPI opens every transport the API is configured for and builds the
// server for each. Nothing is served until the caller serves them.
func (d *daemon) listenAPI(ctx context.Context) (servers []apiServer, err error) {
	defer func() {
		if err != nil {
			for _, s := range servers {
				_ = s.listener.Close()
			}
		}
	}()

	socket, err := listenSocket(ctx, d.cfg.API.Socket)
	if err != nil {
		return nil, err
	}
	servers = append(servers, apiServer{transport: transportUnix, address: d.cfg.API.Socket.Path, listener: socket})

	// The access manager exists either way: trusted clients can be listed
	// and removed locally while the network listener is switched off.
	accessConfig := access.Config{Store: d.definitions, Logger: d.logger}

	var tcpCreds credentials.TransportCredentials
	if d.cfg.API.TCP.Enabled() {
		pair, err := d.loadIdentity()
		if err != nil {
			return servers, err
		}

		tcp, err := (&net.ListenConfig{}).Listen(ctx, "tcp", d.cfg.API.TCP.Listen)
		if err != nil {
			return servers, fmt.Errorf("listen on %s: %w", d.cfg.API.TCP.Listen, err)
		}
		servers = append(servers, apiServer{transport: transportTCP, address: tcp.Addr().String(), listener: tcp})

		accessConfig.Fingerprint, err = certificate.FingerprintOf(pair)
		if err != nil {
			return servers, err
		}
		accessConfig.Addresses, err = advertisedAddresses(d.cfg.API.TCP.Advertise, tcp.Addr())
		if err != nil {
			return servers, err
		}

		tcpCreds = credentials.NewTLS(certificate.ServerConfig(pair))

		d.logger.Info("serving the API over mutual TLS",
			"listen", tcp.Addr().String(),
			"advertise", accessConfig.Addresses,
			"fingerprint", accessConfig.Fingerprint)
	}

	d.access = access.NewManager(accessConfig)

	api := grpcapi.NewServer(grpcapi.Config{
		Hypervisors: d.hypervisors,
		Definitions: d.definitions,
		Addresses:   d.addresses,
		Instances:   d.instances,
		Images:      d.images,
		Kernels:     d.kernels,
		Volumes:     d.volumes,
		Access:      d.access,
		Events:      d.events,
		DataDir:     d.cfg.DataDir,
		Defaults:    grpcapi.Defaults{Kernel: d.cfg.Defaults.Kernel, Network: d.cfg.Defaults.Network},
		Version:     dicer.Version,
		Logger:      d.logger,
	})

	// newGRPCServer takes no context: it builds interceptors, and each runs
	// with the context of the call it intercepts.
	for i := range servers {
		switch servers[i].transport {
		case transportUnix:
			servers[i].server = d.newGRPCServer( //nolint:contextcheck // see above
				api, grpcapi.LocalAuthentication())
		case transportTCP:
			servers[i].server = d.newGRPCServer( //nolint:contextcheck // see above
				api, grpcapi.CertificateAuthentication(d.access), grpc.Creds(tcpCreds))
		}
	}

	return servers, nil
}

// newGRPCServer builds a server for the API with the given authentication.
// The interceptors run in order: metrics sees every call, refused ones
// included; authentication decides who the caller is; the audit log records
// it.
func (d *daemon) newGRPCServer(api *grpcapi.Server, auth grpcapi.Authentication, opts ...grpc.ServerOption) *grpc.Server {
	audit := grpcapi.NewAudit(d.logger)

	opts = append(opts,
		grpc.ChainUnaryInterceptor(
			d.metrics.UnaryServerInterceptor(),
			auth.UnaryInterceptor(),
			audit.UnaryInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			d.metrics.StreamServerInterceptor(),
			auth.StreamInterceptor(),
			audit.StreamInterceptor(),
		),
	)

	s := grpc.NewServer(opts...)
	api.Register(s)

	return s
}

// loadIdentity returns the daemon's key and certificate, generating them on
// first use.
func (d *daemon) loadIdentity() (tls.Certificate, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "dicerd"
	}

	pair, err := certificate.LoadOrGenerate(filepath.Join(d.cfg.DataDir, tlsDir), hostname)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load daemon certificate: %w", err)
	}

	return pair, nil
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

	// The socket is created with no access for anyone but the daemon, and
	// only then opened up to its configured mode: created under the usual
	// umask, it could be connected to before its mode was set.
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

// removeStaleSocket removes a socket left behind by an unclean shutdown,
// which would otherwise block binding. It refuses to remove anything that is
// not a socket, so a misconfigured path cannot delete a real file, and a
// socket something still answers on, which is another daemon's.
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

// advertisedAddresses returns the addresses written into enrolment tokens:
// the configured ones, or else the ones the listener can be reached at.
//
// A listener bound to one address is reached at it. One bound to all of them
// is reached at each of the host's own addresses, loopback and link-local
// ones aside, since those mean nothing to another machine. Behind NAT or a
// port forward none of these is right, which is what configuring them is for.
func advertisedAddresses(configured []string, listening net.Addr) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}

	host, port, err := net.SplitHostPort(listening.String())
	if err != nil {
		return nil, err
	}

	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		return []string{listening.String()}, nil
	}

	interfaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("list the host's addresses: %w", err)
	}

	var addrs []string
	for _, a := range interfaceAddrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || !ipNet.IP.IsGlobalUnicast() {
			continue
		}
		addrs = append(addrs, net.JoinHostPort(ipNet.IP.String(), port))
	}
	if len(addrs) == 0 {
		return nil, errors.New("the host has no address another machine could reach; set api.tcp.advertise")
	}

	return addrs, nil
}
