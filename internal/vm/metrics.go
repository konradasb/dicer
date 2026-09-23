// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "time"

// Metrics records lifecycle operations.
type Metrics interface {
	// RecordInstanceOperation records one finished operation and its error.
	RecordInstanceOperation(operation string, err error, d time.Duration)

	// RecordInstanceRestart records an instance being started again by its
	// restart policy.
	RecordInstanceRestart()
}

// The operation names reported to Metrics.
const (
	opStart           = "start"
	opStop            = "stop"
	opPause           = "pause"
	opResume          = "resume"
	opDelete          = "delete"
	opCreateSnapshot  = "create_snapshot"
	opRestoreSnapshot = "restore_snapshot"
	opDeleteSnapshot  = "delete_snapshot"
)

// discardMetrics is the Metrics used when none is configured.
type discardMetrics struct{}

func (discardMetrics) RecordInstanceOperation(string, error, time.Duration) {}
func (discardMetrics) RecordInstanceRestart()                               {}

// observe records a finished lifecycle operation. Call it deferred, over the
// operation's named error result.
func (m *Manager) observe(operation string, started time.Time, err error) {
	m.metrics.RecordInstanceOperation(operation, err, time.Since(started))
}
