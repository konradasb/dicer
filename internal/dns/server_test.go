// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dns

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/konradasb/dicer/internal/types"
)

// fakeResolver is a network's instances: their names, and addresses.
type fakeResolver map[string]string

func (f fakeResolver) LookupHost(_, name string) []netip.Addr {
	var out []netip.Addr
	for n, addr := range f {
		if strings.EqualFold(n, name) {
			out = append(out, netip.MustParseAddr(addr))
		}
	}
	return out
}

func (f fakeResolver) LookupAddr(_ string, addr netip.Addr) []string {
	var out []string
	for n, a := range f {
		if a == addr.String() {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// fakeMetrics counts the queries recorded, by result, and the forwards.
type fakeMetrics struct {
	mu       sync.Mutex
	queries  map[string]int
	forwards int
}

func (f *fakeMetrics) RecordDNSQuery(network, result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queries == nil {
		f.queries = map[string]int{}
	}
	f.queries[network+" "+result]++
}

func (f *fakeMetrics) RecordDNSForward(string, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forwards++
}

// recorded returns the queries recorded on network shop with result.
func (f *fakeMetrics) recorded(result string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries["shop "+result]
}

// upstream is a nameserver that answers every A query with 192.0.2.99, over
// UDP and TCP, and counts what it was asked.
type upstream struct {
	addr  string
	asked atomic.Int32
	names sync.Map
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()

	u := &upstream{}
	answer := func(query []byte) []byte {
		var p dnsmessage.Parser
		h, err := p.Start(query)
		if err != nil {
			return nil
		}
		q, err := p.Question()
		if err != nil {
			return nil
		}
		u.asked.Add(1)
		u.names.Store(q.Name.String(), true)
		return reply(h, []dnsmessage.Question{q}, dnsmessage.RCodeSuccess,
			[]dnsmessage.Resource{aRecord(q.Name, netip.MustParseAddr("192.0.2.99"))})
	}

	udp, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", udp.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close() })
	u.addr = udp.LocalAddr().String()

	go func() {
		buf := make([]byte, maxMessage)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := answer(buf[:n]); r != nil {
				_, _ = udp.WriteTo(r, from)
			}
		}
	}()
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				q, err := readFramed(conn)
				if err != nil {
					return
				}
				_ = writeFramed(conn, answer(q))
			}()
		}
	}()
	return u
}

// startServer serves network shop on loopback, with the instances given,
// recording into a fakeMetrics: see metricsOf.
func startServer(t *testing.T, instances fakeResolver, upstreams []string, isolated bool) *server {
	t.Helper()

	srv, err := listen(t.Context(), "127.0.0.1:0", network{
		name:             "shop",
		domain:           "shop",
		subnet:           netip.MustParsePrefix("10.8.0.0/24"),
		gateway:          netip.MustParseAddr("10.8.0.1"),
		upstreams:        upstreams,
		answersInstances: !isolated,
	}, instances, &fakeMetrics{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.close)
	return srv
}

// metricsOf returns what a server from startServer records into.
func metricsOf(t *testing.T, srv *server) *fakeMetrics {
	t.Helper()
	m, ok := srv.metrics.(*fakeMetrics)
	if !ok {
		t.Fatalf("server records into %T, not a fakeMetrics", srv.metrics)
	}
	return m
}

// ask sends a query over network ("udp" or "tcp") and returns the answer.
func ask(t *testing.T, transport, addr, name string, qtype dnsmessage.Type) dnsmessage.Message {
	t.Helper()

	q := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 4242, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName(name), Type: qtype, Class: dnsmessage.ClassINET}},
	}
	query, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}

	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), transport, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var raw []byte
	if transport == "tcp" {
		if err := writeFramed(conn, query); err != nil {
			t.Fatal(err)
		}
		if raw, err = readFramed(conn); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := conn.Write(query); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, maxMessage)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("no answer to %s %s: %v", name, qtype, err)
		}
		raw = buf[:n]
	}

	var m dnsmessage.Message
	if err := m.Unpack(raw); err != nil {
		t.Fatal(err)
	}
	if m.ID != 4242 || !m.Response {
		t.Fatalf("answer to %s = %+v, want a response to the query", name, m.Header)
	}
	return m
}

