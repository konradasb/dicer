// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/nrednav/cuid2"

	"github.com/konradasb/dicer/internal/guest"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/image/reference"
	"github.com/konradasb/dicer/internal/instance"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/network"
)

// What the test instance prints, which is how its run is judged.
const (
	testInstanceBooted  = "dicer-doctor: booted"
	testInstanceOnline  = "dicer-doctor: online"
	testInstanceOffline = "dicer-doctor: offline"
)

// testInstanceOnlineURL is what the test instance fetches to show that it
// reaches the internet.
const testInstanceOnlineURL = "http://example.com/"

// instances is what the test instance needs of the instance manager.
type instances interface {
	Create(ctx context.Context, spec instance.Spec, pull image.PullPolicy) error
	Waiter(nameOrID string, opts instance.WaitOptions) (waiter, error)
	Start(ctx context.Context, nameOrID string) error
	StreamLogs(ctx context.Context, nameOrID string, opts instance.LogOptions, w io.Writer) error
	Delete(ctx context.Context, nameOrID string, force bool) error
}

// waiter waits for a test instance to stop.
type waiter interface {
	Wait(ctx context.Context) (instance.Status, error)
	Close()
}

// managerInstances is the instance manager, as the test instance uses it.
type managerInstances struct {
	*instance.Manager
}

