// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"bytes"
	"context"
	"sync"
	"time"

	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// ExecOptions is a command to run inside a running instance.
type ExecOptions struct {
	// Command is the command and its arguments. Empty runs /bin/sh.
	Command []string

	// TTY allocates a pseudo-terminal, which a shell needs to be
	// interactive. It also merges the command's stderr into its stdout, as
	// a terminal does, so output meant for a pipe should not ask for one.
	TTY bool

	// Rows and Cols are the terminal's initial size, when TTY is set.
	Rows, Cols uint16

	// Cwd is the working directory inside the guest.
	Cwd string

	// Timeout kills the command after it elapses. Zero means no limit. It
	// is rounded down to a second, which is what the API carries.
	Timeout time.Duration

	// Env is added to the command's environment.
	Env map[string]string
}

// ExecStream is a command running inside a guest: its input, its output and
// its exit status.
//
// Send, Resize and CloseSend may be called from a different goroutine than
// Recv, and are serialised against each other. Recv is for one goroutine.
type ExecStream struct {
	stream dicerdv1.DaemonService_ExecInstanceClient

	// sendMu serialises sends, which gRPC does not allow from more than one
	// goroutine at a time -- and forwarding a terminal means sending input
	// and resizes from two.
	sendMu sync.Mutex
}

// ExecOutput is one message from a running command: output on one of its
// streams, or the status it exited with.
//
// Exactly one field is set. ExitCode is set on the last message of the
// stream, and only there.
type ExecOutput struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode *int
}

// ExecInstance starts a command inside a running instance.
//
// The command runs until it exits, the timeout kills it, or the context is
// cancelled. The caller closes the stream by cancelling the context, and
// should read from it until Recv returns io.EOF or the exit status.
func (c *Client) ExecInstance(ctx context.Context, name string, opts ExecOptions) (*ExecStream, error) {
	stream, err := c.daemon.ExecInstance(ctx)
	if err != nil {
		return nil, err
	}

	command := opts.Command
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	start := &dicerdv1.ExecInstanceStart{
		Name:           name,
		Command:        command,
		Tty:            opts.TTY,
		Cwd:            opts.Cwd,
		TimeoutSeconds: int32(opts.Timeout / time.Second),
		Rows:           uint32(opts.Rows),
		Cols:           uint32(opts.Cols),
		Env:            opts.Env,
	}

	// An error here means the daemon has already given up on the stream,
	// and the first Recv says why. It is not reported twice.
	if err := stream.Send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Start{Start: start},
	}); err != nil && !isStreamClosed(err) {
		return nil, err
	}

	return &ExecStream{stream: stream}, nil
}

// Send writes to the command's standard input. The bytes are copied, so the
// caller may reuse its buffer.
func (s *ExecStream) Send(p []byte) error {
	return s.send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Stdin{Stdin: bytes.Clone(p)},
	})
}

// Resize tells the guest the terminal's new size. It is for a stream started
// with a TTY, and does nothing on one without.
func (s *ExecStream) Resize(rows, cols uint16) error {
	return s.send(&dicerdv1.ExecInstanceRequest{
		Payload: &dicerdv1.ExecInstanceRequest_Resize{
			Resize: &dicerdv1.ExecInstanceResize{Rows: uint32(rows), Cols: uint32(cols)},
		},
	})
}

// CloseSend says there is no more input, which is what a command reading to
// end-of-file waits for. Output goes on arriving afterwards.
func (s *ExecStream) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	return s.stream.CloseSend()
}

// Recv returns the next message from the command: output, or the status it
// exited with. It returns io.EOF when the stream ends.
func (s *ExecStream) Recv() (ExecOutput, error) {
	resp, err := s.stream.Recv()
	if err != nil {
		return ExecOutput{}, err
	}

	switch p := resp.GetPayload().(type) {
	case *dicerdv1.ExecInstanceResponse_Stdout:
		return ExecOutput{Stdout: p.Stdout}, nil
	case *dicerdv1.ExecInstanceResponse_Stderr:
		return ExecOutput{Stderr: p.Stderr}, nil
	case *dicerdv1.ExecInstanceResponse_ExitCode:
		code := int(p.ExitCode)
		return ExecOutput{ExitCode: &code}, nil
	default:
		// A message this client does not know is not an error: a newer
		// daemon may send one, and the stream carries on.
		return ExecOutput{}, nil
	}
}

func (s *ExecStream) send(req *dicerdv1.ExecInstanceRequest) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	return s.stream.Send(req)
}