// addrsIn returns the A records of an answer.
func addrsIn(m dnsmessage.Message) []string {
	var out []string
	for _, r := range m.Answers {
		if a, ok := r.Body.(*dnsmessage.AResource); ok {
			out = append(out, netip.AddrFrom4(a.A).String())
		}
	}
	slices.Sort(out)
	return out
}

func TestServerAnswersForTheNetworksInstances(t *testing.T) {
	up := newUpstream(t)
	srv := startServer(t, fakeResolver{"db": "10.8.0.5", "web": "10.8.0.6"}, []string{up.addr}, false)

	for _, transport := range []string{"udp", "tcp"} {
		for _, name := range []string{"db.", "DB.", "db.shop.", "Db.Shop."} {
			m := ask(t, transport, srv.addr(), name, dnsmessage.TypeA)
			if m.RCode != dnsmessage.RCodeSuccess || !slices.Equal(addrsIn(m), []string{"10.8.0.5"}) {
				t.Errorf("%s %s = %s %q, want 10.8.0.5", transport, name, m.RCode, addrsIn(m))
			}
			if !m.Authoritative {
				t.Errorf("%s %s is not authoritative", transport, name)
			}
		}
	}

	// It is there, with no IPv6 address: no error, so the guest's resolver
	// goes on to its A record without waiting.
	m := ask(t, "udp", srv.addr(), "db.", dnsmessage.TypeAAAA)
	if m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
		t.Errorf("AAAA db = %s with %d answers, want success and none", m.RCode, len(m.Answers))
	}

	// What is not on the network, under its name, is not anywhere.
	m = ask(t, "udp", srv.addr(), "gone.shop.", dnsmessage.TypeA)
	if m.RCode != dnsmessage.RCodeNameError {
		t.Errorf("gone.shop = %s, want NXDOMAIN", m.RCode)
	}

	if n := up.asked.Load(); n != 0 {
		t.Errorf("upstream was asked %d times about the network's own names", n)
	}
}

// A network's name is its domain whatever its case, as an instance's is.
func TestServerAnswersUnderANetworkNamedInCapitals(t *testing.T) {
	resolver := fakeResolver{"db": "10.8.0.5"}
	servers := NewServers(Config{Resolver: resolver, Logger: slog.New(slog.DiscardHandler)})
	nw, _, err := servers.target(types.Network{Name: "Shop", Subnet: "10.8.0.0/24", Gateway: "10.8.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := listen(t.Context(), "127.0.0.1:0", nw, resolver, discardMetrics{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.close)

	for _, name := range []string{"db.shop.", "db.Shop."} {
		if m := ask(t, "udp", srv.addr(), name, dnsmessage.TypeA); !slices.Equal(addrsIn(m), []string{"10.8.0.5"}) {
			t.Errorf("%s = %s %q, want 10.8.0.5", name, m.RCode, addrsIn(m))
		}
	}
}

// A TCP connection's goroutines end with it, not with the server.
func TestServerLetsGoOfClosedConnections(t *testing.T) {
	srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, nil, false)
	ask(t, "tcp", srv.addr(), "db.", dnsmessage.TypeA) // and warm up

	before := runtime.NumGoroutine()
	for range 20 {
		ask(t, "tcp", srv.addr(), "db.", dnsmessage.TypeA)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("%d goroutines after 20 closed connections, want %d", n, before)
	}
}

func TestServerAnswersForTheHost(t *testing.T) {
	up := newUpstream(t)

	// Isolated networks' guests may reach their gateway too.
	for _, isolated := range []bool{false, true} {
		srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, []string{up.addr}, isolated)

		for _, name := range []string{"host.dicer.internal.", "gateway.dicer.internal.", "HOST.Dicer.Internal."} {
			m := ask(t, "udp", srv.addr(), name, dnsmessage.TypeA)
			if m.RCode != dnsmessage.RCodeSuccess || !slices.Equal(addrsIn(m), []string{"10.8.0.1"}) {
				t.Errorf("isolated=%v: %s = %s %q, want the gateway, 10.8.0.1", isolated, name, m.RCode, addrsIn(m))
			}
		}
		if m := ask(t, "tcp", srv.addr(), "host.dicer.internal.", dnsmessage.TypeAAAA); m.RCode != dnsmessage.RCodeSuccess ||
			len(m.Answers) != 0 {
			t.Errorf("isolated=%v: AAAA host.dicer.internal = %s with %d answers, want success and none",
				isolated, m.RCode, len(m.Answers))
		}

		// The rest of the domain is not there, and not asked about.
		for _, name := range []string{"db.dicer.internal.", "dicer.internal.", "a.host.dicer.internal."} {
			if m := ask(t, "udp", srv.addr(), name, dnsmessage.TypeA); m.RCode != dnsmessage.RCodeNameError {
				t.Errorf("isolated=%v: %s = %s, want NXDOMAIN", isolated, name, m.RCode)
			}
		}

		m := ask(t, "udp", srv.addr(), "1.0.8.10.in-addr.arpa.", dnsmessage.TypePTR)
		if len(m.Answers) != 1 {
			t.Fatalf("isolated=%v: PTR of the gateway = %s with %d answers, want one", isolated, m.RCode, len(m.Answers))
		}
		if ptr, ok := m.Answers[0].Body.(*dnsmessage.PTRResource); !ok || ptr.PTR.String() != "host.dicer.internal." {
			t.Errorf("isolated=%v: PTR of the gateway = %v, want host.dicer.internal.", isolated, m.Answers[0].Body)
		}
	}

	if n := up.asked.Load(); n != 0 {
		t.Errorf("upstream was asked %d times about dicer.internal", n)
	}
}

