// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package instance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"google.golang.org/grpc/connectivity"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/process"
)

// A guest has booted once its agent answers. Until then its VMM running says
// little: Cloud Hypervisor reboots a guest that resets in place, so a guest
// whose kernel panics, or whose init fails before it can tell the host, can
// reboot over and over in a VMM that never exits. Each boot is therefore
// watched, and a guest that panics, or whose agent does not answer within
// bootTimeout, is ended as a failure, with what its console last said.

const (
	// defaultBootTimeout is how long a guest has to boot: for its agent to
	// answer after its VMM starts. Booting usually takes a second, and a
	// full systemd boot seconds more.
	defaultBootTimeout = time.Minute

	// consolePollInterval is how often a booting guest's console is read
	// for a kernel panic.
	consolePollInterval = 200 * time.Millisecond

	// endedSettleTimeout bounds how long a boot whose VMM ended waits for
	// that end to be recorded.
	endedSettleTimeout = 10 * time.Second
)

// kernelPanic is how a Linux kernel begins its panic message.
const kernelPanic = "Kernel panic - not syncing"

// bootWatch is the outcome of watching one boot.
type bootWatch struct {
	done chan struct{}
	// err is why the guest did not boot, or nil once it booted or ended
	// cleanly. It is set before done is closed.
	err error
}

