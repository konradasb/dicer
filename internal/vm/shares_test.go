// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/process"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/virtiofs"
)

// fakeShares stands in for virtiofsd: each share is a process that lives
// until it is terminated, and what it was asked to share is recorded.
type fakeShares struct {
	mu       sync.Mutex
	started  []startedShare
	procs    []*process.Process
	startErr error
}

type startedShare struct {
	socket, dir string
	readOnly    bool
}

func (f *fakeShares) Start(ctx context.Context, s virtiofs.Share) (*process.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.startErr != nil && len(f.started) > 0 {
		// The first share starts; the second does not.
		return nil, f.startErr
	}
	p, err := process.Start(exec.CommandContext(ctx, "sleep", "60"))
	if err != nil {
		return nil, err
	}
	f.started = append(f.started, startedShare{socket: s.Socket, dir: s.Dir, readOnly: s.ReadOnly})
	f.procs = append(f.procs, p)
	return p, nil
}

// ended reports whether every share's process has ended.
func (f *fakeShares) ended(t *testing.T) bool {
	t.Helper()
	for _, p := range f.procs {
		select {
		case <-p.Done():
		case <-time.After(5 * time.Second):
			return false
		}
	}
	return true
}

// withDirectories gives the harness's instance two host directories to
// mount, the second read-only.
func withDirectories(t *testing.T, h *harness) (string, string) {
	t.Helper()
	src, docs := t.TempDir(), t.TempDir()
	h.inst.Mounts = []types.Mount{
		{Type: types.MountDirectory, Source: src, Target: "/app"},
		{Type: types.MountDirectory, Source: docs, Target: "/docs", ReadOnly: true},
	}
	h.definitions.instances[h.inst.Name] = h.inst
	return src, docs
}

func TestStartSharesDirectories(t *testing.T) {
	h := newHarness(t)
	shares := &fakeShares{}
	h.mgr.shares = shares
	src, docs := withDirectories(t, h)

	h.start(t)

	runDir := h.mgr.runtimeDir(h.inst.ID)
	want := []startedShare{
		{socket: filepath.Join(runDir, "fs0.sock"), dir: src},
		{socket: filepath.Join(runDir, "fs1.sock"), dir: docs, readOnly: true},
	}
	if !slices.Equal(shares.started, want) {
		t.Errorf("shares = %+v\nwant     %+v", shares.started, want)
	}

	fs := h.starter.spec.Filesystems
	if len(fs) != 2 || fs[0].Tag != "dicerfs0" || fs[0].Socket != want[0].socket ||
		fs[1].Tag != "dicerfs1" || fs[1].Socket != want[1].socket {
		t.Errorf("filesystems = %+v, want a device on each share's socket", fs)
	}
}

func TestResolveMountsSharesDirectories(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")
	dir := t.TempDir()
	inst.Mounts = []types.Mount{{Type: types.MountDirectory, Source: dir, Target: "/app", ReadOnly: true}}

	resolved, err := mgr.resolveMounts(inst)
	mounts, disks, shares := resolved.guest, resolved.disks, resolved.shares
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 0 {
		t.Errorf("disks = %+v, want none: a directory is no disk", disks)
	}
	if len(mounts) != 1 || mounts[0].Directory == nil || mounts[0].Directory.Tag != "dicerfs0" || !mounts[0].ReadOnly {
		t.Errorf("mounts = %+v, want the share mounted read-only by its tag", mounts)
	}
	if len(shares) != 1 || shares[0].source != dir || shares[0].tag != "dicerfs0" {
		t.Errorf("shares = %+v, want the directory", shares)
	}

	// What is not there, or is a file, cannot be shared.
	for _, source := range []string{filepath.Join(dir, "missing"), "/etc/hostname"} {
		inst.Mounts[0].Source = source
		if _, err := mgr.resolveMounts(inst); err == nil {
			t.Errorf("resolveMounts shared %s, which is not a directory", source)
		}
	}
}

func TestStartWithoutVirtiofsd(t *testing.T) {
	h := newHarness(t)
	h.mgr.shares = nil
	withDirectories(t, h)

	err := h.mgr.Start(context.Background(), h.inst)
	if err == nil || !strings.Contains(err.Error(), "install virtiofsd") {
		t.Errorf("Start = %v, want it to say virtiofsd is needed", err)
	}
	if len(h.starter.vmms) != 0 {
		t.Error("a VMM was started for an instance whose directories cannot be shared")
	}
}

func TestStartStopsSharesWhenItFails(t *testing.T) {
	// The VMM does not start: the shares already started are stopped.
	h := newHarness(t)
	shares := &fakeShares{}
	h.mgr.shares = shares
	withDirectories(t, h)
	h.starter.startErr = errors.New("no KVM")

	if err := h.mgr.Start(context.Background(), h.inst); err == nil {
		t.Fatal("Start succeeded with a VMM that does not start")
	}
	if len(shares.procs) != 2 || !shares.ended(t) {
		t.Errorf("%d shares were started, and not all were stopped when the start failed", len(shares.procs))
	}

	// The second share does not start: the first is stopped.
	h2 := newHarness(t)
	shares2 := &fakeShares{startErr: errors.New("virtiofsd exited")}
	h2.mgr.shares = shares2
	withDirectories(t, h2)

	err := h2.mgr.Start(context.Background(), h2.inst)
	if err == nil || !strings.Contains(err.Error(), "mount on /docs") {
		t.Errorf("Start = %v, want the failing share named", err)
	}
	if len(shares2.procs) != 1 || !shares2.ended(t) {
		t.Error("the share that started was not stopped when the next one failed")
	}
	if len(h2.starter.vmms) != 0 {
		t.Error("a VMM was started though a share failed")
	}
}

func TestStartRefusesDirectoriesOnFirecracker(t *testing.T) {
	h := newHarness(t)
	h.mgr.shares = &fakeShares{}
	withDirectories(t, h)
	h.inst.HypervisorType = types.HypervisorFirecracker
	h.definitions.instances[h.inst.Name] = h.inst
	h.mgr.starters[types.HypervisorFirecracker] = h.mgr.starters[types.HypervisorCloudHypervisor]

	err := h.mgr.Start(context.Background(), h.inst)
	if err == nil || !strings.Contains(err.Error(), "directory mounts need cloud-hypervisor") {
		t.Errorf("Start on Firecracker = %v, want directory mounts refused", err)
	}
}

func TestSnapshotsRefuseSharedDirectories(t *testing.T) {
	h := newHarness(t)
	h.mgr.shares = &fakeShares{}
	withDirectories(t, h)
	h.start(t)

	_, err := h.mgr.CreateSnapshot(context.Background(), h.inst, "before")
	if err == nil || !strings.Contains(err.Error(), "mounts a host directory") {
		t.Errorf("CreateSnapshot = %v, want it refused", err)
	}

	if err := h.mgr.RestoreSnapshot(context.Background(), h.inst, "before"); err == nil {
		t.Error("RestoreSnapshot of an instance that shares a directory succeeded")
	}
}