func TestServerAnswersSeveralInstancesWithOneName(t *testing.T) {
	// Two instances share a hostname: both are given, as for a name with
	// several addresses.
	srv := startServer(t, fakeResolver{"api": "10.8.0.7", "API": "10.8.0.8"}, nil, false)

	m := ask(t, "udp", srv.addr(), "api.", dnsmessage.TypeA)
	if got := addrsIn(m); !slices.Equal(got, []string{"10.8.0.7", "10.8.0.8"}) {
		t.Errorf("api = %q, want both addresses", got)
	}
}

func TestServerForwardsEverythingElse(t *testing.T) {
	up := newUpstream(t)
	srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, []string{up.addr}, false)

	for _, transport := range []string{"udp", "tcp"} {
		for _, name := range []string{"example.com.", "registry-1.docker.io.", "stranger."} {
			m := ask(t, transport, srv.addr(), name, dnsmessage.TypeA)
			if !slices.Equal(addrsIn(m), []string{"192.0.2.99"}) {
				t.Errorf("%s %s = %s %q, want the upstream's answer", transport, name, m.RCode, addrsIn(m))
			}
		}
	}
	if _, ok := up.names.Load("stranger."); !ok {
		t.Error("a bare name the network does not have was not asked of upstream")
	}
}

func TestServerFailsWhenUpstreamDoesNot(t *testing.T) {
	// Nothing listens there.
	dead, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := dead.LocalAddr().String()
	_ = dead.Close()

	srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, []string{addr}, false)

	if m := ask(t, "tcp", srv.addr(), "example.com.", dnsmessage.TypeA); m.RCode != dnsmessage.RCodeServerFailure {
		t.Errorf("example.com with no upstream = %s, want SERVFAIL", m.RCode)
	}
	// The network's own names do not need one.
	if m := ask(t, "udp", srv.addr(), "db.", dnsmessage.TypeA); !slices.Equal(addrsIn(m), []string{"10.8.0.5"}) {
		t.Errorf("db with no upstream = %q, want its address", addrsIn(m))
	}
}

// Once an upstream fails, the one that answered instead is asked first, so
// that every query does not wait on the one that is down.
func TestServerAsksTheUpstreamThatAnsweredLastFirst(t *testing.T) {
	// An upstream that hangs up on every query, counting them.
	failing, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = failing.Close() })
	var failed atomic.Int32
	go func() {
		for {
			conn, err := failing.Accept()
			if err != nil {
				return
			}
			failed.Add(1)
			_ = conn.Close()
		}
	}()
	up := newUpstream(t)
	srv := startServer(t, fakeResolver{}, []string{failing.Addr().String(), up.addr}, false)

	for range 3 {
		if m := ask(t, "tcp", srv.addr(), "example.com.", dnsmessage.TypeA); m.RCode != dnsmessage.RCodeSuccess {
			t.Fatalf("example.com = %s, want the working upstream's answer", m.RCode)
		}
	}
	if n := failed.Load(); n != 1 {
		t.Errorf("the failing upstream was asked %d times, want once", n)
	}
}

