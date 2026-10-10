// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package instance

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
)

// silentAgent makes h's guests' agents never answer, as a guest that does
// not boot.
func silentAgent(h *harness) {
	h.manager.awaitAgent = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
}

// startInBackground starts h's instance as Start does, and returns what it
// returns once it does.
func startInBackground(t *testing.T, h *harness) <-chan error {
	t.Helper()
	h.define()
	started := make(chan error, 1)
	go func() { started <- h.manager.Start(t.Context(), h.instance.Name) }()
	return started
}

// writeConsole appends lines to h's instance's console log, as its guest
// would.
func writeConsole(t *testing.T, h *harness, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(h.manager.serialLogPath(h.instance), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestStartWaitsForTheGuestToBoot(t *testing.T) {
	h := newHarness(t)
	answer := make(chan struct{})
	h.manager.awaitAgent = func(ctx context.Context, _ string) error {
		select {
		case <-answer:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	started := startInBackground(t, h)
	select {
	case err := <-started:
		t.Fatalf("Start returned %v before the guest's agent answered", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(answer)
	if err := <-started; err != nil {
		t.Fatalf("Start = %v once the agent answered, want nil", err)
	}
	if status := h.status(t); status.State != StateRunning {
		t.Errorf("state = %s, want Running", status.State)
	}
}

func TestGuestWhoseAgentNeverAnswersFails(t *testing.T) {
	h := newHarness(t)
	silentAgent(h)
	h.manager.bootTimeout = 500 * time.Millisecond

	started := startInBackground(t, h)
	h.waitForVMMs(t, 1)
	writeConsole(t, h, "[    0.000000] Booting Linux on physical CPU 0x0", "[    3.638020] Run /init as init process")

	err := <-started
	if !errors.Is(err, errdefs.ErrInvalidState) || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("Start = %v, want the boot reported failed", err)
	}
	if !strings.Contains(err.Error(), "Run /init as init process") || !strings.Contains(err.Error(), "dicer logs") {
		t.Errorf("Start = %v, want the console's last line and where to read more", err)
	}

	status := h.waitForState(t, StateFailed)
	if !strings.Contains(status.StateError, "the guest did not boot") {
		t.Errorf("state error = %q, want the boot named as the failure", status.StateError)
	}
	if h.manager.vmm(h.instance.ID) != nil {
		t.Error("the VMM of a guest that did not boot is still registered")
	}
	select {
	case <-h.starter.vmm().Done():
	case <-time.After(5 * time.Second):
		t.Error("the VMM of a guest that did not boot is still running")
	}
}

// TestKernelPanicFailsTheBootAtOnce checks that a guest whose kernel panics
// fails at once, rather than after the boot timeout: Cloud Hypervisor would
// reboot it over and over in a VMM that never ends.
func TestKernelPanicFailsTheBootAtOnce(t *testing.T) {
	h := newHarness(t)
	silentAgent(h)
	h.manager.bootTimeout = time.Hour

	started := startInBackground(t, h)
	h.waitForVMMs(t, 1)
	writeConsole(t, h,
		"[    3.614390] Warning: unable to open an initial console.",
		"[    3.659149] Kernel panic - not syncing: Attempted to kill init! exitcode=0x00000200",
		"[    3.711337] Rebooting in 1 seconds..")

	select {
	case err := <-started:
		want := "its kernel panicked: Kernel panic - not syncing: Attempted to kill init! exitcode=0x00000200"
		if !errors.Is(err, errdefs.ErrInvalidState) || !strings.Contains(err.Error(), want) {
			t.Errorf("Start = %v, want the panic named", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after the guest's kernel panicked")
	}

	status := h.waitForState(t, StateFailed)
	if !strings.Contains(status.StateError, "Kernel panic - not syncing") {
		t.Errorf("state error = %q, want the panic", status.StateError)
	}
}

func TestBootFailureFollowsTheRestartPolicy(t *testing.T) {
	h := newHarness(t)
	silentAgent(h)
	h.manager.bootTimeout = 50 * time.Millisecond
	h.setRestart(t, RestartPolicy{Mode: RestartModeOnFailure, MaxRetries: 1})
	h.restartAtOnce()

	if err := <-startInBackground(t, h); err == nil {
		t.Fatal("Start succeeded for a guest that did not boot")
	}

	// The restart does not boot either, and the policy gives up.
	status := h.waitForState(t, StateFailed)
	if !strings.Contains(status.StateError, "gave up after 1 restart") ||
		!strings.Contains(status.StateError, "the guest did not boot") {
		t.Errorf("state error = %q, want the policy to give up on a guest that does not boot", status.StateError)
	}
}

func TestStopWhileBootingIsNoFailure(t *testing.T) {
	h := newHarness(t)
	silentAgent(h)

	started := startInBackground(t, h)
	h.waitForVMMs(t, 1)
	h.waitForState(t, StateRunning)

	if err := h.manager.Stop(t.Context(), h.instance.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-started; err != nil {
		t.Errorf("Start = %v for a guest stopped as it booted, want nil", err)
	}
	if status := h.status(t); status.State != StateStopped || status.StateError != "" {
		t.Errorf("status = %s %q, want Stopped with no error", status.State, status.StateError)
	}
}

func TestWorkloadEndingCleanlyAsItBootsHasStarted(t *testing.T) {
	h := newHarness(t)
	silentAgent(h)

	started := startInBackground(t, h)
	h.waitForVMMs(t, 1)
	h.waitForState(t, StateRunning)
	h.exit(t, 0)

	if err := <-started; err != nil {
		t.Errorf("Start = %v for a workload that ended cleanly, want nil", err)
	}
}

func TestWorkloadFailingAsItBootsFailsTheStart(t *testing.T) {
	h := newHarness(t)
	silentAgent(h)

	started := startInBackground(t, h)
	h.waitForVMMs(t, 1)
	h.waitForState(t, StateRunning)
	h.exit(t, 3)

	if err := <-started; !errors.Is(err, errdefs.ErrInvalidState) || !strings.Contains(err.Error(), "exit code 3") {
		t.Errorf("Start = %v, want the workload's failure", err)
	}
}

func TestBootConsole(t *testing.T) {
	path := t.TempDir() + "/serial.log"
	write := func(s string, flag int) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|flag, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}

	// What the log held before the boot is not this boot's.
	write("[    1.000000] Kernel panic - not syncing: an earlier boot\n", os.O_APPEND)
	c := newBootConsole(path)
	c.read()
	if c.panicLine() != "" || c.lastLine() != "" {
		t.Fatalf("read the log from before the boot: panic %q, last %q", c.panicLine(), c.lastLine())
	}

	// A line is taken whole, without the kernel's timestamp, once it ends.
	write("[    2.500000] Run /init as", os.O_APPEND)
	c.read()
	if c.lastLine() != "" {
		t.Errorf("took a line before it ended: %q", c.lastLine())
	}
	write(" init process\n[    2.600000] Kernel panic - not syncing: Attempted to kill init!\r\n", os.O_APPEND)
	c.read()
	if got, want := c.lastLines(), []string{"Run /init as init process", "Kernel panic - not syncing: Attempted to kill init!"}; !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
	if got := c.panicLine(); got != "Kernel panic - not syncing: Attempted to kill init!" {
		t.Errorf("panic = %q", got)
	}

	// A log started again is read from its start, and only the last lines
	// are kept.
	var many strings.Builder
	for i := range consoleLines + 5 {
		many.WriteString("line " + string(rune('a'+i)) + "\n")
	}
	write(many.String(), os.O_TRUNC)
	c.read()
	if got := c.lastLines(); len(got) != consoleLines || got[len(got)-1] != "line "+string(rune('a'+consoleLines+4)) {
		t.Errorf("lines after the log started again = %q, want its last %d", got, consoleLines)
	}
}
