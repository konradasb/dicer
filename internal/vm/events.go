// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"fmt"
	"strconv"
	"time"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/humanize"
	"github.com/konradasb/dicer/internal/types"
)

// Recorder records what happens to instances.
type Recorder interface {
	Record(e events.Event)
}

// discardRecorder is the Recorder used when none is configured.
type discardRecorder struct{}

func (discardRecorder) Record(events.Event) {}

// record records an event about instance.
func (m *Manager) record(instance types.InstanceSpec, action events.Action, message string, attrs map[string]string) {
	m.events.Record(events.Event{
		Kind:       events.KindInstance,
		ID:         instance.ID,
		Name:       instance.Name,
		Action:     action,
		Message:    message,
		Attributes: attrs,
	})
}

// recordEnd records how an instance ended and what its restart policy
// decided.
func (m *Manager) recordEnd(instance types.InstanceSpec, exit Exit, decision restartDecision, ranFor time.Duration) {
	attrs := map[string]string{}
	if exit.Code != nil {
		attrs["exit_code"] = strconv.Itoa(*exit.Code)
	}

	after := ""
	if ranFor > 0 {
		after = " after running for " + humanize.Duration(ranFor)
	}
	if exit.Clean() {
		m.record(instance, events.ActionExited, fmt.Sprintf("Instance exited with code %d%s", *exit.Code, after), attrs)
	} else {
		message := fmt.Sprintf("Instance failed%s: %v", after, exit.Failure)
		if decision.gaveUp {
			message += fmt.Sprintf("; restart policy %s gave up after %s", instance.Restart, humanize.Count(decision.restarts, "restart"))
		}
		m.record(instance, events.ActionDied, message, attrs)
	}

	if decision.restart {
		what := "failed"
		if exit.Clean() {
			what = "exited"
		}
		m.record(instance, events.ActionRestarting,
			fmt.Sprintf("Back-off restarting %s instance in %s (%s)", what, humanize.Duration(decision.delay), restartCount(instance.Restart, decision.restarts)),
			map[string]string{"delay": decision.delay.String(), "restart_count": strconv.Itoa(decision.restarts)})
	}
}

// restartCount describes restart n under policy: "restart 1 of 3, policy
// on-failure:3".
func restartCount(policy types.RestartPolicy, n int) string {
	count := strconv.Itoa(n)
	if policy.Mode == types.RestartModeOnFailure && policy.MaxRetries > 0 {
		count += " of " + strconv.Itoa(policy.MaxRetries)
	}
	return fmt.Sprintf("restart %s, policy %s", count, policy)
}