// Idle TCP connections cannot take the places of UDP queries.
func TestServerAnswersOverUDPWhileTCPIsFull(t *testing.T) {
	srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, nil, false)

	dialer := &net.Dialer{Timeout: time.Second}
	for range maxConnections {
		conn, err := dialer.DialContext(t.Context(), "tcp", srv.addr())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
	}

	if m := ask(t, "udp", srv.addr(), "db.", dnsmessage.TypeA); !slices.Equal(addrsIn(m), []string{"10.8.0.5"}) {
		t.Errorf("db over UDP = %q, want its address", addrsIn(m))
	}

	// One connection more than the server takes is closed.
	extra, err := dialer.DialContext(t.Context(), "tcp", srv.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = extra.Close() }()
	_ = extra.SetReadDeadline(time.Now().Add(5 * time.Second))
	// Served, it would wait for a query and time out instead.
	if _, err := extra.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read on a connection beyond maxConnections = %v, want it closed", err)
	}
	if got := metricsOf(t, srv).recorded(QueryDropped); got != 1 {
		t.Errorf("dropped queries = %d, want 1 for the connection closed", got)
	}
}

func TestServerReverseLookups(t *testing.T) {
	up := newUpstream(t)
	srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, []string{up.addr}, false)

	m := ask(t, "udp", srv.addr(), "5.0.8.10.in-addr.arpa.", dnsmessage.TypePTR)
	if len(m.Answers) != 1 {
		t.Fatalf("PTR 10.8.0.5 = %s with %d answers, want one", m.RCode, len(m.Answers))
	}
	if ptr, ok := m.Answers[0].Body.(*dnsmessage.PTRResource); !ok || ptr.PTR.String() != "db.shop." {
		t.Errorf("PTR 10.8.0.5 = %v, want db.shop.", m.Answers[0].Body)
	}

	// An address of the network nobody holds is not asked of upstream.
	if m := ask(t, "udp", srv.addr(), "77.0.8.10.in-addr.arpa.", dnsmessage.TypePTR); m.RCode != dnsmessage.RCodeNameError {
		t.Errorf("PTR 10.8.0.77 = %s, want NXDOMAIN", m.RCode)
	}
	if n := up.asked.Load(); n != 0 {
		t.Errorf("upstream was asked %d times about the network's addresses", n)
	}

	// Another network's is.
	ask(t, "udp", srv.addr(), "8.8.8.8.in-addr.arpa.", dnsmessage.TypePTR)
	if n := up.asked.Load(); n != 1 {
		t.Errorf("upstream was asked %d times about an outside address, want once", n)
	}
}

func TestServerOnAnIsolatedNetworkOnlyForwards(t *testing.T) {
	up := newUpstream(t)
	srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, []string{up.addr}, true)

	if m := ask(t, "udp", srv.addr(), "db.", dnsmessage.TypeA); slices.Contains(addrsIn(m), "10.8.0.5") {
		t.Error("an isolated network's server gave away another instance's address")
	}
	if m := ask(t, "udp", srv.addr(), "5.0.8.10.in-addr.arpa.", dnsmessage.TypePTR); len(m.Answers) != 0 {
		t.Error("an isolated network's server gave away another instance's name")
	}
}

