// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

// Package process provides handles on long-lived processes such as VMMs,
// started or adopted, that report when the process exits.
package process

import (
	"errors"
	"fmt"
	"os/exec"
)

// ErrExitStatusUnknown is the exit error of an adopted process. Only a
// process's parent can collect its exit status, and an adopted process was
// started by a previous daemon.
var ErrExitStatusUnknown = errors.New("exit status unavailable for an adopted process")

// Process is a handle on a supervised process.
type Process struct {
	pid  int
	done chan struct{}
	err  error
	kill func() error
}

// Start starts cmd and returns a handle on it. The process is reaped as soon
// as it exits, so it never lingers as a zombie, and its exit status is kept
// for Err.
func Start(cmd *exec.Cmd) (*Process, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p := &Process{
		pid:  cmd.Process.Pid,
		done: make(chan struct{}),
		kill: cmd.Process.Kill,
	}

	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()

	return p, nil
}

// PID returns the process ID.
func (p *Process) PID() int { return p.pid }

// Done returns a channel that is closed when the process exits.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err reports why the process exited: nil for a clean exit, an
// *exec.ExitError for a child that failed or was killed, and
// ErrExitStatusUnknown for an adopted process. It must only be called after
// Done is closed.
func (p *Process) Err() error { return p.err }

// Kill sends SIGKILL. Killing a process that has already exited is not an
// error: the caller wanted it gone, and it is.
func (p *Process) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}

	if err := p.kill(); err != nil {
		select {
		case <-p.done:
			return nil
		default:
		}
		return fmt.Errorf("kill process %d: %w", p.pid, err)
	}

	return nil
}

// Terminate kills the process and waits for it to exit.
func (p *Process) Terminate() {
	_ = p.Kill()
	<-p.done
}
