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

// Events records what happens to instances.
type Events interface {
	Record(e events.Event)
}

// discardEvents is the Events used when none is configured.
type discardEvents struct{}

func (discardEvents) Record(events.Event) {}

// record records an event about inst.
func (m *Manager) record(inst types.InstanceSpec, action events.Action, message string, attrs map[string]string) {
	m.events.Record(events.Event{
		Kind:       events.KindInstance,
		ID:         inst.ID,
		Name:       inst.Name,
		Action:     action,
		Message:    message,
		Attributes: attrs,
	})
}

// recordEnd records how an instance ended and what its restart policy
// decided.
func (m *Manager) recordEnd(inst types.InstanceSpec, exit Exit, d decision, ranFor time.Duration) {
	attrs := map[string]string{}
	if exit.Code != nil {
		attrs["exit_code"] = strconv.Itoa(*exit.Code)
	}

	after := ""
	if ranFor > 0 {
		after = " after running for " + humanize.Duration(ranFor)
	}
	if exit.Clean() {
		m.record(inst, events.ActionExited, fmt.Sprintf("Instance exited with code %d%s", *exit.Code, after), attrs)
	} else {
		message := fmt.Sprintf("Instance failed%s: %v", after, exit.Failure)
		if d.gaveUp {
			message += fmt.Sprintf("; restart policy %s gave up after %s", inst.Restart, humanize.Count(d.restarts, "restart"))
		}
		m.record(inst, events.ActionDied, message, attrs)
	}

	if d.restart {
		what := "failed"
		if exit.Clean() {
			what = "exited"
		}
		m.record(inst, events.ActionRestarting,
			fmt.Sprintf("Back-off restarting %s instance in %s (%s)", what, humanize.Duration(d.delay), restartCount(inst.Restart, d.restarts)),
			map[string]string{"delay": d.delay.String(), "restart_count": strconv.Itoa(d.restarts)})
	}
}

// restartCount describes restart n under policy p: "restart 1 of 3, policy
// on-failure:3".
func restartCount(p types.RestartPolicy, n int) string {
	count := strconv.Itoa(n)
	if p.Mode == types.RestartOnFailure && p.MaxRetries > 0 {
		count += " of " + strconv.Itoa(p.MaxRetries)
	}
	return fmt.Sprintf("restart %s, policy %s", count, p)
}
