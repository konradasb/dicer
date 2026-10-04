// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package dns answers the DNS queries of a network's guests, so that an
// instance can reach another by name.
//
// Each network has a server of its own, listening on the network's gateway
// address, which is the nameserver its guests are given. It answers for the
// network's running instances -- by name or hostname, bare or followed by the
// network's name, "db" or "db.default" -- and for reverse lookups of the
// network's addresses. Every other query is forwarded, as it is, to the
// network's upstream nameservers.
//
// On every network, isolated ones too, it also answers for the host under
// InternalDomain: host.dicer.internal and gateway.dicer.internal are the
// network's gateway, the host's address on the network.
package dns

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// InternalDomain is the domain the server answers for itself on every
// network, never asking upstream: .internal is reserved for private use, so
// no public name is under it.
const InternalDomain = "dicer.internal"

// hostNames are the names under InternalDomain that are the host, as guests
// reach it: the network's gateway.
var hostNames = []string{"host." + InternalDomain, "gateway." + InternalDomain}

// Resolver knows a network's instances.
type Resolver interface {
	// LookupHost returns the addresses of the running instances on network
	// whose name or hostname is name, compared without regard to case.
	LookupHost(network, name string) []netip.Addr
	// LookupAddr returns the names of the running instance on network that
	// holds addr, or none if no running instance does.
	LookupAddr(network string, addr netip.Addr) []string
}

const (
	// ttl is how long an answer about an instance may be cached. Short, as
	// an instance's address goes with it when it is deleted.
	ttl = 5

	// forwardTimeout bounds one attempt to ask an upstream nameserver.
	forwardTimeout = 3 * time.Second

	// tcpIdleTimeout closes a TCP connection a client leaves idle.
	tcpIdleTimeout = 10 * time.Second

	// maxInFlight bounds the UDP queries handled at once; more are dropped,
	// and the guest's resolver asks again.
	maxInFlight = 256

	// maxConnections bounds the TCP connections served at once, apart from
	// UDP queries, since an idle one holds its place for tcpIdleTimeout.
	// More are closed, and the guest's resolver asks again.
	maxConnections = 32

	// maxMessage is the largest DNS message, over TCP.
	maxMessage = 65535
)

// How a server answered a query, as Metrics.RecordDNSQuery is told.
const (
	// QueryLocal is a query answered from the network's own names, found
	// or not.
	QueryLocal = "local"
	// QueryForwarded is a query an upstream nameserver answered.
	QueryForwarded = "forwarded"
	// QueryFailed is a query no upstream nameserver answered, failed with
	// SERVFAIL.
	QueryFailed = "failed"
	// QueryInvalid is a message that is not one query, refused with
	// FORMERR or not answered at all.
	QueryInvalid = "invalid"
	// QueryDropped is a query dropped, or a TCP connection closed, because
	// the server was too busy.
	QueryDropped = "dropped"
)

// Metrics records what the servers answer. It is declared here, and
// satisfied by internal/metrics, so this package measures itself without
// depending on a metrics library. It must be safe for concurrent use.
type Metrics interface {
	// RecordDNSQuery records a query to a network's server, and how it
	// was answered: QueryLocal, QueryForwarded, QueryFailed, QueryInvalid
	// or QueryDropped.
	RecordDNSQuery(network, result string)

	// RecordDNSForward records how long asking a network's upstream
	// nameservers took, answered or not.
	RecordDNSForward(network string, d time.Duration)
}

// discardMetrics is the Metrics used when none is configured.
type discardMetrics struct{}

func (discardMetrics) RecordDNSQuery(string, string)          {}
func (discardMetrics) RecordDNSForward(string, time.Duration) {}

// network is what a server needs to know of the network it serves.
type network struct {
	name string
	// domain is the network's name as queries, lowercased, end in it.
	domain string
	subnet netip.Prefix
	// gateway is the host's address on the network.
	gateway netip.Addr
	// upstreams are the nameservers forwarded to, as host:port.
	upstreams []string
	// answersInstances is whether the network's instances' names are
	// answered: not on an isolated network, whose instances cannot reach
	// each other anyway.
	answersInstances bool
}

