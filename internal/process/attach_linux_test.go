// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

//go:build linux

package process

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
)

// startMarked starts a process carrying marker on its command line, as a VMM
// carries its socket path. It is started with exec directly rather than with
// Start so that the test, like a restarted daemon, has only a PID.
func startMarked(t *testing.T, marker string) *exec.Cmd {
	t.Helper()

	// sh takes the argument after the script as $0, so it appears in
	// cmdline without being interpreted. The script loops rather than
	// sleeping once: a shell handed a single simple command may exec it,
	// replacing the command line -- marker and all -- with the command's.
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "while :; do sleep 1; done", marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	return cmd
}

func TestAttachSeesExit(t *testing.T) {
	cmd := startMarked(t, "/run/dicer/instances/a/hypervisor.sock")

	p, err := Attach(cmd.Process.Pid, "/run/dicer/instances/a/hypervisor.sock")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	select {
	case <-p.Done():
		t.Fatal("Done closed before the process exited")
	default:
	}

	// Killed by someone else, the way an operator's kill -9 would be.
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_ = cmd.Wait()
	waitDone(t, p)

	if !errors.Is(p.Err(), ErrExitStatusUnknown) {
		t.Errorf("Err = %v, want ErrExitStatusUnknown", p.Err())
	}
}

func TestAttachKill(t *testing.T) {
	cmd := startMarked(t, "/sock")

	p, err := Attach(cmd.Process.Pid, "/sock")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_ = cmd.Wait()
	waitDone(t, p)

	if err := p.Kill(); err != nil {
		t.Errorf("Kill after exit = %v, want nil", err)
	}
}

func TestAttachRejectsOtherProcess(t *testing.T) {
	cmd := startMarked(t, "/run/dicer/instances/a/hypervisor.sock")

	if _, err := Attach(cmd.Process.Pid, "/run/dicer/instances/b/hypervisor.sock"); err == nil {
		t.Error("Attach accepted a process started without the argument")
	}
}

func TestAttachRejectsDeadProcess(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := Attach(cmd.ProcessState.Pid(), "anything"); err == nil {
		t.Error("Attach accepted a process that has exited")
	}
}

func TestContainsArg(t *testing.T) {
	cmdline := []byte("cloud-hypervisor\x00--api-socket\x00/run/x.sock\x00")

	if !containsArg(cmdline, "/run/x.sock") {
		t.Error("did not find a whole argument")
	}
	if containsArg(cmdline, "/run/x") {
		t.Error("matched a prefix of an argument")
	}
}
