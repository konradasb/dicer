// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dns

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/konradasb/dicer/internal/types"
)

// Port is the port guests ask on.
const Port = 53

// Config configures [Servers].
type Config struct {
	// Resolver knows the networks' instances.
	Resolver Resolver
	// Metrics records what the servers answer. Nil records nothing.
	Metrics Metrics
	// DefaultNameservers are the nameservers a network that names none
	// forwards to.
	DefaultNameservers []string
	// Port overrides the port listened on, for tests. Zero means [Port].
	Port int
	// Logger is where the servers log. Nil is slog.Default().
	Logger *slog.Logger
}

// Servers runs a server for each network that has one. It is safe for
// concurrent use, but calls for one network must not overlap: the instance
// manager makes them under the network's lock.
type Servers struct {
	cfg    Config
	logger *slog.Logger

	mu      sync.Mutex
	servers map[string]*server
}

// NewServers returns servers for networks, none of them started.
func NewServers(cfg Config) *Servers {
	if cfg.Port == 0 {
		cfg.Port = Port
	}
	if cfg.Metrics == nil {
		cfg.Metrics = discardMetrics{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Servers{
		cfg:     cfg,
		logger:  cfg.Logger.With("component", "dns"),
		servers: make(map[string]*server),
	}
}

// Serve starts a server for nw on its gateway address, unless one is
// already serving it as it is: it is restarted if the network's gateway,
// subnet, upstreams or isolation have changed. The gateway address must
// already be on the host, on the network's bridge.
func (s *Servers) Serve(ctx context.Context, nw types.Network) error {
	want, err := s.networkOf(nw)
	if err != nil {
		return err
	}

	s.mu.Lock()
	running, ok := s.servers[nw.Name]
	if ok && sameNetwork(running.network, want) {
		s.mu.Unlock()
		return nil
	}
	delete(s.servers, nw.Name)
	s.mu.Unlock()

	// Closing waits for the queries in flight, so not under the lock.
	if ok {
		running.close()
	}
	addr := net.JoinHostPort(want.gateway.String(), strconv.Itoa(s.cfg.Port))
	srv, err := listen(ctx, addr, want, s.cfg.Resolver, s.cfg.Metrics, s.logger)
	if err != nil {
		return fmt.Errorf("serve DNS for network %q on %s: %w", nw.Name, addr, err)
	}
	s.mu.Lock()
	s.servers[nw.Name] = srv
	s.mu.Unlock()

	s.logger.InfoContext(ctx, "serving DNS", "network", nw.Name, "address", srv.addr(), "upstreams", want.upstreams)
	return nil
}

// networkOf returns what a server needs to know of nw.
func (s *Servers) networkOf(nw types.Network) (network, error) {
	subnet, err := netip.ParsePrefix(nw.Subnet)
	if err != nil {
		return network{}, fmt.Errorf("network %q: subnet: %w", nw.Name, err)
	}
	gateway, err := netip.ParseAddr(nw.Gateway)
	if err != nil {
		return network{}, fmt.Errorf("network %q: gateway: %w", nw.Name, err)
	}

	nameservers := nw.Nameservers
	if len(nameservers) == 0 {
		nameservers = s.cfg.DefaultNameservers
	}
	upstreams := make([]string, 0, len(nameservers))
	for _, ns := range nameservers {
		upstreams = append(upstreams, net.JoinHostPort(ns, strconv.Itoa(Port)))
	}

	return network{
		name:             nw.Name,
		domain:           strings.ToLower(nw.Name),
		subnet:           subnet.Masked(),
		gateway:          gateway,
		upstreams:        upstreams,
		answersInstances: !nw.Isolated,
	}, nil
}

// sameNetwork reports whether a server for a serves b as it is.
func sameNetwork(a, b network) bool {
	return a.name == b.name && a.subnet == b.subnet && a.gateway == b.gateway &&
		slices.Equal(a.upstreams, b.upstreams) && a.answersInstances == b.answersInstances
}

// Stop stops a network's server, if it has one.
func (s *Servers) Stop(networkName string) {
	s.mu.Lock()
	srv, ok := s.servers[networkName]
	delete(s.servers, networkName)
	s.mu.Unlock()

	if ok {
		srv.close()
		s.logger.Info("stopped serving DNS", "network", networkName)
	}
}

// Close stops every server.
func (s *Servers) Close() {
	s.mu.Lock()
	servers := s.servers
	s.servers = make(map[string]*server)
	s.mu.Unlock()

	for _, srv := range servers {
		srv.close()
	}
}
