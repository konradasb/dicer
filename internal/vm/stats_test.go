// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
)

// fakeProc is a procfs tree for a test Manager to read stats from.
type fakeProc struct{ dir string }

// newFakeProc points mgr at an empty procfs tree.
func newFakeProc(t *testing.T, mgr *Manager) fakeProc {
	t.Helper()

	p := fakeProc{dir: t.TempDir()}
	mgr.procDir = p.dir
	p.write(t, "net/dev", netDevHeader)

	return p
}

// netDevHeader is the two lines /proc/net/dev starts with.
const netDevHeader = "Inter-|   Receive                                                |  Transmit\n" +
	" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"

// process writes the stat and io of the process pid: CPU time in clock
// ticks of 1/100s, resident memory in pages.
func (p fakeProc) process(t *testing.T, pid int, utime, stime, rssPages, readBytes, writeBytes uint64) {
	t.Helper()

	// The 42 fields after the command name, all zero but those set.
	fields := make([]string, 42)
	for i := range fields {
		fields[i] = "0"
	}
	fields[0] = "S"
	fields[11], fields[12] = strconv.FormatUint(utime, 10), strconv.FormatUint(stime, 10)
	fields[21] = strconv.FormatUint(rssPages, 10)

	dir := strconv.Itoa(pid)
	p.write(t, filepath.Join(dir, "stat"), fmt.Sprintf("%d (cloud-hypervisor) %s\n", pid, strings.Join(fields, " ")))
	p.write(t, filepath.Join(dir, "io"), fmt.Sprintf(
		"rchar: 0\nwchar: 0\nsyscr: 0\nsyscw: 0\nread_bytes: %d\nwrite_bytes: %d\ncancelled_write_bytes: 0\n",
		readBytes, writeBytes))
}

// deviceCounters are one direction of a network device's counters.
type deviceCounters struct {
	bytes, packets, errors, drops uint64
}

// device adds a network device's counters to net/dev.
func (p fakeProc) device(t *testing.T, name string, rx, tx deviceCounters) {
	t.Helper()

	f, err := os.OpenFile(filepath.Join(p.dir, "net", "dev"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	// Each direction is bytes, packets, errs, drop, fifo and three more.
	if _, err := fmt.Fprintf(f, "%s: %d %d %d %d 0 0 0 0 %d %d %d %d 0 0 0 0\n",
		name, rx.bytes, rx.packets, rx.errors, rx.drops, tx.bytes, tx.packets, tx.errors, tx.drops); err != nil {
		t.Fatal(err)
	}
}

func (p fakeProc) write(t *testing.T, name, content string) {
	t.Helper()

	path := filepath.Join(p.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStatsAreReadFromTheVMMAndItsTAPDevice(t *testing.T) {
	h := newHarness(t)
	h.inst.MemoryBytes = 1 << 30
	h.definitions.instances[h.inst.Name] = h.inst
	h.start(t)

	proc := newFakeProc(t, h.mgr)
	proc.process(t, h.starter.vmm().PID(), 250, 50, 1000, 4096, 8192)
	proc.device(t, "lo", deviceCounters{bytes: 1}, deviceCounters{bytes: 1})
	// What the host's end of the TAP receives, the guest transmitted.
	proc.device(t, network.TAPName(h.inst.ID),
		deviceCounters{bytes: 300, packets: 5, errors: 1, drops: 2},
		deviceCounters{bytes: 700, packets: 9, errors: 3, drops: 4})

	before := time.Now()
	stats := h.mgr.Stats()
	if len(stats) != 1 {
		t.Fatalf("Stats() = %+v, want the one running instance", stats)
	}
	got := stats[0]

	want := types.InstanceStats{
		InstanceID:             h.inst.ID,
		Name:                   h.inst.Name,
		StartedAt:              h.runtime(t).StartedAt,
		ReadAt:                 got.ReadAt,
		Committed:              types.Resources{VCPUs: 1, MemoryBytes: 1 << 30},
		CPUTime:                3 * time.Second,
		ResidentMemoryBytes:    1000 * int64(os.Getpagesize()),
		DiskReadBytes:          4096,
		DiskWrittenBytes:       8192,
		NetworkReceiveBytes:    700,
		NetworkTransmitBytes:   300,
		NetworkReceivePackets:  9,
		NetworkTransmitPackets: 5,
		NetworkReceiveDrops:    4,
		NetworkTransmitDrops:   2,
		NetworkReceiveErrors:   3,
		NetworkTransmitErrors:  1,
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want the run's, %v", got.StartedAt, want.StartedAt)
	}
	want.StartedAt = got.StartedAt
	if got != want {
		t.Errorf("Stats()[0] =\n%+v\nwant\n%+v", got, want)
	}
	if got.ReadAt.Before(before) {
		t.Errorf("ReadAt = %v, before Stats was called at %v", got.ReadAt, before)
	}
}

func TestStatsLeaveOutInstancesWithNoVMM(t *testing.T) {
	h := newHarness(t)
	newFakeProc(t, h.mgr)

	// Recorded as running, but no VMM was started, as for a stopped one.
	h.running(t)

	if stats := h.mgr.Stats(); len(stats) != 0 {
		t.Errorf("Stats() = %+v, want none without a VMM", stats)
	}
}

func TestStatsLeaveOutAVMMThatCannotBeRead(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	newFakeProc(t, h.mgr)

	if stats := h.mgr.Stats(); len(stats) != 0 {
		t.Errorf("Stats() = %+v, want none for a VMM with nothing in /proc", stats)
	}
}

func TestStatsLeaveOutAVMMThatHasExited(t *testing.T) {
	h := newHarness(t)
	h.start(t)

	vmm := h.starter.vmm()
	proc := newFakeProc(t, h.mgr)
	proc.process(t, vmm.PID(), 1, 1, 1, 0, 0)

	// The supervisor has not yet noticed the exit, but the PID may already
	// be another process's.
	h.mgr.Close()
	vmm.Terminate()

	if stats := h.mgr.Stats(); len(stats) != 0 {
		t.Errorf("Stats() = %+v, want none for a VMM that has exited", stats)
	}
}
