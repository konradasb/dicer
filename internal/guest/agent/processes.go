// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/user"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/procfs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	diceragentv1 "github.com/konradasb/dicer/proto/diceragent/v1"
)

// kernelThreadFlag marks a kernel thread in a process's stat (PF_KTHREAD).
const kernelThreadFlag = 0x00200000

// ListProcesses implements diceragentv1.AgentServiceServer: it lists the
// processes running in the VM.
func (s *server) ListProcesses(
	context.Context, *diceragentv1.ListProcessesRequest,
) (*diceragentv1.ListProcessesResponse, error) {
	proc, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read /proc: %v", err)
	}

	processes, err := processesIn(proc)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &diceragentv1.ListProcessesResponse{Processes: processes}, nil
}

// processesIn reads the processes in proc, other than kernel threads, in
// PID order. A process that exits while it is read is left out.
func processesIn(proc procfs.FS) ([]*diceragentv1.Process, error) {
	procfsProcesses, err := proc.AllProcs()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	slices.SortFunc(procfsProcesses, func(a, b procfs.Proc) int { return cmp.Compare(a.PID, b.PID) })

	userNames := make(map[uint64]string)
	processes := make([]*diceragentv1.Process, 0, len(procfsProcesses))
	for _, p := range procfsProcesses {
		stat, err := p.Stat()
		if processExited(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read process %d: %w", p.PID, err)
		}
		if stat.Flags&kernelThreadFlag != 0 {
			continue
		}

		process, err := processOf(p, stat, userNames)
		if processExited(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read process %d: %w", p.PID, err)
		}
		processes = append(processes, process)
	}
	return processes, nil
}

// processOf returns the process p, whose stat has already been read, reading
// what stat leaves out. userNames caches user names by UID.
func processOf(p procfs.Proc, stat procfs.ProcStat, userNames map[uint64]string) (*diceragentv1.Process, error) {
	command, err := p.CmdLine()
	if err != nil {
		return nil, err
	}
	procStatus, err := p.NewStatus()
	if err != nil {
		return nil, err
	}
	started, err := stat.StartTime()
	if err != nil {
		return nil, err
	}

	return &diceragentv1.Process{
		Pid:                 int32(stat.PID),
		Ppid:                int32(stat.PPID),
		User:                userName(procStatus.UIDs[1], userNames),
		State:               stat.State,
		Name:                stat.Comm,
		Command:             command,
		StartTime:           timestamppb.New(time.UnixMilli(int64(started * 1000))),
		CpuTime:             durationpb.New(time.Duration(stat.CPUTime() * float64(time.Second))),
		ResidentMemoryBytes: int64(stat.ResidentMemory()),
	}, nil
}

// userName returns the name of the user uid in the guest's user database,
// or the UID itself if it has none. userNames caches the answer.
func userName(uid uint64, userNames map[uint64]string) string {
	if name, ok := userNames[uid]; ok {
		return name
	}

	id := strconv.FormatUint(uid, 10)
	name := id
	if u, err := user.LookupId(id); err == nil {
		name = u.Username
	}
	userNames[uid] = name
	return name
}

// processExited reports whether err is from reading a process that has
// exited.
func processExited(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}