// server serves one network.
type server struct {
	network  network
	resolver Resolver
	metrics  Metrics
	logger   *slog.Logger

	udp net.PacketConn
	tcp net.Listener

	inFlight    chan struct{}
	connections chan struct{}
	// firstUpstream is the index in network.upstreams of the one asked
	// first: the one that answered last, so that one that is down is not
	// waited on by every query.
	firstUpstream atomic.Int32
	// cancel ends the context the server runs in, and with it the queries
	// in flight, upstream ones too.
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// listen starts a server on addr.
func listen(
	ctx context.Context, addr string, nw network, resolver Resolver, metrics Metrics, logger *slog.Logger,
) (*server, error) {
	var lc net.ListenConfig
	udp, err := lc.ListenPacket(ctx, "udp4", addr)
	if err != nil {
		return nil, err
	}
	// On the port UDP got, which is addr's unless it asked for any.
	tcp, err := lc.Listen(ctx, "tcp4", udp.LocalAddr().String())
	if err != nil {
		_ = udp.Close()
		return nil, err
	}

	// Not ctx itself: that is the request that started the server, not its
	// life.
	serverCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &server{
		network:     nw,
		resolver:    resolver,
		metrics:     metrics,
		logger:      logger,
		udp:         udp,
		tcp:         tcp,
		inFlight:    make(chan struct{}, maxInFlight),
		connections: make(chan struct{}, maxConnections),
		cancel:      cancel,
	}
	s.wg.Go(func() { s.serveUDP(serverCtx) })
	s.wg.Go(func() { s.serveTCP(serverCtx) })
	return s, nil
}

// addr is the UDP address the server listens on.
func (s *server) addr() string { return s.udp.LocalAddr().String() }

// close stops the server and waits for the queries in flight.
func (s *server) close() {
	s.cancel()
	_ = s.udp.Close()
	_ = s.tcp.Close()
	s.wg.Wait()
}

func (s *server) serveUDP(ctx context.Context) {
	buf := make([]byte, maxMessage)
	for {
		n, from, err := s.udp.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Warn("read DNS query", "error", err)
			continue
		}

		select {
		case s.inFlight <- struct{}{}:
		default:
			s.metrics.RecordDNSQuery(s.network.name, QueryDropped)
			continue // too busy: the guest asks again
		}

		query := append([]byte(nil), buf[:n]...)
		s.wg.Go(func() {
			defer func() { <-s.inFlight }()
			if reply := s.answer(ctx, query, s.forwardUDP); reply != nil {
				_, _ = s.udp.WriteTo(reply, from)
			}
		})
	}
}

func (s *server) serveTCP(ctx context.Context) {
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.Warn("accept DNS connection", "error", err)
			continue
		}

		select {
		case s.connections <- struct{}{}:
		default:
			s.metrics.RecordDNSQuery(s.network.name, QueryDropped)
			_ = conn.Close() // too busy: the guest asks again
			continue
		}
		s.wg.Go(func() {
			defer func() { <-s.connections }()
			s.serveConn(ctx, conn)
		})
	}
}

// serveConn answers the queries on one TCP connection, each prefixed with
// its length, until the client is done or the server closes.
func (s *server) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	for {
		_ = conn.SetDeadline(time.Now().Add(tcpIdleTimeout))
		query, err := readFramed(conn)
		if err != nil {
			return
		}
		reply := s.answer(ctx, query, s.forwardTCP)
		if reply == nil {
			return
		}
		if err := writeFramed(conn, reply); err != nil {
			return
		}
	}
}

