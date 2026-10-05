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
	"time"

	"github.com/creack/pty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/guest"
	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// Exit statuses for a command that could not be started, following the shell
// convention.
const (
	exitNotExecutable = 126
	exitNotFound      = 127
	exitTimedOut      = 124 // as GNU timeout reports it
)

// Exec runs a command for the life of the stream. The first client message
// must be an ExecStart; later ones carry stdin or terminal resizes.
func (s *server) Exec(stream diceragentv1.AgentService_ExecServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	start := req.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first message must be an ExecStart")
	}

	command := start.GetCommand()
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	slog.Info("exec", "command", command, "tty", start.GetTty(), "workdir", start.GetCwd(), "timeout", start.GetTimeoutSeconds())

	ctx := stream.Context()
	if start.GetTimeoutSeconds() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(start.GetTimeoutSeconds())*time.Second)
		defer cancel()
	}

	if start.GetTty() {
		return execTerminal(ctx, stream, start, command)
	}
	return execPlain(ctx, stream, start, command)
}

// sender serialises writes to an exec stream: gRPC allows one concurrent
// sender, and stdout, stderr and the exit status come from different
// goroutines.
type sender struct {
	mu     sync.Mutex
	stream diceragentv1.AgentService_ExecServer
}

// send sends resp on the stream. It is safe for concurrent use.
func (s *sender) send(resp *diceragentv1.ExecResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(resp)
}

// sendExitCode ends the exec with the command's exit code.
func (s *sender) sendExitCode(code int32) error {
	return s.send(&diceragentv1.ExecResponse{
		Payload: &diceragentv1.ExecResponse_ExitCode{ExitCode: code},
	})
}

// execOutput is an io.Writer that forwards each write to the stream as it
// happens, so a long-running command's output arrives while it runs. It
// sends stdout unless stderr is set.
type execOutput struct {
	sender *sender
	stderr bool
}

func (o execOutput) Write(p []byte) (int, error) {
	// The stream may retain the message after Send returns, and the caller
	// reuses p, so it must be copied.
	data := bytes.Clone(p)

	resp := &diceragentv1.ExecResponse{Payload: &diceragentv1.ExecResponse_Stdout{Stdout: data}}
	if o.stderr {
		resp = &diceragentv1.ExecResponse{Payload: &diceragentv1.ExecResponse_Stderr{Stderr: data}}
	}

	if err := o.sender.send(resp); err != nil {
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
	cmd.Env = execEnv(start.GetEnv(), false)
	cmd.Dir = start.GetCwd()
	cmd.Stdout = execOutput{sender: s}
	cmd.Stderr = execOutput{sender: s, stderr: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return reportStartError(s, err, execOutput{sender: s, stderr: true}, "\n")
	}

	go forwardInput(stream, stdin, nil)

	// Wait returns once the process has exited and its output has been
	// copied to the stream.
	return s.sendExitCode(exitCodeOf(ctx, cmd, cmd.Wait()))
}

// execTerminal runs a command on a pseudo-terminal. A terminal has one
// output stream, so everything arrives as stdout.
func execTerminal(
	ctx context.Context, stream diceragentv1.AgentService_ExecServer, start *diceragentv1.ExecStart, command []string,
) error {
	s := &sender{stream: stream}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = execEnv(start.GetEnv(), true)
	cmd.Dir = start.GetCwd()

	rows, cols := start.GetRows(), start.GetCols()
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}

	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return reportStartError(s, err, execOutput{sender: s}, "\r\n")
	}
	defer func() { _ = terminal.Close() }()

	go forwardInput(stream, terminal, terminal)

	// The terminal reports EIO once the command exits and its side closes;
	// that is the end of output, not an error worth reporting.
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = io.Copy(execOutput{sender: s}, terminal) })

	waitErr := cmd.Wait()
	wg.Wait()

	return s.sendExitCode(exitCodeOf(ctx, cmd, waitErr))
}

// forwardInput copies stdin from the stream to w until the client closes its
// side, and applies resize events to the terminal if there is one. It closes
// w unless w is the terminal, which the command's runner closes.
func forwardInput(stream diceragentv1.AgentService_ExecServer, w io.WriteCloser, terminal *os.File) {
	defer func() {
		if terminal == nil {
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
		if r := req.GetResize(); r != nil && terminal != nil {
			_ = pty.Setsize(terminal, &pty.Winsize{Rows: uint16(r.GetRows()), Cols: uint16(r.GetCols())})
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
	return s.sendExitCode(code)
}

// execEnv returns the environment a command runs with: env merged over the
// agent's own environment. A terminal session also gets terminal defaults
// unless env overrides them. A nil env adds nothing.
func execEnv(env map[string]string, terminal bool) []string {
	overrides := make(map[string]string, len(env))
	if terminal {
		overrides["TERM"] = "xterm-256color"
		overrides["LANG"] = "C.UTF-8"
		overrides["LC_ALL"] = "C.UTF-8"
		overrides["COLORTERM"] = "truecolor"
	}
	maps.Copy(overrides, env)

	agentEnv := os.Environ()
	merged := make([]string, 0, len(agentEnv)+len(overrides))
	for _, e := range agentEnv {
		k, _, _ := strings.Cut(e, "=")
		if _, ok := overrides[k]; !ok {
			merged = append(merged, e)
		}
	}
	for k, v := range overrides {
		merged = append(merged, k+"="+v)
	}
	return merged
}

// exitCodeOf returns a finished command's exit status as a shell reports it,
// or exitTimedOut if ctx's timeout killed it.
func exitCodeOf(ctx context.Context, cmd *exec.Cmd, waitErr error) int32 {
	state := cmd.ProcessState
	if state == nil {
		// Wait failed before the process could be reaped. Nothing says how
		// it ended; it did not succeed.
		slog.Warn("command ended without an exit status", "error", waitErr)
		return 1
	}

	if !state.Exited() && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return exitTimedOut
	}
	return int32(guest.ExitStatus(state))
}