// wait waits until the boot's outcome is known, or ctx ends, and returns
// why the guest did not boot, or nil. A nil bootWatch, for a guest resumed
// rather than booted, has nothing to wait for.
func (w *bootWatch) wait(ctx context.Context) error {
	if w == nil {
		return nil
	}
	select {
	case <-w.done:
		return w.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// watchBoot watches the boot of an instance whose VMM has just started, in
// the background, ending the guest if it does not boot. console is its
// console log, read from where it stood before the VMM started.
func (m *Manager) watchBoot(ctx context.Context, instance Spec, vmm *process.Process, vsockPath string, console *bootConsole) *bootWatch {
	ctx = context.WithoutCancel(ctx)
	w := &bootWatch{done: make(chan struct{})}
	m.watchers.Go(func() {
		defer close(w.done)
		w.err = m.awaitBoot(ctx, instance, vmm, vsockPath, console)
	})
	return w
}

// awaitBoot waits for a guest to boot, and returns why it did not, or nil.
func (m *Manager) awaitBoot(ctx context.Context, instance Spec, vmm *process.Process, vsockPath string, console *bootConsole) error {
	agentCtx, cancel := context.WithTimeout(ctx, m.bootTimeout)
	defer cancel()
	answered := make(chan error, 1)
	go func() { answered <- m.awaitAgent(agentCtx, vsockPath) }()

	ticker := time.NewTicker(consolePollInterval)
	defer ticker.Stop()

	for {
		select {
		case err := <-answered:
			if err == nil {
				return nil
			}
			console.read()
			cause := fmt.Errorf("its agent did not answer within %s", humanize.Duration(m.bootTimeout))
			if !errors.Is(err, context.DeadlineExceeded) {
				cause = fmt.Errorf("its agent cannot be reached: %w", err)
			}
			return m.failBoot(ctx, instance, vmm, console, cause)
		case <-vmm.Done():
			return m.endedWhileBooting(ctx, instance, vmm)
		case <-m.closing:
			return nil
		case <-ticker.C:
			console.read()
			if line := console.panicLine(); line != "" {
				return m.failBoot(ctx, instance, vmm, console, errors.New("its kernel panicked: "+line))
			}
		}
	}
}

// failBoot ends a guest that did not boot because of cause, as an unexpected
// end its restart policy then handles, and returns why, naming where to read
// more. A guest that was stopped or replaced meanwhile is left alone.
func (m *Manager) failBoot(ctx context.Context, instance Spec, vmm *process.Process, console *bootConsole, cause error) error {
	lock := m.lock(instance.ID)
	lock.Lock()
	defer lock.Unlock()

	if m.vmm(instance.ID) != vmm {
		return nil
	}
	// Forgotten first, so that the watcher of the VMM ignores its end.
	m.forget(instance.ID)
	vmm.Terminate()

	if current, err := m.store.Instance(instance.ID); err == nil {
		instance = current
	}
	status, err := m.statusOf(instance)
	if err != nil {
		m.logger.WarnContext(ctx, "cannot read instance status", "instance", instance.Name, "error", err)
	}

	if line := console.lastLine(); line != "" && !strings.Contains(cause.Error(), line) {
		cause = fmt.Errorf("%w; its console last said %q", cause, line)
	}
	m.logger.WarnContext(ctx, "the guest did not boot", "instance", instance.Name, "error", cause,
		"console", console.lastLines())
	m.ended(ctx, instance, status, Exit{Failure: fmt.Errorf("the guest did not boot: %w", cause)})

	return errdefs.InvalidState("instance %q did not boot: %v; see 'dicer logs %s'", instance.Name, cause, instance.Name)
}

// endedWhileBooting waits for the end of a VMM that exited while its guest
// booted to be recorded, and returns it as a failure if it was one. A
// workload that ran and exited cleanly is not one.
func (m *Manager) endedWhileBooting(ctx context.Context, instance Spec, vmm *process.Process) error {
	deadline := time.Now().Add(endedSettleTimeout)
	for {
		lock := m.lock(instance.ID)
		lock.Lock()
		handled := m.vmm(instance.ID) != vmm
		status, err := m.statusOf(instance)
		lock.Unlock()

		switch {
		case err != nil:
			return err
		case !handled && time.Now().Before(deadline):
			select {
			case <-time.After(10 * time.Millisecond):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		case status.State == StateFailed || status.State == StateRestarting:
			return errdefs.InvalidState("instance %q ended while booting: %s; see 'dicer logs %s'",
				instance.Name, status.StateError, instance.Name)
		default:
			return nil
		}
	}
}

// awaitAgent waits until the guest agent behind vsockPath answers, or ctx
// ends.
func awaitAgent(ctx context.Context, vsockPath string) error {
	conn, err := dialAgent(vsockPath)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	for {
		state := conn.GetState()
		if state == connectivity.Ready {
			return nil
		}
		conn.Connect()

		// A connection that keeps failing may not change state, so it is
		// tried again at least once a second.
		changeCtx, cancel := context.WithTimeout(ctx, time.Second)
		conn.WaitForStateChange(changeCtx, state)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// consoleLines is how many of a booting guest's last console lines are
// kept, for the log of one that does not boot.
const consoleLines = 10

// consoleTimestamp is the time the kernel puts before each line it logs.
var consoleTimestamp = regexp.MustCompile(`^\[\s*\d+\.\d+\]\s*`)

// bootConsole reads what a booting guest writes to its console, from where
// the log stood before its VMM started. It is used by one goroutine.
type bootConsole struct {
	path   string
	offset int64

	partial []byte
	last    []string
	panic   string
}

// newBootConsole returns a reader of the console log at path, from its end.
func newBootConsole(path string) *bootConsole {
	c := &bootConsole{path: path}
	if info, err := os.Stat(path); err == nil {
		c.offset = info.Size()
	}
	return c
}

// read takes in what the console has written since the last read. A log
// that has shrunk, because it was started again, is read from its start.
func (c *bootConsole) read() {
	f, err := os.Open(c.path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	if info, err := f.Stat(); err == nil && info.Size() < c.offset {
		c.offset, c.partial = 0, nil
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return
	}
	c.offset += int64(len(data))

	data = append(c.partial, data...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		c.partial = data
		return
	}
	c.partial = append([]byte(nil), data[end+1:]...)

	for raw := range strings.SplitSeq(string(data[:end]), "\n") {
		line := strings.TrimSpace(consoleTimestamp.ReplaceAllString(strings.TrimRight(raw, "\r"), ""))
		if line == "" {
			continue
		}
		if c.panic == "" && strings.Contains(line, kernelPanic) {
			c.panic = line
		}
		c.last = append(c.last, line)
		if len(c.last) > consoleLines {
			c.last = c.last[len(c.last)-consoleLines:]
		}
	}
}

// panicLine returns the line in which the guest's kernel panicked, or "".
func (c *bootConsole) panicLine() string { return c.panic }

// lastLine returns the last line the console wrote, or "".
func (c *bootConsole) lastLine() string {
	if len(c.last) == 0 {
		return ""
	}
	return c.last[len(c.last)-1]
}

// lastLines returns the last lines the console wrote, oldest first.
func (c *bootConsole) lastLines() []string { return c.last }
