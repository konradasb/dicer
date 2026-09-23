// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"

	"github.com/dicer-sh/dicer/internal/guest"
	diceragentv1 "github.com/dicer-sh/dicer/proto/diceragent/v1"
)

// Exit statuses for a command that could not be started, following the shell
// convention.
const (
	exitNotExecutable = 126
	exitNotFound      = 127
	exitTimedOut      = 124 // as GNU timeout reports it
)

// sender serialises writes to an exec stream: gRPC allows one concurrent
// sender, and stdout, stderr and the exit status come from different
// goroutines.
type sender struct {
	mu     sync.Mutex
	stream diceragentv1.AgentService_ExecServer
}

func (s *sender) send(resp *diceragentv1.ExecResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(resp)
}

func (s *sender) exit(code int32) error {
	return s.send(&diceragentv1.ExecResponse{
		Payload: &diceragentv1.ExecResponse_ExitCode{ExitCode: code},
	})
}

// output is an io.Writer that forwards each write to the stream as it
// happens, so a long-running command's output arrives while it runs.
type output struct {
	s      *sender
	stderr bool
}

func (o output) Write(p []byte) (int, error) {
	// The stream may retain the message after Send returns, and the caller
	// reuses p, so it must be copied.
	data := bytes.Clone(p)

	resp := &diceragentv1.ExecResponse{Payload: &diceragentv1.ExecResponse_Stdout{Stdout: data}}
	if o.stderr {
		resp = &diceragentv1.ExecResponse{Payload: &diceragentv1.ExecResponse_Stderr{Stderr: data}}
	}

	if err := o.s.send(resp); err != nil {
		return 0, err
	}
	return len(p), nil
}

// execPlain runs a command without a terminal, streaming stdout and stderr
// separately and finishing with the exit status.
func execPlain(
	ctx context.Context, stream diceragentv1.AgentService_ExecServer, start *diceragentv1.ExecStart, command []string,
) error {
	s := &sender{stream: stream}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = buildEnv(start.GetEnv(), false)
	cmd.Dir = start.GetCwd()
	cmd.Stdout = output{s: s}
	cmd.Stderr = output{s: s, stderr: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return reportStartError(s, err, output{s: s, stderr: true}, "\n")
	}

	go forwardInput(stream, stdin, nil)

	// Wait returns once the process has exited and its output has been
	// copied to the stream.
	return s.exit(exitCodeOf(ctx, cmd, cmd.Wait()))
}

// execTTY runs a command on a pseudo-terminal. A terminal has one output
// stream, so everything arrives as stdout.
func execTTY(
	ctx context.Context, stream diceragentv1.AgentService_ExecServer, start *diceragentv1.ExecStart, command []string,
) error {
	s := &sender{stream: stream}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = buildEnv(start.GetEnv(), true)
	cmd.Dir = start.GetCwd()

	rows, cols := start.GetRows(), start.GetCols()
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return reportStartError(s, err, output{s: s}, "\r\n")
	}
	defer func() { _ = ptmx.Close() }()

	go forwardInput(stream, ptmx, ptmx)

	// The terminal reports EIO once the command exits and its side closes;
	// that is the end of output, not an error worth reporting.
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = io.Copy(output{s: s}, ptmx) })

	waitErr := cmd.Wait()
	wg.Wait()

	return s.exit(exitCodeOf(ctx, cmd, waitErr))
}

// forwardInput copies stdin from the stream to w until the client closes its
// side, and applies resize events to the terminal if there is one.
func forwardInput(stream diceragentv1.AgentService_ExecServer, w io.WriteCloser, tty *os.File) {
	defer func() {
		if tty == nil {
			_ = w.Close()
		}
	}()

	for {
		req, err := stream.Recv()
		if err != nil {
			return
		}
		if data := req.GetStdin(); len(data) > 0 {
			_, _ = w.Write(data)
		}
		if r := req.GetResize(); r != nil && tty != nil {
			_ = pty.Setsize(tty, &pty.Winsize{Rows: uint16(r.GetRows()), Cols: uint16(r.GetCols())})
		}
	}
}

// reportStartError tells the client why a command could not start and ends
// the stream with the conventional status.
func reportStartError(s *sender, err error, w io.Writer, newline string) error {
	code := int32(exitNotExecutable)
	if errors.Is(err, exec.ErrNotFound) {
		code = exitNotFound
	}

	_, _ = io.WriteString(w, err.Error()+newline)
	return s.exit(code)
}

// buildEnv merges user-supplied variables over the agent's own environment.
// A terminal session also gets sensible terminal defaults unless overridden.
func buildEnv(envMap map[string]string, tty bool) []string {
	overrides := make(map[string]string, len(envMap))
	if tty {
		overrides["TERM"] = "xterm-256color"
		overrides["LANG"] = "C.UTF-8"
		overrides["LC_ALL"] = "C.UTF-8"
		overrides["COLORTERM"] = "truecolor"
	}
	maps.Copy(overrides, envMap)

	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if _, ok := overrides[k]; !ok {
			env = append(env, e)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// exitCodeOf is the exit status of a finished command as a shell reports
// it: 124 if the request's timeout, which ctx carries, killed it, as GNU
// timeout reports that; otherwise its exit code, or 128 plus the signal that
// killed it -- 137 for the OOM killer's SIGKILL.
func exitCodeOf(ctx context.Context, cmd *exec.Cmd, waitErr error) int32 {
	ps := cmd.ProcessState
	if ps == nil {
		// Wait failed before the process could be reaped. Nothing says how
		// it ended; it did not succeed.
		slog.Warn("command ended without an exit status", "error", waitErr)
		return 1
	}

	if !ps.Exited() && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return exitTimedOut
	}
	return int32(guest.ExitStatus(ps))
}
