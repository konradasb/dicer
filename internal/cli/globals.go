// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer/internal/cli/remote"
)

// debugEnv turns on --debug for every command.
const debugEnv = "DICER_DEBUG"

// addGlobalFlags adds the flags every command takes.
func addGlobalFlags(cmd *cobra.Command) {
	flags := cmd.PersistentFlags()
	flags.StringP("remote", "r", "",
		"Daemon to talk to: a remote's name, or an address (unix:///PATH or HOST:PORT) "+
			"(default $"+remoteEnv+", then the current remote, then "+remote.Local+")")
	flags.BoolP("debug", "D", false, "Trace every call to the daemon on stderr (or set $"+debugEnv+")")
	flags.Duration("timeout", 0, "Give up on a call to the daemon after this long, e.g. 30s "+
		"(0: never; streams such as logs -f and exec are not bounded)")

	_ = cmd.RegisterFlagCompletionFunc("remote", completeRemotes)
}

// validateGlobalFlags checks the global flags a command was given, before
// it runs.
func validateGlobalFlags(cmd *cobra.Command, _ []string) error {
	if d, _ := cmd.Flags().GetDuration("timeout"); d < 0 {
		return usagef(cmd, "invalid --timeout %s: it cannot be negative", d)
	}

	return checkRequiredFlags(cmd)
}

// debugging reports whether --debug or $DICER_DEBUG asks for tracing.
func debugging(cmd *cobra.Command) bool {
	if on, _ := cmd.Flags().GetBool("debug"); on {
		return true
	}
	on, _ := strconv.ParseBool(os.Getenv(debugEnv))
	return on
}

// isTerminal reports whether w is a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// tracer writes --debug's lines to stderr. The lines of concurrent calls,
// which completion and streams can make, do not interleave.
type tracer struct {
	mu  sync.Mutex
	out io.Writer
}

func newTracer(cmd *cobra.Command) *tracer {
	return &tracer{out: cmd.ErrOrStderr()}
}

// printf writes one line, marked as a debug one and timed.
func (t *tracer) printf(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()

	_, _ = fmt.Fprintf(t.out, "debug %s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// call writes how a call ended: its method, status and duration, and the
// whole error if it failed.
func (t *tracer) call(method string, start time.Time, err error) {
	line := fmt.Sprintf("%s %s %s",
		path.Base(method), status.Code(err), time.Since(start).Round(100*time.Microsecond))
	if err != nil {
		line += "\n  " + err.Error()
	}
	t.printf("%s", line)
}

// dialOptions are what every connection has: a bound on each unary call for
// --timeout, and a trace of each call for --debug.
func dialOptions(cmd *cobra.Command) []grpc.DialOption {
	var (
		unary  []grpc.UnaryClientInterceptor
		stream []grpc.StreamClientInterceptor
	)

	if timeout, _ := cmd.Flags().GetDuration("timeout"); timeout > 0 {
		unary = append(unary, func(
			ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
			invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
		) error {
			parent := ctx
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// The daemon is sent the deadline too, and its timer may run out
			// before ours: a deadline passed while the caller's context lives
			// is --timeout's either way.
			err := invoker(ctx, method, req, reply, cc, opts...)
			if status.Code(err) == codes.DeadlineExceeded && parent.Err() == nil {
				return fmt.Errorf("%s took longer than --timeout %s", path.Base(method), timeout)
			}
			return err
		})
	}

	if debugging(cmd) {
		tr := newTracer(cmd)
		unary = append(unary, func(
			ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
			invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
		) error {
			start := time.Now()
			err := invoker(ctx, method, req, reply, cc, opts...)
			tr.call(method, start, err)
			return err
		})
		stream = append(stream, func(
			ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string,
			streamer grpc.Streamer, opts ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			tr.printf("%s opening stream", path.Base(method))
			start := time.Now()
			s, err := streamer(ctx, desc, cc, method, opts...)
			if err != nil {
				tr.call(method, start, err)
				return nil, err
			}
			return &tracedStream{ClientStream: s, tracer: tr, method: method, start: start}, nil
		})
	}

	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(unary...),
		grpc.WithChainStreamInterceptor(stream...),
	}
}

// tracedStream reports when a stream ends, and how.
type tracedStream struct {
	grpc.ClientStream

	tracer *tracer
	method string
	start  time.Time
	once   sync.Once
}

func (s *tracedStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.once.Do(func() {
			ended := err
			if errors.Is(ended, io.EOF) {
				ended = nil // the stream's normal end
			}
			s.tracer.call(s.method, s.start, ended)
		})
	}
	return err
}
