// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"

	"github.com/spf13/cobra"

	"github.com/dicer-sh/dicer"
)

// statusUnknown is what wait exits with for a guest that ended without
// reporting a status: its VMM was lost, or it never started. It is what a
// shell reports for a command it could not run, and says plainly that the
// number is not the workload's own.
const statusUnknown = 125

func newInstanceWaitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait NAME",
		Short: "Wait until an instance stops, and exit with its status",
		Long: "Waits until an instance stops, prints the status its guest ended with and\n" +
			"exits with it: the workload's exit code, or 0 for a guest that powered\n" +
			"itself off.\n\n" +
			"An instance that has already stopped is not waited for; its last status is\n" +
			"reported at once. An instance its restart policy starts again has not\n" +
			"stopped, so the wait goes on.",
		Example: "  dicer wait web\n" +
			"  dicer wait --timeout 30s web\n" +
			"  dicer start job && dicer wait job",
		Args:              one("an instance name"),
		ValidArgsFunction: complete(1, instancesIn()),
		RunE:              runInstanceWaitCommand,
	}

	cmd.Flags().Duration("timeout", 0, "Give up after this long (0: wait indefinitely)")

	return cmd
}

func runInstanceWaitCommand(cmd *cobra.Command, args []string) error {
	name := args[0]

	client, cleanup, err := newClient(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := cmd.Context()
	if timeout, _ := cmd.Flags().GetDuration("timeout"); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	status, err := waitForExit(ctx, client, name)
	if err != nil {
		return suggest(cmd.Context(), client, instancesIn(), name, err)
	}

	cmd.Println(status)
	if status != 0 {
		// The status is the answer, not a failure of the command, so it is
		// passed through without a message of ours.
		return &exitError{code: status}
	}

	return nil
}

// waitForExit returns the status an instance's guest ended with, waiting for
// it to stop if it has not.
//
// The events stream is what it waits on, so a stop is reported as it happens
// rather than found by asking over and over. The subscription is opened
// before the instance is read, because an instance that stopped in between
// would otherwise be waited for forever.
func waitForExit(ctx context.Context, client *dicer.Client, name string) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := &waiter{stopped: make(chan struct{}, 1), caughtUp: make(chan struct{}), failed: make(chan error, 1)}
	go func() {
		w.failed <- client.Events(ctx, dicer.EventOptions{
			Filter: dicer.EventFilter{Kind: dicer.KindInstance, Names: []string{name}},
			Follow: true,
		}, w.observe, sync.OnceFunc(func() { close(w.caughtUp) }))
	}()

	// Wait until the stream is reporting what happens next before reading
	// the instance, so that a stop between the two cannot be missed.
	select {
	case <-w.caughtUp:
	case err := <-w.failed:
		return 0, streamEnded(ctx, err)
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	for {
		switch status, stopped, err := w.status(ctx, client, name); {
		case err != nil:
			return 0, err
		case stopped:
			return status, nil
		}

		select {
		case <-w.stopped:
		case err := <-w.failed:
			return 0, streamEnded(ctx, err)
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// waiter watches one instance's events for the one thing wait cares about:
// that it is no longer running, and what it ended with.
type waiter struct {
	stopped  chan struct{}
	caughtUp chan struct{}
	failed   chan error

	// mu guards what the stream reports against the goroutine reading it.
	mu sync.Mutex

	// lastExit is the status of the last end an event reported, kept
	// because an instance deleted as it stopped -- by --rm -- cannot be
	// asked for its status afterwards.
	lastExit *int

	// seen records that the instance was there to be read. Until it has
	// been, an instance that is not found never existed; afterwards, one
	// that is not found was deleted as it stopped.
	seen bool
}

// observe records one of the instance's events.
func (w *waiter) observe(e dicer.Event) {
	switch e.Action {
	case dicer.ActionExited, dicer.ActionDied, dicer.ActionStopped, dicer.ActionDeleted:
	default:
		// A start, a restart, a health verdict: the instance is running, or
		// is about to be.
		return
	}

	if code, err := strconv.Atoi(e.Attributes["exit_code"]); err == nil {
		w.mu.Lock()
		w.lastExit = &code
		w.mu.Unlock()
	}

	select {
	case w.stopped <- struct{}{}:
	default: // one wake-up is enough; the instance is read either way
	}
}

// status returns what the instance ended with, and whether it has ended.
func (w *waiter) status(ctx context.Context, client *dicer.Client, name string) (int, bool, error) {
	inst, err := client.GetInstance(ctx, name)
	if errors.Is(err, dicer.ErrNotFound) && w.seen {
		// Deleted as it stopped, which --rm asks for. It stopped, which is
		// what was waited for, and what it ended with is what its last
		// event said.
		return w.reported(), true, nil
	}
	if err != nil {
		return 0, false, err
	}
	w.seen = true

	switch inst.Status.State {
	case dicer.StateStopped, dicer.StateFailed:
	default:
		return 0, false, nil
	}

	switch {
	case inst.Status.ExitCode != nil:
		return *inst.Status.ExitCode, true, nil
	case inst.Status.State == dicer.StateFailed:
		return statusUnknown, true, nil
	default:
		return 0, true, nil
	}
}

// reported is the status the last end reported, or the unknown status if no
// end reported one.
func (w *waiter) reported() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.lastExit == nil {
		return 0
	}

	return *w.lastExit
}

// streamEnded says what a finished event stream means for a wait: a stream
// that ends on its own has stopped reporting what the wait is waiting for,
// which is a failure however quietly it happened.
func streamEnded(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err == nil, errors.Is(err, io.EOF):
		return errors.New("the daemon stopped reporting events before the instance stopped")
	default:
		return err
	}
}
