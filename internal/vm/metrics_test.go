// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"context"
	"testing"

	"github.com/konradasb/dicer/internal/types"
)

func TestOperationsAreRecordedWithTheirOutcome(t *testing.T) {
	manager, definitions, _ := newTestManager(t)
	recorder := &fakeMetrics{}
	manager.metrics = recorder

	instance := types.InstanceSpec{ID: "i-1", Name: "web", VCPUs: 1, MemoryBytes: 1 << 30}
	definitions.instances[instance.Name] = instance

	// Stopping an already-stopped instance succeeds, and is still an
	// operation that happened.
	if err := manager.Stop(context.Background(), instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Pausing one that is not running fails before it touches the host,
	// which is exactly the kind of failure the counter should catch.
	if err := manager.Pause(context.Background(), instance); err == nil {
		t.Fatal("Pause on a stopped instance should fail")
	}

	want := []recordedOperation{
		{operation: operationStop, failed: false},
		{operation: operationPause, failed: true},
	}
	if len(recorder.operations) != len(want) {
		t.Fatalf("recorded %v, want %v", recorder.operations, want)
	}
	for i, operation := range want {
		if recorder.operations[i] != operation {
			t.Errorf("operation %d = %+v, want %+v", i, recorder.operations[i], operation)
		}
	}
}

// A Manager built without a recorder records into a discard, so the
// lifecycle code can call it unconditionally.
func TestOperationsWithoutAMetricsRecorder(t *testing.T) {
	manager, definitions, _ := newTestManager(t)

	instance := types.InstanceSpec{ID: "i-1", Name: "web"}
	definitions.instances[instance.Name] = instance

	if err := manager.Stop(context.Background(), instance); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
