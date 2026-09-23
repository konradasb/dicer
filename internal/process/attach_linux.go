// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package process

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Attach adopts a running process the daemon did not start, such as a VMM
// that outlived the previous daemon. It pins the process with a pidfd and
// checks that arg is on its command line, so a reused PID is not mistaken for
// it.
func Attach(pid int, arg string) (*Process, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, fmt.Errorf("open pidfd for %d: %w", pid, err)
	}

	if err := verify(fd, pid, arg); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	// mu guards fd against being closed, and its number reused, while
	// Kill is signalling through it.
	var (
		mu     sync.Mutex
		closed bool
	)

	p := &Process{
		pid:  pid,
		done: make(chan struct{}),
		err:  ErrExitStatusUnknown,
		kill: func() error {
			mu.Lock()
			defer mu.Unlock()
			if closed {
				return nil
			}
			return unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		},
	}

	go func() {
		// A pidfd becomes readable when its process exits. There is
		// nothing to read; readiness is the whole message.
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			if _, err := unix.Poll(fds, -1); !errors.Is(err, unix.EINTR) {
				break
			}
		}
		mu.Lock()
		closed = true
		_ = unix.Close(fd)
		mu.Unlock()

		close(p.done)
	}()

	return p, nil
}

// cmdlineRetries and cmdlineRetryInterval bound the wait for a process being
// exec'd to show its command line.
const (
	cmdlineRetries       = 20
	cmdlineRetryInterval = 5 * time.Millisecond
)

// verify checks that the process pinned by fd was started with arg.
func verify(fd, pid int, arg string) error {
	var (
		cmdline []byte
		err     error
	)
	for range cmdlineRetries {
		cmdline, err = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			return fmt.Errorf("read command line of %d: %w", pid, err)
		}
		if len(cmdline) > 0 {
			break
		}
		time.Sleep(cmdlineRetryInterval)
	}

	if !containsArg(cmdline, arg) {
		return fmt.Errorf("process %d was not started with %q; the PID has been reused", pid, arg)
	}

	// The command line was read by PID, so it could belong to a process
	// that replaced ours between PidfdOpen and ReadFile. If ours is still
	// alive, nothing can have taken its number.
	if err := unix.PidfdSendSignal(fd, 0, nil, 0); err != nil {
		return fmt.Errorf("process %d exited: %w", pid, err)
	}

	return nil
}

// containsArg reports whether a NUL-separated /proc cmdline holds arg as one
// of its arguments.
func containsArg(cmdline []byte, arg string) bool {
	for a := range bytes.SplitSeq(bytes.TrimRight(cmdline, "\x00"), []byte{0}) {
		if string(a) == arg {
			return true
		}
	}
	return false
}
