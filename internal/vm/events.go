// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"fmt"
	"strconv"
	"time"

	"github.com/docker/go-units"

	"github.com/dicer-sh/dicer"
)

// Events records what happens to instances. It is declared here, and
// satisfied by internal/events, so that the lifecycle reports what it does
// without knowing who listens.
type Events interface {
	Record(e dicer.Event)
}

// discardEvents is the Events used when none is configured.
type discardEvents struct{}

func (discardEvents) Record(dicer.Event) {}

// record records that action happened to inst, with a line saying what, and
// attributes for a program.
func (m *Manager) record(inst dicer.InstanceSpec, action dicer.EventAction, message string, attrs map[string]string) {
	m.events.Record(dicer.Event{
		Kind:       dicer.KindInstance,
		ID:         inst.ID,
		Name:       inst.Name,
		Action:     action,
		Message:    message,
		Attributes: attrs,
	})
}

// recordEnd records how an instance's guest ended, after running for
// ranFor, and what its restart policy made of it.
func (m *Manager) recordEnd(inst dicer.InstanceSpec, exit Exit, d decision, ranFor time.Duration) {
	attrs := map[string]string{}
	if exit.Code != nil {
		attrs["exit_code"] = strconv.Itoa(*exit.Code)
	}

	after := ""
	if ranFor > 0 {
		after = " after running for " + duration(ranFor)
	}
	if exit.Clean() {
		m.record(inst, dicer.ActionExited, fmt.Sprintf("dicer.InstanceSpec exited with code %d%s", *exit.Code, after), attrs)
	} else {
		message := fmt.Sprintf("dicer.InstanceSpec failed%s: %v", after, exit.Failure)
		if d.gaveUp {
			message += fmt.Sprintf("; restart policy %s gave up after %s", inst.Restart, plural(d.restarts, "restart"))
		}
		m.record(inst, dicer.ActionDied, message, attrs)
	}

	if d.restart {
		what := "failed"
		if exit.Clean() {
			what = "exited"
		}
		m.record(inst, dicer.ActionRestarting,
			fmt.Sprintf("Back-off restarting %s instance in %s (%s)", what, duration(d.delay), restartCount(inst.Restart, d.restarts)),
			map[string]string{"delay": d.delay.String(), "restart_count": strconv.Itoa(d.restarts)})
	}
}

// restartCount says which restart in a row n is, under policy p: "restart
// 1 of 3, policy on-failure:3".
func restartCount(p dicer.RestartPolicy, n int) string {
	count := strconv.Itoa(n)
	if p.Mode == dicer.RestartOnFailure && p.MaxRetries > 0 {
		count += " of " + strconv.Itoa(p.MaxRetries)
	}
	return fmt.Sprintf("restart %s, policy %s", count, p)
}

// duration is d as an event says it: to the millisecond under a second, to
// a tenth of a second under a minute, to the second after.
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

// binarySizeUnits are the units sizes are written in: the binary ones they
// are given in, as --memory 512MiB.
var binarySizeUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}

// size is a size in bytes in the binary units sizes are given in: 512 MiB.
func size(n int64) string {
	return units.CustomSize("%.4g %s", float64(n), 1024, binarySizeUnits)
}

// vcpus is a count of vCPUs: "1 vCPU", "2 vCPUs".
func vcpus(n int) string {
	return plural(n, "vCPU")
}

// plural is n of what: "1 restart", "3 restarts".
func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}
