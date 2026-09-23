// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// maxProbeOutput bounds what a probe reports of itself. Its output is shown
// to a person, not parsed, and a chatty health command must not flood the
// host.
const maxProbeOutput = 4096

// Probe implements diceragentv1.AgentServiceServer: it runs one health check
// probe, within the request's timeout.
//
// A probe that fails is a healthy answer to the RPC, reported in the
// response; an error is kept for a request that is not a probe at all.
func (s *server) Probe(ctx context.Context, req *diceragentv1.ProbeRequest) (*diceragentv1.ProbeResponse, error) {
	timeout := req.GetTimeout().AsDuration()
	if timeout <= 0 {
		return nil, status.Error(codes.InvalidArgument, "a probe needs a positive timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var output string
	var err error
	switch p := req.GetProbe().(type) {
	case *diceragentv1.ProbeRequest_Exec:
		if len(p.Exec.GetCommand()) == 0 {
			return nil, status.Error(codes.InvalidArgument, "an exec probe needs a command")
		}
		output, err = probeExec(ctx, p.Exec.GetCommand())
	case *diceragentv1.ProbeRequest_Http:
		output, err = probeHTTP(ctx, p.Http.GetPort(), p.Http.GetPath())
	case *diceragentv1.ProbeRequest_Tcp:
		output, err = probeTCP(ctx, p.Tcp.GetPort())
	default:
		return nil, status.Error(codes.InvalidArgument, "the request names no probe")
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		output = joinOutput(output, err.Error())
	}

	return &diceragentv1.ProbeResponse{Healthy: err == nil, Output: truncate(output, maxProbeOutput)}, nil
}

// probeExec runs a command, in the environment an exec gets. It passes if
// the command exits 0; what it printed is its output either way.
func probeExec(ctx context.Context, command []string) (string, error) {
	var out boundedBuffer
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = buildEnv(nil, false)
	cmd.Stdout = &out
	cmd.Stderr = &out
	// A command that leaves a child holding its output open must not hold
	// the probe past its timeout.
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = fmt.Errorf("exited with code %d", exitErr.ExitCode())
	}
	return out.String(), err
}

// probeHTTP gets path on port, and passes on a 2xx or 3xx status. A redirect
// is an answer, not something to follow: the service is up.
func probeHTTP(ctx context.Context, port uint32, path string) (string, error) {
	if path == "" {
		path = "/"
	}
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(port), 10)) + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return resp.Status, errors.New("unhealthy HTTP status")
	}
	return resp.Status, nil
}

// probeTCP passes if a connection to port opens.
func probeTCP(ctx context.Context, port uint32) (string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(port), 10)))
	if err != nil {
		return "", err
	}
	_ = conn.Close()
	return "connected", nil
}

// boundedBuffer keeps the first maxProbeOutput bytes written to it, and
// accepts the rest without keeping it: a command must not be stopped by its
// own output, nor the agent by a command's.
type boundedBuffer struct {
	buf []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := maxProbeOutput - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.buf) }

// joinOutput puts why a probe failed after what it said, if it said
// anything.
func joinOutput(output, reason string) string {
	if output == "" {
		return reason
	}
	if output[len(output)-1] != '\n' {
		output += "\n"
	}
	return output + reason
}

// truncate cuts s to at most n bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