// Waiter returns a waiter for an instance, which the caller must Close.
func (m managerInstances) Waiter(nameOrID string, opts instance.WaitOptions) (waiter, error) {
	w, err := m.Manager.Waiter(nameOrID, opts)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// testInstanceResults boots a test instance as opts says and yields what it
// showed, then, if it ran its command, whether it reached the internet.
func (d *Doctor) testInstanceResults(ctx context.Context, opts Options, yield func(Result) bool) {
	r, online := d.testInstance(ctx, opts)
	if !yield(r) || online == nil {
		return
	}
	yield(internetResult(*online))
}

// testInstance boots a test instance as opts says, has it run a command and
// try to reach the internet, and deletes it, unless opts says to keep it
// and it failed. It returns what the instance showed, and whether it
// reached the internet, or nil if it did not run its command.
func (d *Doctor) testInstance(ctx context.Context, opts Options) (r Result, online *bool) {
	// Named, so that the deferred deletion can say a failed instance was kept.
	r = Result{Group: GroupInstances, Name: string(opts.TestInstanceHypervisor), Status: StatusFailed}

	spec, err := testInstanceSpec(opts)
	if err != nil {
		r.Detail = "could not be defined: " + err.Error()
		return r, nil
	}
	if err := d.instances.Create(ctx, spec, image.PullPolicyMissing); err != nil {
		r.Detail = "could not be created: " + err.Error()
		if pullErr := (*image.PullError)(nil); errors.As(err, &pullErr) {
			r.Hint = "name an image the host can pull, with dicer doctor --image"
		}
		return r, nil
	}
	defer func() {
		if opts.KeepFailedTestInstance && r.Status == StatusFailed {
			r.Detail += fmt.Sprintf("; kept as %s, delete it with dicer rm -f %s", spec.Name, spec.Name)
			return
		}
		deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = d.instances.Delete(deleteCtx, spec.ID, true)
	}()

	waitCtx, cancel := context.WithTimeout(ctx, opts.TestInstanceTimeout)
	defer cancel()
	w, err := d.instances.Waiter(spec.ID, instance.WaitOptions{ID: spec.ID, NextStop: true})
	if err != nil {
		r.Detail = "could not be waited for: " + err.Error()
		return r, nil
	}
	defer w.Close()

	started := time.Now()
	if err := d.instances.Start(waitCtx, spec.ID); err != nil {
		r.Detail = "did not start: " + err.Error()
		r.Console = consoleExcerpt(d.testInstanceConsole(ctx, spec.ID))
		return r, nil
	}

	status, err := w.Wait(waitCtx)
	took := time.Since(started)
	console := d.testInstanceConsole(ctx, spec.ID)
	booted := slices.Contains(console, testInstanceBooted)

	switch {
	case waitCtx.Err() != nil:
		r.Detail = "did not run its command within " + humanize.Duration(opts.TestInstanceTimeout)
	case err != nil:
		r.Detail = "could not be waited for: " + err.Error()
	case !booted:
		r.Detail = "ended without running its command: " + stopDescription(status)
	case status.ExitCode == nil || *status.ExitCode != 0:
		r.Detail = "ran its command, then ended: " + stopDescription(status)
	default:
		r.Status = StatusOK
		r.Detail = "booted, ran a command and stopped in " + humanize.Duration(took)
	}

	if booted {
		reached := slices.Contains(console, testInstanceOnline)
		online = &reached
	}
	if r.Status == StatusFailed {
		r.Console = consoleExcerpt(withoutTestInstanceLines(console))
	}
	return r, online
}

// testInstanceSpec returns the definition of the small test instance opts
// asks for, which prints whether it booted and reached the internet, then
// stops.
func testInstanceSpec(opts Options) (instance.Spec, error) {
	ref, err := reference.Parse(opts.TestInstanceImage)
	if err != nil {
		return instance.Spec{}, err
	}
	script := fmt.Sprintf("echo %s; if wget -q -T 5 -O /dev/null %s; then echo %s; else echo %s; fi",
		testInstanceBooted, testInstanceOnlineURL, testInstanceOnline, testInstanceOffline)

	now := time.Now()
	return instance.Spec{
		ID:                cuid2.Generate(),
		Name:              naming.Generate("doctor"),
		ImageRef:          ref.String(),
		HypervisorType:    opts.TestInstanceHypervisor,
		HypervisorVersion: opts.TestInstanceHypervisorVersion,
		KernelName:        kernel.DefaultName,
		VCPUs:             1,
		MemoryBytes:       512 << 20,
		DiskBytes:         1 << 30,
		NetworkName:       network.DefaultName,
		Cmd:               []string{"sh", "-c", script},
		Restart:           instance.RestartPolicy{Mode: instance.RestartModeNo},
		InitMode:          guest.InitModeAuto,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// stopDescription says how a test instance stopped: with its command's exit
// code, or with why it failed.
func stopDescription(status instance.Status) string {
	switch {
	case status.ExitCode != nil:
		return fmt.Sprintf("exit code %d", *status.ExitCode)
	case status.StateError != "":
		return status.StateError
	default:
		return string(status.State)
	}
}

// internetResult reports whether a test instance reached the internet.
func internetResult(online bool) Result {
	r := Result{Group: GroupInstances, Name: "internet"}
	if online {
		r.Status, r.Detail = StatusOK, "a test instance reached the internet"
		return r
	}
	r.Status, r.Detail = StatusWarning, "a test instance could not reach "+testInstanceOnlineURL
	r.Hint = "check that the host is online, and the firewall and uplink checks"
	return r
}

// testInstanceConsole returns the lines of a test instance's console log, or
// none if it cannot be read.
func (d *Doctor) testInstanceConsole(ctx context.Context, nameOrID string) []string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	var buf bytes.Buffer
	if err := d.instances.StreamLogs(ctx, nameOrID, instance.LogOptions{TailLines: 200}, &buf); err != nil {
		return nil
	}

	var lines []string
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// consoleExcerpt returns what of a failed guest's console says most about
// why: the kernel's panic, with the lines before it, or else its last
// lines. A guest that rebooted over and over may have stopped anywhere in
// its boot, so its last lines can say nothing.
func consoleExcerpt(lines []string) []string {
	const n = 5
	for i, line := range lines {
		if strings.Contains(line, "Kernel panic") {
			return lines[max(0, i-n+2):min(len(lines), i+1)]
		}
	}
	return lines[max(0, len(lines)-n):]
}

// withoutTestInstanceLines returns lines without those the test instance
// printed for the doctor itself.
func withoutTestInstanceLines(lines []string) []string {
	return slices.DeleteFunc(slices.Clone(lines), func(l string) bool {
		return strings.HasPrefix(l, "dicer-doctor:")
	})
}