// answer replies to one query: from the network's own names if it asks for
// one, or else with what forward gets from upstream. Nil means no reply: a
// message too broken to answer.
func (s *server) answer(
	ctx context.Context, query []byte, forward func(context.Context, []byte) ([]byte, error),
) []byte {
	var p dnsmessage.Parser
	header, err := p.Start(query)
	if err != nil || header.Response {
		s.metrics.RecordDNSQuery(s.network.name, QueryInvalid)
		return nil
	}
	questions, err := p.AllQuestions()
	if err != nil || len(questions) != 1 {
		s.metrics.RecordDNSQuery(s.network.name, QueryInvalid)
		return reply(header, questions, dnsmessage.RCodeFormatError, nil)
	}
	q := questions[0]

	if q.Class == dnsmessage.ClassINET {
		if records, rcode, ok := s.ownRecords(q); ok {
			s.metrics.RecordDNSQuery(s.network.name, QueryLocal)
			return reply(header, questions, rcode, records)
		}
	}

	started := time.Now()
	upstream, err := forward(ctx, query)
	s.metrics.RecordDNSForward(s.network.name, time.Since(started))
	if err != nil {
		s.metrics.RecordDNSQuery(s.network.name, QueryFailed)
		s.logger.Debug("forward DNS query", "network", s.network.name, "name", q.Name.String(), "error", err)
		return reply(header, questions, dnsmessage.RCodeServerFailure, nil)
	}
	s.metrics.RecordDNSQuery(s.network.name, QueryForwarded)
	return upstream
}

// ownRecords answers a question about the network's own names or
// addresses, if it is one.
func (s *server) ownRecords(q dnsmessage.Question) ([]dnsmessage.Resource, dnsmessage.RCode, bool) {
	name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))

	if q.Type == dnsmessage.TypePTR {
		addr, ok := reverseAddr(name)
		if !ok || !s.network.subnet.Contains(addr) {
			return nil, 0, false
		}
		// The network's own addresses are never asked of upstream, which
		// would learn nothing of them but what the guests are looking at.
		var records []dnsmessage.Resource
		if addr == s.network.gateway {
			records = append(records, ptrRecord(q.Name, hostNames[0]+"."))
		} else if s.network.answersInstances {
			for _, n := range s.resolver.LookupAddr(s.network.name, addr) {
				records = append(records, ptrRecord(q.Name, n+"."+s.network.name+"."))
			}
		}
		if len(records) == 0 {
			return nil, dnsmessage.RCodeNameError, true
		}
		return records, dnsmessage.RCodeSuccess, true
	}

	// The host, which every network's guests may reach, isolated or not.
	if name == InternalDomain || strings.HasSuffix(name, "."+InternalDomain) {
		if !slices.Contains(hostNames, name) {
			return nil, dnsmessage.RCodeNameError, true
		}
		return addressRecords(q, []netip.Addr{s.network.gateway}), dnsmessage.RCodeSuccess, true
	}

	if !s.network.answersInstances {
		return nil, 0, false
	}

	// "db", or "db.<network>".
	host, qualified := strings.CutSuffix(name, "."+s.network.domain)
	if host == "" || strings.Contains(host, ".") {
		return nil, 0, false
	}

	addrs := s.resolver.LookupHost(s.network.name, host)
	if len(addrs) == 0 {
		if qualified {
			// The network's name is its domain: what is not there, is not.
			return nil, dnsmessage.RCodeNameError, true
		}
		// A bare name may be the upstream's to answer.
		return nil, 0, false
	}

	return addressRecords(q, addrs), dnsmessage.RCodeSuccess, true
}

// addressRecords answers a question about a name that is there, with addrs:
// A records, or none for a type it has no records of, such as AAAA.
func addressRecords(q dnsmessage.Question, addrs []netip.Addr) []dnsmessage.Resource {
	if q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeALL {
		return nil
	}
	records := make([]dnsmessage.Resource, 0, len(addrs))
	for _, a := range addrs {
		records = append(records, aRecord(q.Name, a))
	}
	return records
}

// reverseAddr reads the IPv4 address an in-addr.arpa name stands for.
func reverseAddr(name string) (netip.Addr, bool) {
	rest, ok := strings.CutSuffix(name, ".in-addr.arpa")
	if !ok {
		return netip.Addr{}, false
	}
	labels := strings.Split(rest, ".")
	if len(labels) != 4 {
		return netip.Addr{}, false
	}

	var ip [4]byte
	for i, label := range labels {
		n, err := strconv.ParseUint(label, 10, 8)
		if err != nil {
			return netip.Addr{}, false
		}
		ip[3-i] = byte(n)
	}
	return netip.AddrFrom4(ip), true
}

