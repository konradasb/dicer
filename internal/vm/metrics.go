// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import "time"

// Metrics records how lifecycle operations went. It is declared here, and
// satisfied by internal/metrics, so that this package measures itself without
// depending on a metrics library.
type Metrics interface {
	// RecordInstanceOperation records one finished operation. The outcome
	// is taken from err, which is whatever the operation is returning.
	RecordInstanceOperation(operation string, err error, d time.Duration)

	// RecordInstanceRestart records an instance being started again by its
	// restart policy.
	RecordInstanceRestart()
}

// The operation names reported to Metrics. They are constants so that the
// label set stays bounded and a typo is a compile error rather than a second
// time series nobody graphs. Each is named for the Manager method it
// measures.
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

// discardMetrics is the Metrics used when none is configured, so that the
// lifecycle code can record unconditionally. It is the same treatment
// Config.Logger gets.
type discardMetrics struct{}

func (discardMetrics) RecordInstanceOperation(string, error, time.Duration) {}
func (discardMetrics) RecordInstanceRestart()                               {}

// observe records a finished lifecycle operation. It is called from a
// deferred closure over the operation's named error result, so that every
// return path is measured -- including the ones that fail before touching the
// host, which are the ones worth alerting on.
func (m *Manager) observe(operation string, started time.Time, err error) {
	m.metrics.RecordInstanceOperation(operation, err, time.Since(started))
}
