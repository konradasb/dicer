// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

func TestOperationsAreRecordedWithTheirOutcome(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)
	recorder := &fakeMetrics{}
	mgr.metrics = recorder

	inst := types.InstanceSpec{ID: "i-1", Name: "web", VCPUs: 1, MemoryBytes: 1 << 30}
	definitions.instances[inst.Name] = inst

	// Stopping an already-stopped instance succeeds, and is still an
	// operation that happened.
	if err := mgr.Stop(context.Background(), inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Pausing one that is not running fails before it touches the host,
	// which is exactly the kind of failure the counter should catch.
	if err := mgr.Pause(context.Background(), inst); err == nil {
		t.Fatal("Pause on a stopped instance should fail")
	}

	want := []recordedOp{
		{operation: opStop, failed: false},
		{operation: opPause, failed: true},
	}
	if len(recorder.ops) != len(want) {
		t.Fatalf("recorded %v, want %v", recorder.ops, want)
	}
	for i, op := range want {
		if recorder.ops[i] != op {
			t.Errorf("operation %d = %+v, want %+v", i, recorder.ops[i], op)
		}
	}
}

// A Manager built without a recorder records into a discard, so the
// lifecycle code can call it unconditionally.
func TestOperationsWithoutAMetricsRecorder(t *testing.T) {
	mgr, definitions, _ := newTestManager(t)

	inst := types.InstanceSpec{ID: "i-1", Name: "web"}
	definitions.instances[inst.Name] = inst

	if err := mgr.Stop(context.Background(), inst); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
