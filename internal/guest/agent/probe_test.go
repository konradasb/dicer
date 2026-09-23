// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

func probe(t *testing.T, req *diceragentv1.ProbeRequest) *diceragentv1.ProbeResponse {
	t.Helper()

	if req.GetTimeout() == nil {
		req.Timeout = durationpb.New(5 * time.Second)
	}
	resp, err := (&server{}).Probe(t.Context(), req)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return resp
}

func execProbe(command ...string) *diceragentv1.ProbeRequest {
	return &diceragentv1.ProbeRequest{Probe: &diceragentv1.ProbeRequest_Exec{
		Exec: &diceragentv1.ExecProbe{Command: command},
	}}
}

func TestExecProbe(t *testing.T) {
	tests := []struct {
		name    string
		req     *diceragentv1.ProbeRequest
		healthy bool
		output  string
	}{
		{"exit 0", execProbe("sh", "-c", "echo ready"), true, "ready"},
		{"exit 1", execProbe("sh", "-c", "echo not yet >&2; exit 1"), false, "not yet\nexited with code 1"},
		{"not found", execProbe("/no/such/command"), false, "no such file"},
		{
			"overruns its timeout",
			&diceragentv1.ProbeRequest{
				Probe:   execProbe("sleep", "10").GetProbe(),
				Timeout: durationpb.New(100 * time.Millisecond),
			},
			false, "timed out after 100ms",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := probe(t, tt.req)
			if resp.GetHealthy() != tt.healthy || !strings.Contains(resp.GetOutput(), tt.output) {
				t.Errorf("Probe = healthy %v, %q; want %v, mentioning %q",
					resp.GetHealthy(), resp.GetOutput(), tt.healthy, tt.output)
			}
		})
	}
}

// A health command that prints without end is cut off, not buffered.
func TestExecProbeBoundsOutput(t *testing.T) {
	resp := probe(t, execProbe("sh", "-c", "head -c 100000 /dev/zero | tr '\\0' x"))
	if !resp.GetHealthy() || len(resp.GetOutput()) > maxProbeOutput {
		t.Errorf("Probe = healthy %v, %d bytes; want healthy, at most %d", resp.GetHealthy(), len(resp.GetOutput()), maxProbeOutput)
	}
}

// portOf returns the port a test server listens on.
func portOf(t *testing.T, addr string) uint32 {
	t.Helper()

	tcp, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return uint32(tcp.Port)
}

func TestHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/moved":
			// Followed, this would fail: a redirect is an answer.
			http.Redirect(w, r, "/broken", http.StatusFound)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	port := portOf(t, srv.Listener.Addr().String())

	tests := []struct {
		path    string
		healthy bool
		output  string
	}{
		{"/ok", true, "200 OK"},
		{"/moved", true, "302 Found"},
		{"/broken", false, "503 Service Unavailable"},
	}
	for _, tt := range tests {
		resp := probe(t, &diceragentv1.ProbeRequest{Probe: &diceragentv1.ProbeRequest_Http{
			Http: &diceragentv1.HTTPProbe{Port: port, Path: tt.path},
		}})
		if resp.GetHealthy() != tt.healthy || !strings.Contains(resp.GetOutput(), tt.output) {
			t.Errorf("GET %s = healthy %v, %q; want %v, %q", tt.path, resp.GetHealthy(), resp.GetOutput(), tt.healthy, tt.output)
		}
	}
}

func TestTCPProbe(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portOf(t, l.Addr().String())

	tcp := func() *diceragentv1.ProbeRequest {
		return &diceragentv1.ProbeRequest{Probe: &diceragentv1.ProbeRequest_Tcp{Tcp: &diceragentv1.TCPProbe{Port: port}}}
	}

	if resp := probe(t, tcp()); !resp.GetHealthy() {
		t.Errorf("open port = %q, want healthy", resp.GetOutput())
	}

	_ = l.Close()
	if resp := probe(t, tcp()); resp.GetHealthy() || !strings.Contains(resp.GetOutput(), "refused") {
		t.Errorf("closed port = healthy %v, %q; want a refusal", resp.GetHealthy(), resp.GetOutput())
	}
}

func TestProbeRejectsRequestsThatAreNotProbes(t *testing.T) {
	for name, req := range map[string]*diceragentv1.ProbeRequest{
		"no probe":   {Timeout: durationpb.New(time.Second)},
		"no command": {Probe: execProbe().GetProbe(), Timeout: durationpb.New(time.Second)},
		"no timeout": execProbe("true"),
	} {
		if _, err := (&server{}).Probe(t.Context(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: Probe = %v, want InvalidArgument", name, err)
		}
	}
}