func aRecord(name dnsmessage.Name, addr netip.Addr) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl},
		Body:   &dnsmessage.AResource{A: addr.As4()},
	}
}

func ptrRecord(name dnsmessage.Name, target string) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: ttl},
		Body:   &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(target)},
	}
}

// reply builds the answer to a query.
func reply(
	query dnsmessage.Header, questions []dnsmessage.Question, rcode dnsmessage.RCode, answers []dnsmessage.Resource,
) []byte {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:                 query.ID,
			Response:           true,
			OpCode:             query.OpCode,
			Authoritative:      rcode != dnsmessage.RCodeServerFailure && rcode != dnsmessage.RCodeFormatError,
			RecursionDesired:   query.RecursionDesired,
			RecursionAvailable: true,
			RCode:              rcode,
		},
		Questions: questions,
		Answers:   answers,
	}
	out, err := msg.Pack()
	if err != nil {
		return nil
	}
	return out
}

// answerBuffers hold upstream answers as they are read over UDP: large
// enough for any, and too large to allocate for each.
var answerBuffers = sync.Pool{New: func() any {
	buf := make([]byte, maxMessage)
	return &buf
}}

// forwardUDP asks each upstream nameserver in turn over UDP, and returns
// the first answer.
func (s *server) forwardUDP(ctx context.Context, query []byte) ([]byte, error) {
	return s.forwardEach(ctx, func(ctx context.Context, upstream string) ([]byte, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "udp", upstream)
		if err != nil {
			return nil, err
		}
		defer func() { _ = conn.Close() }()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}

		if _, err := conn.Write(query); err != nil {
			return nil, err
		}
		bufp, _ := answerBuffers.Get().(*[]byte)
		defer answerBuffers.Put(bufp)
		buf := *bufp
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return nil, err
			}
			// Ignore anything that does not answer this query.
			if n >= 2 && len(query) >= 2 && buf[0] == query[0] && buf[1] == query[1] {
				return bytes.Clone(buf[:n]), nil
			}
		}
	})
}

// forwardTCP asks each upstream nameserver in turn over TCP, for a client
// that asked over TCP, as it does for an answer too large for UDP.
func (s *server) forwardTCP(ctx context.Context, query []byte) ([]byte, error) {
	return s.forwardEach(ctx, func(ctx context.Context, upstream string) ([]byte, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", upstream)
		if err != nil {
			return nil, err
		}
		defer func() { _ = conn.Close() }()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}

		if err := writeFramed(conn, query); err != nil {
			return nil, err
		}
		return readFramed(conn)
	})
}

// forwardEach tries ask on each upstream until one answers, starting with
// the one that answered last.
func (s *server) forwardEach(
	ctx context.Context, ask func(ctx context.Context, upstream string) ([]byte, error),
) ([]byte, error) {
	upstreams := s.network.upstreams
	if len(upstreams) == 0 {
		return nil, errors.New("no upstream nameservers")
	}

	first := int(s.firstUpstream.Load())
	var errs []error
	for i := range upstreams {
		n := (first + i) % len(upstreams)
		askCtx, cancel := context.WithTimeout(ctx, forwardTimeout)
		answer, err := ask(askCtx, upstreams[n])
		cancel()
		if err == nil {
			s.firstUpstream.Store(int32(n))
			return answer, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", upstreams[n], err))
	}
	return nil, errors.Join(errs...)
}

// readFramed reads one DNS message as TCP carries it: after its length.
func readFramed(r io.Reader) ([]byte, error) {
	var size [2]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// writeFramed writes one DNS message as TCP carries it.
func writeFramed(w io.Writer, msg []byte) error {
	if len(msg) > maxMessage {
		return errors.New("message too long")
	}
	framed := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(framed, uint16(len(msg)))
	copy(framed[2:], msg)
	_, err := w.Write(framed)
	return err
}
