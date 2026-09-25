// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/konradasb/dicer"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// statusUnknown is the exit status of a guest that ended without reporting
// one.
const statusUnknown = 125

func newInstanceWaitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait NAME...",
		Short: "Wait until one or more instances stop, and print their exit codes",
		Long: "Waits until each instance stops and prints the status its guest ended with,\n" +
			"a line for each: the workload's exit code, 0 for a guest that powered\n" +
			"itself off, or 125 for one that ended without saying how.\n\n" +
			"An instance that has already stopped is not waited for; its last status is\n" +
			"reported at once, even if --rm has deleted it since. An instance its\n" +
			"restart policy starts again has not stopped, so the wait goes on.\n\n" +
			"As with docker wait, the statuses are printed, not exited with: the command\n" +
			"fails only for an instance it cannot wait for.",
		Example: "  dicer wait web\n" +
			"  dicer wait job1 job2\n" +
			"  dicer wait --timeout 30s web\n" +
			"  status=$(dicer wait job)",
		Args:              oneOrMore("instance name"),
		ValidArgsFunction: complete(0, instancesIn()),
		RunE:              runInstanceWaitCommand,
	}

	cmd.Flags().Duration("timeout", 0, "Give up after this long (0: wait indefinitely)")

	return cmd
}

func runInstanceWaitCommand(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if timeout, _ := cmd.Flags().GetDuration("timeout"); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	return eachName(cmd, args, instancesIn(), func(client *dicer.Client, name string) error {
		code, err := waitForExit(ctx, client, name, "")
		if err != nil {
			return err
		}

		_, err = fmt.Fprintln(cmd.OutOrStdout(), code)
		return err
	})
}

// waitForExit waits on the events stream for an instance to stop and returns
// its exit status. id, if not empty, is the instance to wait for, which tells
// it apart from a later one given the same name; empty is whichever the name
// picks. The history the stream starts with says how an instance ended even
// once it is deleted.
func waitForExit(ctx context.Context, client *dicer.Client, name, id string) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	w := &waiter{
		id:       id,
		ends:     make(map[string]*ending),
		stopped:  make(chan struct{}, 1),
		caughtUp: make(chan struct{}),
		failed:   make(chan error, 1),
	}
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
		switch code, stopped, err := w.status(ctx, client, name); {
		case err != nil:
			return 0, err
		case stopped:
			return code, nil
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

// waiter watches the events of the instances that had a name for the one
// thing wait cares about: that the one waited for is no longer running, and
// what it ended with.
type waiter struct {
	stopped  chan struct{}
	caughtUp chan struct{}
	failed   chan error

	// id is the instance waited for, once known.
	id string

	// mu guards what the stream reports against the goroutine reading it.
	mu sync.Mutex

	// ends is how each instance last ended, by ID, as its events told.
	ends map[string]*ending

	// latest is the instance the latest event was about: for a name no
	// instance has now, the last one that had it.
	latest string
}

// ending is how an instance ended, as its events told.
type ending struct {
	// code is the exit code reported, if one was.
	code *int

	// died is an end that reported no code of its own.
	died bool

	// deleted is set once the instance is deleted.
	deleted bool
}

// status is the exit status the ending stands for.
func (e *ending) status() int {
	switch {
	case e.code != nil:
		return *e.code
	case e.died:
		return statusUnknown
	default:
		return 0
	}
}

// observe records one of the instances' events.
func (w *waiter) observe(e *dicerdv1.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	id := e.GetId()
	w.latest = id

	switch e.GetAction() {
	case dicerdv1.EventAction_EVENT_ACTION_EXITED, dicerdv1.EventAction_EVENT_ACTION_DIED,
		dicerdv1.EventAction_EVENT_ACTION_STOPPED:
		end := &ending{died: e.GetAction() == dicerdv1.EventAction_EVENT_ACTION_DIED}
		if code, err := strconv.Atoi(e.GetAttributes()["exit_code"]); err == nil {
			end.code = &code
		}
		w.ends[id] = end
	case dicerdv1.EventAction_EVENT_ACTION_DELETED:
		if w.ends[id] == nil {
			w.ends[id] = &ending{}
		}
		w.ends[id].deleted = true
	default:
		// A start, a restart, a health verdict: the instance is running, or
		// is about to be, and whatever it ended with before is past.
		delete(w.ends, id)
		return
	}

	select {
	case w.stopped <- struct{}{}:
	default: // one wake-up is enough; the instance is read either way
	}
}

// status returns what the instance ended with, and whether it has ended.
func (w *waiter) status(ctx context.Context, client *dicer.Client, name string) (int, bool, error) {
	inst, err := client.GetInstance(ctx, &dicerdv1.GetInstanceRequest{Name: name})
	switch {
	case err == nil && (w.id == "" || inst.GetId() == w.id):
		w.id = inst.GetId()
		return endedWith(inst)
	case err == nil, status.Code(err) == codes.NotFound:
		// Gone, or the name another instance's now.
		if code, ok := w.deleted(); ok {
			return code, true, nil
		}
		if w.id != "" {
			// Deleted, and the event saying so is on its way.
			return 0, false, nil
		}
		return 0, false, err
	default:
		return 0, false, err
	}
}

// deleted returns the status the instance waited for ended with, if its
// events say it is deleted. Before the instance is known, it is the one that
// had the name last.
func (w *waiter) deleted() (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	end := w.ends[cmp.Or(w.id, w.latest)]
	if end == nil || !end.deleted {
		return 0, false
	}

	return end.status(), true
}

// endedWith returns the status a stopped instance ended with, and whether it
// has stopped.
func endedWith(inst *dicerdv1.Instance) (int, bool, error) {
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
