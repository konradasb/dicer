// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package doctor

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/hypervisor"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/instance"
)

// fakeInstances is an instance manager whose test instances do what run says
// once started.
type fakeInstances struct {
	// run returns what a started instance's console shows, and the exit code
	// it stops with, or false if it never stops.
	run func(spec instance.Spec) (console string, exitCode int, stops bool)

	// createErr, if set, is what Create returns.
	createErr error

	mu      sync.Mutex
	specs   map[string]instance.Spec // by ID
	console map[string]string
	stopped map[string]chan instance.Status
}

func newFakeInstances(run func(instance.Spec) (string, int, bool)) *fakeInstances {
	return &fakeInstances{
		run:     run,
		specs:   make(map[string]instance.Spec),
		console: make(map[string]string),
		stopped: make(map[string]chan instance.Status),
	}
}

// instancesRun returns a run that has every instance print console and exit
// with exitCode.
func instancesRun(console string, exitCode int) func(instance.Spec) (string, int, bool) {
	return func(instance.Spec) (string, int, bool) { return console, exitCode, true }
}

func (f *fakeInstances) Create(_ context.Context, spec instance.Spec, _ image.PullPolicy) error {
	if f.createErr != nil {
		return f.createErr
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs[spec.ID] = spec
	f.stopped[spec.ID] = make(chan instance.Status, 1)
	return nil
}

func (f *fakeInstances) Waiter(id string, _ instance.WaitOptions) (waiter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stopped, ok := f.stopped[id]
	if !ok {
		return nil, errdefs.NotFound("instance %q not found", id)
	}
	return fakeWaiter(stopped), nil
}

func (f *fakeInstances) Start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	console, exitCode, stops := f.run(f.specs[id])
	f.console[id] = console
	if stops {
		f.stopped[id] <- instance.Status{State: instance.StateStopped, ExitCode: &exitCode}
	}
	return nil
}

func (f *fakeInstances) StreamLogs(_ context.Context, id string, _ instance.LogOptions, w io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := io.WriteString(w, f.console[id])
	return err
}

func (f *fakeInstances) Delete(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.specs, id)
	return nil
}

// left returns how many instances were not deleted.
func (f *fakeInstances) left() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.specs)
}

// fakeWaiter waits for a fake instance's stop.
type fakeWaiter chan instance.Status

func (w fakeWaiter) Wait(ctx context.Context) (instance.Status, error) {
	select {
	case status := <-w:
		return status, nil
	case <-ctx.Done():
		return instance.Status{}, ctx.Err()
	}
}

func (w fakeWaiter) Close() {}

// testInstanceDoctor returns a Doctor of a healthy host, whose test
// instance f runs.
func testInstanceDoctor(t *testing.T, f *fakeInstances) *Doctor {
	t.Helper()
	d := healthyHost().doctor(t, Config{}, false)
	d.instances = f
	return d
}

// testInstanceResults checks the host with a test instance, as opts says,
// and returns the test instance's results.
func testInstanceResults(t *testing.T, d *Doctor, opts Options) []Result {
	t.Helper()
	opts.TestInstance = true
	var results []Result
	for r := range d.Check(t.Context(), opts) {
		if r.Group == GroupInstances {
			results = append(results, r)
		}
	}
	return results
}

// names returns the names of results.
func names(results []Result) []string {
	var out []string
	for _, r := range results {
		out = append(out, r.Name)
	}
	return out
}

func TestATestInstanceBootsOnTheDefaultHypervisor(t *testing.T) {
	f := newFakeInstances(instancesRun("Linux booting\ndicer-doctor: booted\ndicer-doctor: online\n", 0))
	results := testInstanceResults(t, testInstanceDoctor(t, f), Options{})

	if got, want := names(results), []string{"cloud-hypervisor", "internet"}; !slices.Equal(got, want) {
		t.Fatalf("test instance results = %v, want %v", got, want)
	}
	for _, r := range results {
		if r.Status != StatusOK || r.Hint != "" || len(r.Console) != 0 {
			t.Errorf("%s = %s %q (hint %q, console %q), want ok", r.Name, r.Status, r.Detail, r.Hint, r.Console)
		}
	}
	if !strings.HasPrefix(results[0].Detail, "booted, ran a command and stopped") {
		t.Errorf("detail = %q, want it to say the instance booted", results[0].Detail)
	}
	if n := f.left(); n != 0 {
		t.Errorf("%d test instances were left behind", n)
	}
}

func TestATestInstanceBootsOnTheHypervisorNamed(t *testing.T) {
	var booted instance.Spec
	f := newFakeInstances(func(spec instance.Spec) (string, int, bool) {
		booted = spec
		return "dicer-doctor: booted\ndicer-doctor: online\n", 0, true
	})
	results := testInstanceResults(t, testInstanceDoctor(t, f), Options{
		TestInstanceHypervisor: hypervisor.TypeFirecracker, TestInstanceHypervisorVersion: "v1.17.0",
	})

	if booted.HypervisorType != hypervisor.TypeFirecracker || booted.HypervisorVersion != "v1.17.0" ||
		results[0].Name != "firecracker" {
		t.Errorf("booted on %s %s, reported as %q; want firecracker v1.17.0",
			booted.HypervisorType, booted.HypervisorVersion, results[0].Name)
	}
}

func TestCheckComesInGroupsHostFirst(t *testing.T) {
	f := newFakeInstances(instancesRun("dicer-doctor: booted\ndicer-doctor: online\n", 0))
	d := testInstanceDoctor(t, f)

	var groups []Group
	for r := range d.Check(t.Context(), Options{TestInstance: true}) {
		if len(groups) == 0 || groups[len(groups)-1] != r.Group {
			groups = append(groups, r.Group)
		}
	}
	if want := []Group{GroupHost, GroupInstances}; !slices.Equal(groups, want) {
		t.Errorf("groups = %v, want %v", groups, want)
	}

	for r := range d.Check(t.Context(), Options{}) {
		if r.Group != GroupHost {
			t.Errorf("without test instances, %s is in group %s", r.Name, r.Group)
		}
	}
}