func TestServerRefusesWhatIsNotAQuery(t *testing.T) {
	srv := startServer(t, fakeResolver{}, nil, false)

	// Two questions in one query: format error.
	q := dnsmessage.Message{
		Header: dnsmessage.Header{ID: 4242},
		Questions: []dnsmessage.Question{
			{Name: dnsmessage.MustNewName("a."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
			{Name: dnsmessage.MustNewName("b."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		},
	}
	query, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(srv.answer(t.Context(), query, nil)); err != nil || m.RCode != dnsmessage.RCodeFormatError {
		t.Errorf("two questions = %s, %v; want FORMERR", m.RCode, err)
	}

	// Garbage, or a response: nothing at all.
	if got := srv.answer(t.Context(), []byte{1, 2, 3}, nil); got != nil {
		t.Errorf("garbage was answered: %x", got)
	}
	q.Response = true
	q.Questions = q.Questions[:1]
	if resp, _ := q.Pack(); srv.answer(t.Context(), resp, nil) != nil {
		t.Error("a response was answered")
	}
}

// TestServerRecordsHowItAnswered covers the result each query is recorded
// with, and that only a forwarded one is timed.
func TestServerRecordsHowItAnswered(t *testing.T) {
	srv := startServer(t, fakeResolver{"db": "10.8.0.5"}, nil, false)
	metrics := metricsOf(t, srv)

	query := func(name string) []byte {
		t.Helper()
		q := dnsmessage.Message{Questions: []dnsmessage.Question{
			{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
		}}
		b, err := q.Pack()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	answers := func(context.Context, []byte) ([]byte, error) { return []byte{0}, nil }
	fails := func(context.Context, []byte) ([]byte, error) { return nil, errors.New("unreachable") }

	srv.answer(t.Context(), query("db."), nil)
	srv.answer(t.Context(), query("gone.shop."), nil)
	srv.answer(t.Context(), query("example.com."), answers)
	srv.answer(t.Context(), query("example.com."), fails)
	srv.answer(t.Context(), []byte{1, 2, 3}, nil)

	for result, want := range map[string]int{
		QueryLocal:     2,
		QueryForwarded: 1,
		QueryFailed:    1,
		QueryInvalid:   1,
		QueryDropped:   0,
	} {
		if got := metrics.recorded(result); got != want {
			t.Errorf("%s queries = %d, want %d", result, got, want)
		}
	}
	if metrics.forwards != 2 {
		t.Errorf("forwards timed = %d, want 2", metrics.forwards)
	}
}

func TestServersServeAndStop(t *testing.T) {
	// A free port, for the servers to listen on at 127.0.0.1.
	probe, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	probeAddr, ok := probe.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatal("a UDP socket's address is not a UDP address")
	}
	port := probeAddr.Port
	_ = probe.Close()

	servers := NewServers(Config{
		Resolver:           fakeResolver{"db": "127.0.0.5"},
		DefaultNameservers: []string{"192.0.2.1"},
		Port:               port,
		Logger:             slog.New(slog.DiscardHandler),
	})
	t.Cleanup(servers.Close)

	nw := types.Network{Name: "shop", Subnet: "127.0.0.0/8", Gateway: "127.0.0.1"}
	if err := servers.Serve(t.Context(), nw); err != nil {
		t.Fatal(err)
	}
	// Serving again as it is changes nothing.
	first := servers.servers["shop"].server
	if err := servers.Serve(t.Context(), nw); err != nil {
		t.Fatal(err)
	}
	if servers.servers["shop"].server != first {
		t.Error("serving an unchanged network again restarted its server")
	}
	if got := servers.servers["shop"].network.upstreams; !slices.Equal(got, []string{"192.0.2.1:53"}) {
		t.Errorf("upstreams = %q, want the default's", got)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if m := ask(t, "udp", addr, "db.", dnsmessage.TypeA); !slices.Equal(addrsIn(m), []string{"127.0.0.5"}) {
		t.Errorf("db = %q, want its address", addrsIn(m))
	}

	// A changed network gets a new server.
	nw.Nameservers = []string{"192.0.2.2"}
	if err := servers.Serve(t.Context(), nw); err != nil {
		t.Fatal(err)
	}
	if got := servers.servers["shop"].network.upstreams; !slices.Equal(got, []string{"192.0.2.2:53"}) {
		t.Errorf("upstreams after a change = %q, want the network's own", got)
	}

	servers.Stop("shop")
	servers.Stop("shop") // twice is fine
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", addr)
	if err == nil {
		_ = conn.Close()
		t.Error("a stopped server still accepts connections")
	}

	// An address the host does not have cannot be served.
	if err := servers.Serve(t.Context(), types.Network{
		Name: "elsewhere", Subnet: "192.0.2.0/24", Gateway: "192.0.2.1",
	}); err == nil {
		t.Error("serving on an address the host does not have succeeded")
	}
}
