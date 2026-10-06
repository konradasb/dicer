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
	operationStart           = "start"
	operationStop            = "stop"
	operationPause           = "pause"
	operationResume          = "resume"
	operationStandby         = "standby"
	operationResize          = "resize"
	operationDelete          = "delete"
	operationCreateSnapshot  = "create_snapshot"
	operationRestoreSnapshot = "restore_snapshot"
	operationDeleteSnapshot  = "delete_snapshot"
	operationForkSnapshot    = "fork_snapshot"
	operationForkInstance    = "fork_instance"
)

// discardMetrics is the Metrics used when none is configured.
type discardMetrics struct{}

func (discardMetrics) RecordInstanceOperation(string, error, time.Duration) {}
func (discardMetrics) RecordInstanceRestart()                               {}

// observeOperation records a finished lifecycle operation. Call it deferred,
// over the operation's named error result.
func (m *Manager) observeOperation(operation string, started time.Time, err error) {
	m.metrics.RecordInstanceOperation(operation, err, time.Since(started))
}
