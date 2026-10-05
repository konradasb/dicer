// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/procfs"
)

// bootTime is when the fake /proc's machine booted, in seconds since the
// epoch.
const bootTime = 1_700_000_000

// fakeProcess is what a test writes of a process into a fake /proc.
type fakeProcess struct {
	pid, ppid, uid int
	comm, state    string
	flags          uint
	cmdline        []string
	// startTicks and cpuTicks are in clock ticks of 1/100s.
	startTicks, cpuTicks uint64
	rssPages             uint64
	// exited leaves the process's status out, as if it exited while /proc
	// was read.
	exited bool
}

// newFakeProc writes processes into a fake /proc and opens it.
func newFakeProc(t *testing.T, processes ...fakeProcess) procfs.FS {
	t.Helper()

	dir := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("stat", fmt.Sprintf("cpu  0 0 0 0 0 0 0 0 0 0\nbtime %d\n", bootTime))
	for _, p := range processes {
		// The 42 fields after the command name, all zero but those set.
		fields := make([]string, 42)
		for i := range fields {
			fields[i] = "0"
		}
		fields[0] = p.state
		fields[1] = strconv.Itoa(p.ppid)
		fields[6] = strconv.FormatUint(uint64(p.flags), 10)
		fields[11] = strconv.FormatUint(p.cpuTicks, 10)
		fields[19] = strconv.FormatUint(p.startTicks, 10)
		fields[21] = strconv.FormatUint(p.rssPages, 10)

		pid := strconv.Itoa(p.pid)
		write(pid+"/stat", fmt.Sprintf("%d (%s) %s\n", p.pid, p.comm, strings.Join(fields, " ")))
		write(pid+"/cmdline", strings.Join(p.cmdline, "\x00"))
		if p.exited {
			continue
		}
		write(pid+"/status", fmt.Sprintf("Name:\t%s\nUid:\t%d\t%d\t%d\t%d\n", p.comm, p.uid, p.uid, p.uid, p.uid))
	}

	proc, err := procfs.NewFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	return proc
}

func TestProcessesAreReadInPIDOrderWithoutKernelThreads(t *testing.T) {
	proc := newFakeProc(t,
		fakeProcess{pid: 30, ppid: 1, uid: 4242424, comm: "nginx", state: "S",
			cmdline: []string{"nginx: worker process"}, startTicks: 250, cpuTicks: 187, rssPages: 10},
		fakeProcess{pid: 2, comm: "kthreadd", state: "S", flags: kernelThreadFlag},
		fakeProcess{pid: 1, uid: 0, comm: "init", state: "S", cmdline: []string{"/init"}},
		fakeProcess{pid: 7, ppid: 2, comm: "kworker/0:0", state: "I", flags: kernelThreadFlag | 0x40},
		fakeProcess{pid: 31, ppid: 30, uid: 0, comm: "sh", state: "Z"},
	)

	processes, err := processesIn(proc)
	if err != nil {
		t.Fatal(err)
	}

	var pids []int32
	for _, p := range processes {
		pids = append(pids, p.GetPid())
	}
	if fmt.Sprint(pids) != "[1 30 31]" {
		t.Fatalf("PIDs = %v, want [1 30 31]: in order, without kernel threads", pids)
	}

	nginx := processes[1]
	if nginx.GetPpid() != 1 || nginx.GetState() != "S" || nginx.GetName() != "nginx" {
		t.Errorf("nginx = %v, want PPID 1, state S, name nginx", nginx)
	}
	if got := nginx.GetCommand(); len(got) != 1 || got[0] != "nginx: worker process" {
		t.Errorf("command = %q, want the command line as it is", got)
	}
	if got, want := nginx.GetStartTime().AsTime(), time.Unix(bootTime+2, 500e6); !got.Equal(want) {
		t.Errorf("start time = %v, want %v: boot time and 250 ticks", got, want)
	}
	if got := nginx.GetCpuTime().AsDuration(); got != 1870*time.Millisecond {
		t.Errorf("CPU time = %v, want 1.87s", got)
	}
	if got, want := nginx.GetResidentMemoryBytes(), int64(10*os.Getpagesize()); got != want {
		t.Errorf("resident memory = %d, want %d", got, want)
	}

	// UID 0 is root in any user database; this one is in none.
	if got := processes[0].GetUser(); got != "root" {
		t.Errorf("init's user = %q, want root", got)
	}
	if got := nginx.GetUser(); got != "4242424" {
		t.Errorf("nginx's user = %q, want its UID, which has no name", got)
	}

	if zombie := processes[2]; len(zombie.GetCommand()) != 0 || zombie.GetName() != "sh" {
		t.Errorf("zombie = %v, want no command line, but its name", zombie)
	}
}

// A process that exits between the listing of /proc and the reading of its
// entries is left out, rather than failing the whole list.
func TestProcessesLeaveOutAProcessThatExited(t *testing.T) {
	proc := newFakeProc(t,
		fakeProcess{pid: 1, comm: "init", state: "S", cmdline: []string{"/init"}},
		fakeProcess{pid: 9, ppid: 1, comm: "sleep", state: "S", cmdline: []string{"sleep", "1"}, exited: true},
	)

	processes, err := processesIn(proc)
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 1 || processes[0].GetPid() != 1 {
		t.Errorf("processes = %v, want only init", processes)
	}
}
