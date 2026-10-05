// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/network"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	"github.com/konradasb/dicer/internal/volume"
)

// wantClass checks that an error reached the client in the class the handler
// put it in, which is the whole of what a client can match on.
func wantClass(t *testing.T, err, class error) {
	t.Helper()

	if !errors.Is(err, class) {
		t.Errorf("error %v is not in class %v", err, class)
	}
}

// fakeRecorder keeps the events recorded.
type fakeRecorder struct {
	events []events.Event
}

func (f *fakeRecorder) Record(e events.Event) { f.events = append(f.events, e) }

// testCapacity is a 4-CPU, 8GiB host with the daemon's default admission:
// 16 vCPUs and 7GiB.
var testCapacity = types.Capacity{
	Host:                types.Resources{VCPUs: 4, MemoryBytes: 8 << 30},
	ReservedMemoryBytes: 1 << 30,
	CPUOvercommit:       4,
	MemoryOvercommit:    1,
}

// newTestServer returns a Server over real definitions and a lifecycle
// manager with testCapacity, and the definitions.
func newTestServer(t *testing.T) (*Server, *filestore.Manager) {
	t.Helper()

	logger := slog.New(slog.DiscardHandler)
	dataDir := filepath.Join(t.TempDir(), "data")

	definitions, err := filestore.NewManager(filestore.Config{DataDir: dataDir, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}

	networks, err := network.NewManager(network.Config{Dir: filepath.Join(dataDir, "allocations"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}

	kernels, err := kernel.NewManager(kernel.Config{DataDir: dataDir, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}

	instances := vm.NewManager(vm.Config{
		Definitions: definitions,
		RunDir:      filepath.Join(t.TempDir(), "run"),
		Capacity:    testCapacity,
		Logger:      logger,
	})

	return NewServer(Config{
		Definitions: definitions,
		Networks:    networks,
		Instances:   instances,
		Volumes:     volume.NewManager(volume.Config{DataDir: dataDir, Logger: logger}),
		Kernels:     kernels,
		DataDir:     dataDir,
	}), definitions
}
