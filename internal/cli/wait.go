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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// statusUnknown is wait's exit status for a guest that ended without
// reporting one.
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

// waitForExit waits on the events stream for an instance to stop and returns
// its exit status. It subscribes before reading the instance so no stop is
// missed.
func waitForExit(ctx context.Context, client *dicer.Client, name string) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := &waiter{stopped: make(chan struct{}, 1), caughtUp: make(chan struct{}), failed: make(chan error, 1)}
	go func() {
		w.failed <- streamEvents(ctx, client, &dicerdv1.GetEventsRequest{
			Kind:   dicerdv1.EventKind_EVENT_KIND_INSTANCE,
			Name:   name,
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

	// lastExit is the last exit status an event reported, for an instance
	// removed on exit.
	lastExit *int

	// seen records that the instance was found, so a later not-found means
	// it was removed on exit.
	seen bool
}

// observe records one of the instance's events.
func (w *waiter) observe(e *dicerdv1.Event) {
	switch e.GetAction() {
	case dicerdv1.EventAction_EVENT_ACTION_EXITED, dicerdv1.EventAction_EVENT_ACTION_DIED,
		dicerdv1.EventAction_EVENT_ACTION_STOPPED, dicerdv1.EventAction_EVENT_ACTION_DELETED:
	default:
		// A start, a restart, a health verdict: the instance is running, or
		// is about to be.
		return
	}

	if code, err := strconv.Atoi(e.GetAttributes()["exit_code"]); err == nil {
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
	inst, err := client.GetInstance(ctx, &dicerdv1.GetInstanceRequest{Name: name})
	if status.Code(err) == codes.NotFound && w.seen {
		// Removed on exit: report its last event's status.
		return w.reported(), true, nil
	}
	if err != nil {
		return 0, false, err
	}
	w.seen = true

	switch inst.GetState() {
	case stateStopped, stateFailed:
	default:
		return 0, false, nil
	}

	switch {
	case inst.ExitCode != nil:
		return int(inst.GetExitCode()), true, nil
	case inst.GetState() == stateFailed:
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

// streamEnded returns the error for an event stream that ended during a
// wait.
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
