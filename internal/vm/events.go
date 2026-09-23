// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"fmt"
	"strconv"
	"time"

	"github.com/docker/go-units"

	"github.com/konradasb/dicer/internal/types"
)

// Events records what happens to instances.
type Events interface {
	Record(e types.Event)
}

// discardEvents is the Events used when none is configured.
type discardEvents struct{}

func (discardEvents) Record(types.Event) {}

// record records an event about inst.
func (m *Manager) record(inst types.InstanceSpec, action types.EventAction, message string, attrs map[string]string) {
	m.events.Record(types.Event{
		Kind:       types.KindInstance,
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
		after = " after running for " + duration(ranFor)
	}
	if exit.Clean() {
		m.record(inst, types.ActionExited, fmt.Sprintf("Instance exited with code %d%s", *exit.Code, after), attrs)
	} else {
		message := fmt.Sprintf("Instance failed%s: %v", after, exit.Failure)
		if d.gaveUp {
			message += fmt.Sprintf("; restart policy %s gave up after %s", inst.Restart, plural(d.restarts, "restart"))
		}
		m.record(inst, types.ActionDied, message, attrs)
	}

	if d.restart {
		what := "failed"
		if exit.Clean() {
			what = "exited"
		}
		m.record(inst, types.ActionRestarting,
			fmt.Sprintf("Back-off restarting %s instance in %s (%s)", what, duration(d.delay), restartCount(inst.Restart, d.restarts)),
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

// duration formats d, rounded to suit its magnitude.
func duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// binarySizeUnits are the IEC units sizes are written in.
var binarySizeUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// size formats n bytes: "512 MiB".
func size(n int64) string {
	return units.CustomSize("%.4g %s", float64(n), 1024, binarySizeUnits)
}

// vcpus formats a vCPU count: "2 vCPUs".
func vcpus(n int) string {
	return plural(n, "vCPU")
}

// plural formats n of what: "3 restarts".
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}
