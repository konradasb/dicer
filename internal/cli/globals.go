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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer/internal/cli/remote"
)

// debugEnv turns on --debug for every command.
const debugEnv = "DICER_DEBUG"

// addGlobalFlags adds the flags every command takes.
func addGlobalFlags(cmd *cobra.Command) {
	flags := cmd.PersistentFlags()
	flags.StringP("remote", "r", "",
		"Daemon to talk to: a remote's name, or a unix:// socket address "+
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

// dialOptions are what every connection has: a trace of each call for
// --debug, a bound on each call for --timeout, and, for a daemon described
// as daemon, a plain account of failing to reach it.
func dialOptions(cmd *cobra.Command, daemon string) []grpc.DialOption {
	var (
		unary  []grpc.UnaryClientInterceptor
		stream []grpc.StreamClientInterceptor
	)

	if daemon != "" {
		unary = append(unary, func(
			ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
			invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
		) error {
			return unreachable(daemon, invoker(ctx, method, req, reply, cc, opts...))
		})
		stream = append(stream, func(
			ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string,
			streamer grpc.Streamer, opts ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			s, err := streamer(ctx, desc, cc, method, opts...)
			if err != nil {
				return nil, unreachable(daemon, err)
			}
			return &reachStream{ClientStream: s, daemon: daemon}, nil
		})
	}

	if timeout, _ := cmd.Flags().GetDuration("timeout"); timeout > 0 {
		unary = append(unary, timeoutInterceptor(timeout))
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

// timeoutInterceptor bounds each unary call, and says which bound it hit
// rather than just "context deadline exceeded".
func timeoutInterceptor(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
	) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		err := invoker(ctx, method, req, reply, cc, opts...)
		if status.Code(err) == codes.DeadlineExceeded && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return status.Errorf(codes.DeadlineExceeded,
				"%s took longer than --timeout %s", path.Base(method), timeout)
		}
		return err
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

// transportFailure is how gRPC words a call that never reached the daemon,
// and the cause it gives: `connection error: desc = "transport: Error
// while dialing: dial tcp 192.0.2.1:7443: connect: connection refused"`.
var transportFailure = regexp.MustCompile(`^connection error: desc = "transport: (.*)"$`)

// unreachable rewords a call's failure to reach the daemon as that, with
// the cause as the operating system put it:
//
//	cannot reach the Dicer daemon at prod (tcp://192.0.2.1:7443): connection refused
//
// Any other error, the daemon's own Unavailable among them, is returned
// as it was.
func unreachable(daemon string, err error) error {
	s, ok := status.FromError(err)
	if !ok || s.Code() != codes.Unavailable {
		return err
	}
	m := transportFailure.FindStringSubmatch(s.Message())
	if m == nil {
		return err
	}

	cause := m[1]
	switch {
	case strings.Contains(cause, "authentication handshake failed: "):
		_, cause, _ = strings.Cut(cause, "authentication handshake failed: ")
		cause = "the TLS handshake failed: " + cause
	case strings.LastIndex(cause, ": ") >= 0:
		// "Error while dialing: dial tcp ...: connect: connection refused"
		cause = cause[strings.LastIndex(cause, ": ")+2:]
	}

	return status.Errorf(codes.Unavailable, "cannot reach the Dicer daemon at %s: %s", daemon, cause)
}

// reachStream rewords a stream's failure to reach the daemon, which shows
// only once it is used.
type reachStream struct {
	grpc.ClientStream

	daemon string
}

func (s *reachStream) SendMsg(m any) error { return unreachable(s.daemon, s.ClientStream.SendMsg(m)) }
func (s *reachStream) RecvMsg(m any) error { return unreachable(s.daemon, s.ClientStream.RecvMsg(m)) }
