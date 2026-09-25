// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

const (
	// consolePollInterval is how often an attached run looks for the console
	// of an instance still starting.
	consolePollInterval = 50 * time.Millisecond

	// consoleDrainTimeout bounds how long an attached run waits, once the
	// instance has stopped, for the console's last lines.
	consoleDrainTimeout = 5 * time.Second

	// statusInterrupted is an attached run's exit status when a second
	// Ctrl+C leaves the instance to stop on its own: a shell's for SIGINT.
	statusInterrupted = 130
)

// runAttached runs an instance as docker run does without -d: it writes the
// guest's console until the instance stops, and exits with the status it
// ended with. Ctrl+C stops the instance; a second one stops waiting for it.
func runAttached(cmd *cobra.Command, req *dicerdv1.CreateInstanceRequest) error {
	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := ensureImage(cmd, client, req.GetImageRef()); err != nil {
		return err
	}

	// Defined first and started after, so that the console is followed from
	// its first line: a job --rm deletes as it ends leaves none to read later.
	req.Start = false
	inst, err := client.CreateInstance(cmd.Context(), req)
	if err != nil {
		return err
	}
	name := inst.GetName()

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	started := make(chan struct{})
	console := make(chan error, 1)
	go func() { console <- followConsole(ctx, client, name, started, cmd.OutOrStdout()) }()

	t := startTask(cmd, "Starting "+name)
	_, err = client.StartInstance(ctx, &dicerdv1.StartInstanceRequest{Name: name})
	t.end()
	if err != nil {
		cancel()
		if req.GetRemoveOnExit() {
			// It never ran, so never ended, and nothing else deletes it.
			_, _ = client.DeleteInstance(context.WithoutCancel(ctx), &dicerdv1.DeleteInstanceRequest{Name: name, Force: true})
		}
		return err
	}
	close(started)

	interrupts := make(chan os.Signal, 2)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)

	type result struct {
		code int
		err  error
	}
	exited := make(chan result, 1)
	go func() {
		code, err := waitForExit(ctx, client, name, inst.GetId())
		exited <- result{code, err}
	}()

	stopping := false
	for {
		select {
		case r := <-exited:
			if r.err != nil {
				return r.err
			}
			drainConsole(cmd, console)
			if r.code != 0 {
				// The workload's own output explains its status.
				return &exitError{code: r.code}
			}
			return nil
		case <-interrupts:
			if stopping {
				cmd.PrintErrf("Instance %s is left to stop on its own\n", name)
				return &exitError{code: statusInterrupted}
			}
			stopping = true
			cmd.PrintErrf("Stopping %s; press Ctrl+C again to stop waiting for it\n", name)
			go func() {
				_, _ = client.StopInstance(ctx, &dicerdv1.StopInstanceRequest{Name: name})
			}()
		}
	}
}

// drainConsole waits a while for the console's last lines, which are read as
// the instance stops, and reports a console that could not be read.
func drainConsole(cmd *cobra.Command, console <-chan error) {
	select {
	case err := <-console:
		if err != nil {
			cmd.PrintErrf("Error: cannot read the console: %s\n", errorMessage(err))
		}
	case <-time.After(consoleDrainTimeout):
	}
}

// followConsole writes an instance's console to w from its first line until
// the instance stops. The console appears only as the instance starts, so
// until started is closed one not found is looked for again; after, it means
// the instance is gone, with nothing left to read.
func followConsole(ctx context.Context, client *dicer.Client, name string, started <-chan struct{}, w io.Writer) error {
	w = &gatedWriter{ctx: ctx, open: started, w: w}

	for {
		wasStarted := isClosed(started)

		err := streamLogs(ctx, client, &dicerdv1.GetInstanceLogsRequest{Name: name, Follow: true}, w)
		switch {
		case status.Code(err) != codes.NotFound:
			return err
		case wasStarted:
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(consolePollInterval):
		}
	}
}

// gatedWriter holds writes back until open is closed, so that the console
// does not write over the spinner that shows the instance starting.
type gatedWriter struct {
	ctx  context.Context
	open <-chan struct{}
	w    io.Writer
}

func (g *gatedWriter) Write(p []byte) (int, error) {
	select {
	case <-g.open:
	case <-g.ctx.Done():
		return 0, g.ctx.Err()
	}

	return g.w.Write(p)
}

// isClosed reports whether ch is closed.
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