func TestATestInstanceThatDoesNotBoot(t *testing.T) {
	f := newFakeInstances(instancesRun(
		"[    3.659149] Kernel panic - not syncing: Attempted to kill init! exitcode=0x00000200\n", 1))
	results := testInstanceResults(t, testInstanceDoctor(t, f), Options{})

	if got, want := names(results), []string{"cloud-hypervisor"}; !slices.Equal(got, want) {
		t.Fatalf("test instance results = %v, want %v, and no internet check from an instance that did not boot", got, want)
	}
	r := results[0]
	if r.Status != StatusFailed || !strings.Contains(r.Detail, "ended without running its command: exit code 1") {
		t.Errorf("result = %s %q, want failed for not running its command", r.Status, r.Detail)
	}
	if len(r.Console) == 0 || !strings.Contains(r.Console[len(r.Console)-1], "Kernel panic") {
		t.Errorf("console = %q, want the panic", r.Console)
	}
}

func TestATestInstanceWhoseCommandFails(t *testing.T) {
	f := newFakeInstances(instancesRun("dicer-doctor: booted\nsh: wget: not found\n", 127))
	r := testInstanceResults(t, testInstanceDoctor(t, f), Options{})[0]

	if r.Status != StatusFailed || r.Detail != "ran its command, then ended: exit code 127" {
		t.Errorf("result = %s %q, want failed with the exit code", r.Status, r.Detail)
	}
	if !slices.Equal(r.Console, []string{"sh: wget: not found"}) {
		t.Errorf("console = %q, want the instance's own lines only", r.Console)
	}
}

func TestATestInstanceOfflineIsAWarning(t *testing.T) {
	f := newFakeInstances(instancesRun("dicer-doctor: booted\ndicer-doctor: offline\n", 0))
	results := testInstanceResults(t, testInstanceDoctor(t, f), Options{})

	internet := results[len(results)-1]
	if internet.Name != "internet" || internet.Status != StatusWarning || !strings.Contains(internet.Detail, testInstanceOnlineURL) {
		t.Errorf("last result = %s %s %q, want an internet warning", internet.Name, internet.Status, internet.Detail)
	}
}

func TestATestInstanceThatNeverEndsTimesOut(t *testing.T) {
	f := newFakeInstances(func(instance.Spec) (string, int, bool) { return "", 0, false })
	d := testInstanceDoctor(t, f)

	r := testInstanceResults(t, d, Options{TestInstanceTimeout: 50 * time.Millisecond})[0]
	if r.Status != StatusFailed || !strings.Contains(r.Detail, "did not run its command within") {
		t.Errorf("result = %s %q, want failed for the timeout", r.Status, r.Detail)
	}
	if n := f.left(); n != 0 {
		t.Errorf("%d test instances were left behind", n)
	}
}

func TestAFailedTestInstanceIsKeptWhenAsked(t *testing.T) {
	f := newFakeInstances(instancesRun("no shell here\n", 127))
	d := testInstanceDoctor(t, f)

	r := testInstanceResults(t, d, Options{KeepFailedTestInstance: true})[0]
	if !strings.Contains(r.Detail, "kept as doctor-") {
		t.Errorf("detail = %q, want it to name the kept instance", r.Detail)
	}
	if n := f.left(); n != 1 {
		t.Errorf("%d test instances left, want the failed one kept", n)
	}
}

func TestATestInstanceWhoseImageCannotBePulled(t *testing.T) {
	f := newFakeInstances(nil)
	f.createErr = &image.PullError{Ref: "busybox", Cause: errors.New("connection refused")}
	r := testInstanceResults(t, testInstanceDoctor(t, f), Options{})[0]

	if r.Status != StatusFailed || !strings.Contains(r.Hint, "dicer doctor --image") {
		t.Errorf("result = %s %q (hint %q), want failed with a hint to name another image", r.Status, r.Detail, r.Hint)
	}
}

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		ok   bool
	}{
		{name: "the defaults", opts: Options{TestInstance: true}, ok: true},
		{name: "an image", opts: Options{TestInstanceImage: "registry.example.com/busybox:1.37"}, ok: true},
		{name: "an invalid image", opts: Options{TestInstanceImage: "Not An Image"}},
		{name: "a hypervisor", opts: Options{TestInstanceHypervisor: hypervisor.TypeFirecracker}, ok: true},
		{name: "an unknown hypervisor", opts: Options{TestInstanceHypervisor: "qemu"}},
		{name: "a negative timeout", opts: Options{TestInstanceTimeout: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.ok && err != nil || !tt.ok && !errors.Is(err, errdefs.ErrInvalidArgument) {
				t.Errorf("Validate = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestConsoleExcerptShowsThePanic(t *testing.T) {
	boot := []string{
		"booting", "Run /init as init process", "Kernel panic - not syncing: Attempted to kill init!",
		"Rebooting in 1 seconds..", "booting again", "rcu: ...",
	}
	got := consoleExcerpt(boot)
	if len(got) == 0 || got[len(got)-1] != "Kernel panic - not syncing: Attempted to kill init!" ||
		!slices.Contains(got, "Run /init as init process") {
		t.Errorf("excerpt = %q, want the panic and what led to it", got)
	}

	plain := []string{"a", "b", "c", "d", "e", "f", "g"}
	if got := consoleExcerpt(plain); !slices.Equal(got, plain[2:]) {
		t.Errorf("excerpt without a panic = %q, want the last five lines", got)
	}
}
