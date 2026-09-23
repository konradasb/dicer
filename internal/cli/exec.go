// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/dicer-sh/dicer"
)

func newInstanceExecCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [flags] NAME [COMMAND [ARG...]]",
		Short: "Run a command inside a running instance",
		Long: "Runs a command inside a running instance, /bin/sh if none is given.\n\n" +
			"A pseudo-TTY is allocated when this terminal is on both ends -- stdin and\n" +
			"stdout -- so a shell is interactive, and output piped elsewhere is not\n" +
			"mangled by one. -t and -T force it on or off. Flags go before the name:\n" +
			"everything after it is the command's.",
		Example: "  dicer exec web\n" +
			"  dicer exec web ls -la /srv\n" +
			"  dicer exec -e DEBUG=1 -w /srv web ./check.sh\n" +
			"  dicer exec -T web cat /var/log/app.log > app.log",
		RunE: runInstanceExecCommand,
		Args: oneThenCommand("an instance name"),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveDefault
			}
			return complete(1, instancesIn(dicer.StateRunning))(cmd, args, toComplete)
		},
	}

	// Everything after the name is the guest command's, flags and all:
	// 'dicer exec web ls -la' runs ls -la.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().SortFlags = false
	cmd.Flags().BoolP("tty", "t", false, "Allocate a pseudo-TTY (default: when stdin and stdout are a terminal)")
	cmd.Flags().BoolP("no-tty", "T", false, "Do not allocate a pseudo-TTY")
	cmd.Flags().BoolP("interactive", "i", true, "Accepted for Docker compatibility; stdin is always forwarded")
	_ = cmd.Flags().MarkHidden("interactive")
	cmd.Flags().StringArrayP("env", "e", nil,
		"Environment variable as KEY=VALUE, or KEY to pass this shell's value (repeatable)")
	cmd.Flags().StringP("workdir", "w", "", "Working directory inside the instance")
	cmd.Flags().Int32("timeout", 0, "Kill the command after this many seconds (0: no limit)")
	cmd.MarkFlagsMutuallyExclusive("tty", "no-tty")

	return cmd
}

// wantTTY decides whether exec allocates a pseudo-TTY: when asked to, or
// else when this terminal is on both ends. A TTY on a pipe would turn its
// newlines into CRLFs and merge stderr into stdout.
func wantTTY(force, never, stdinTTY, stdoutTTY bool) bool {
	switch {
	case never:
		return false
	case force:
		return true
	default:
		return stdinTTY && stdoutTTY
	}
}

func runInstanceExecCommand(cmd *cobra.Command, args []string) error {
	name, opts, err := execOptions(cmd, args)
	if err != nil {
		return err
	}

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	// Cancel the stream on SIGINT/SIGTERM so Ctrl+C tears the connection
	// down cleanly. In TTY mode the terminal is raw, so Ctrl+C arrives as a
	// 0x03 byte on stdin and is forwarded to the guest process instead; a
	// signal-based SIGINT (kill -2) still cancels here.
	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	stream, err := client.ExecInstance(ctx, name, opts)
	if err != nil {
		return err
	}

	if opts.TTY {
		stdin := int(os.Stdin.Fd())
		if oldState, err := term.MakeRaw(stdin); err == nil {
			defer func() { _ = term.Restore(stdin, oldState) }()
		}
		go forwardResizes(stream, stdin)
	}
	go forwardStdin(stream)

	exitCode, err := receiveOutput(ctx, stream)
	if err != nil {
		return suggest(cmd.Context(), client, instancesIn(), name, err)
	}
	if exitCode != 0 {
		// The guest command's own output already explains the failure; pass
		// its status through without adding a message of ours.
		return &exitError{code: exitCode}
	}

	return nil
}

// execOptions builds the command args name from the command line's flags and
// this terminal, and returns the instance to run it in.
func execOptions(cmd *cobra.Command, args []string) (string, dicer.ExecOptions, error) {
	command := trimDash(args[1:])
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	ttyFlag, _ := cmd.Flags().GetBool("tty")
	noTTY, _ := cmd.Flags().GetBool("no-tty")
	envSpecs, _ := cmd.Flags().GetStringArray("env")
	workdir, _ := cmd.Flags().GetString("workdir")
	timeout, _ := cmd.Flags().GetInt32("timeout")

	env, err := parseEnv(envSpecs, nil)
	if err != nil {
		return "", dicer.ExecOptions{}, usagef(cmd, "%s", err)
	}

	opts := dicer.ExecOptions{
		Command: command,
		TTY:     wantTTY(ttyFlag, noTTY, term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd()))),
		Cwd:     workdir,
		Timeout: time.Duration(timeout) * time.Second,
		Env:     env,
	}
	if opts.TTY {
		if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
			opts.Rows, opts.Cols = uint16(h), uint16(w)
		}
	}

	return args[0], opts, nil
}

// forwardResizes tells the guest the terminal's new size each time it
// changes.
func forwardResizes(stream *dicer.ExecStream, tty int) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	for range winch {
		w, h, err := term.GetSize(tty)
		if err != nil {
			continue
		}
		_ = stream.Resize(uint16(h), uint16(w))
	}
}

// forwardStdin sends stdin to the guest command until either ends.
func forwardStdin(stream *dicer.ExecStream) {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(buf[:n]); sendErr != nil {
				return
			}
		}
		if err != nil {
			_ = stream.CloseSend()

			return
		}
	}
}

// receiveOutput writes the guest command's output to stdout and stderr until
// the stream ends, and returns its exit code.
func receiveOutput(ctx context.Context, stream *dicer.ExecStream) (int, error) {
	exitCode := 0
	for {
		out, err := stream.Recv()
		if err != nil {
			return exitCode, ignoreStreamEnd(ctx, err)
		}

		switch {
		case out.Stdout != nil:
			_, _ = os.Stdout.Write(out.Stdout)
		case out.Stderr != nil:
			_, _ = os.Stderr.Write(out.Stderr)
		case out.ExitCode != nil:
			exitCode = *out.ExitCode
		}
	}
}

// ignoreStreamEnd returns nil for the end of the stream, or its cancellation
// by a signal, which are a clean exit, and err otherwise.
func ignoreStreamEnd(ctx context.Context, err error) error {
	if !errors.Is(err, io.EOF) && ctx.Err() == nil {
		return err
	}
	return nil
}
