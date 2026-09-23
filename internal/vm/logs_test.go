// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/types"
)

// writeGuestLog puts a serial console log where the hypervisor would.
func writeGuestLog(t *testing.T, mgr *Manager, inst types.InstanceSpec, contents string) string {
	t.Helper()

	path, err := mgr.logPath(inst, LogSourceGuest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestStreamLogs(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")
	writeGuestLog(t, mgr, inst, "booting\nready\n")

	var out bytes.Buffer
	if err := mgr.StreamLogs(t.Context(), inst, LogOptions{}, &out); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}

	if out.String() != "booting\nready\n" {
		t.Errorf("logs = %q, want the whole file", out.String())
	}
}

func TestStreamLogsTail(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")

	var sb strings.Builder
	for i := range 500 {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	writeGuestLog(t, mgr, inst, sb.String())

	tests := []struct {
		tail      int
		wantLines int
		wantFirst string
	}{
		{tail: 3, wantLines: 3, wantFirst: "line 497"},
		{tail: 1, wantLines: 1, wantFirst: "line 499"},
		// Asking for more lines than there are gives the whole file.
		{tail: 5000, wantLines: 500, wantFirst: "line 0"},
	}

	for _, tt := range tests {
		var out bytes.Buffer
		if err := mgr.StreamLogs(t.Context(), inst, LogOptions{TailLines: tt.tail}, &out); err != nil {
			t.Fatalf("StreamLogs(tail=%d): %v", tt.tail, err)
		}

		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if len(lines) != tt.wantLines {
			t.Errorf("tail=%d gave %d lines, want %d", tt.tail, len(lines), tt.wantLines)
		}
		if lines[0] != tt.wantFirst {
			t.Errorf("tail=%d starts at %q, want %q", tt.tail, lines[0], tt.wantFirst)
		}
	}
}

// TestStreamLogsTailSpansChunks covers a tail that has to read back through
// more than one chunk of the file.
func TestStreamLogsTailSpansChunks(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")

	line := strings.Repeat("x", 1000) + "\n"
	var sb strings.Builder
	for range 100 { // ~100 KiB, several chunks
		sb.WriteString(line)
	}
	sb.WriteString("last\n")
	writeGuestLog(t, mgr, inst, sb.String())

	var out bytes.Buffer
	if err := mgr.StreamLogs(t.Context(), inst, LogOptions{TailLines: 2}, &out); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}

	if got := strings.Count(out.String(), "\n"); got != 2 {
		t.Errorf("got %d lines, want 2", got)
	}
	if !strings.HasSuffix(out.String(), "last\n") {
		t.Error("the tail does not end at the end of the file")
	}
}

// TestStreamLogsFollowStopsWithInstance checks that following a log ends
// when the instance does, rather than hanging on a file nothing will write
// to again.
func TestStreamLogsFollowStopsWithInstance(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")
	path := writeGuestLog(t, mgr, inst, "booting\n")

	pid := os.Getpid()
	if err := mgr.writeRuntime(types.InstanceStatus{InstanceID: inst.ID, State: types.StateRunning, HypervisorPID: &pid}); err != nil {
		t.Fatal(err)
	}

	var (
		mu   sync.Mutex
		out  bytes.Buffer
		done = make(chan error, 1)
	)
	go func() {
		done <- mgr.StreamLogs(t.Context(), inst, LogOptions{Follow: true}, &lockedWriter{mu: &mu, w: &out})
	}()

	// Output written while it runs is followed.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Stopping the instance ends the stream.
	time.Sleep(2 * logPollInterval)
	forceState(t, mgr, inst.ID, types.StateStopping)
	forceState(t, mgr, inst.ID, types.StateStopped)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamLogs: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("following a log did not end when the instance stopped")
	}

	mu.Lock()
	defer mu.Unlock()
	if got := out.String(); !strings.Contains(got, "booting") || !strings.Contains(got, "ready") {
		t.Errorf("logs = %q, want what was written before and during the follow", got)
	}
}

func TestStreamLogsMissing(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")

	err := mgr.StreamLogs(t.Context(), inst, LogOptions{}, &bytes.Buffer{})
	if !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("StreamLogs with no log = %v, want ErrNotFound", err)
	}
}

func TestStreamLogsUnknownSource(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	inst := seedInstance(t, definitions, "web")

	err := mgr.StreamLogs(t.Context(), inst, LogOptions{Source: "syslog"}, &bytes.Buffer{})
	if !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("StreamLogs of an unknown source = %v, want ErrInvalidArgument", err)
	}
}

// lockedWriter lets the test read what has been written while the follow is
// still writing.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
