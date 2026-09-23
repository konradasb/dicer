// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"time"

	"github.com/dicer-sh/dicer"
)

// The backoff a restart waits out, so that an instance that fails as soon as
// it starts does not take the host with it.
const (
	// restartBackoffBase is the first delay; each restart in a row doubles
	// it, up to restartBackoffMax.
	restartBackoffBase = time.Second
	restartBackoffMax  = 5 * time.Minute

	// restartBackoffReset is how long a run must last to count as having
	// worked: after one, the count starts again at zero, so a crash after a
	// long run waits a second and has its retry limit back.
	restartBackoffReset = 10 * time.Minute
)

// decision is what the restart policy decided about an instance that ended.
type decision struct {
	// restart is whether to start it again, after delay.
	restart bool
	delay   time.Duration

	// restarts is the restart count to record: the one this decision makes,
	// or the count as it was if it makes none.
	restarts int

	// gaveUp is whether an on-failure instance has used up its retries.
	gaveUp bool
}

// decide is what happens to an instance that ended: whether to start it
// again, when, and what its restart count becomes.
//
// It is a pure function of the policy, how the guest ended, the restarts in a
// row so far and how long the last run lasted -- no clock and no I/O -- so
// that it can be tested exhaustively on its own.
//
// Whether an unless-stopped instance was stopped by a user is not its
// business: that is desired state, and a user's stop takes the instance out
// of supervision altogether rather than ending it.
func decide(p dicer.RestartPolicy, exit Exit, restarts int, ranFor time.Duration) decision {
	// A run that lasted has earned a clean slate: the count starts again,
	// which resets both the backoff and the retry limit.
	if ranFor >= restartBackoffReset {
		restarts = 0
	}

	wanted := false
	switch p.Mode {
	case dicer.RestartAlways, dicer.RestartUnlessStopped:
		wanted = true
	case dicer.RestartOnFailure:
		wanted = !exit.Clean()
	case "", dicer.RestartNo:
	}

	if !wanted {
		return decision{restarts: restarts}
	}

	// A retry limit applies only to on-failure, where Validate is what
	// keeps it from being set on anything else.
	if p.MaxRetries > 0 && restarts >= p.MaxRetries {
		return decision{restarts: restarts, gaveUp: true}
	}

	return decision{restart: true, delay: backoff(restarts), restarts: restarts + 1}
}

// backoff is how long to wait before the restart after restarts of them in a
// row: the base doubled once per restart, capped.
func backoff(restarts int) time.Duration {
	delay := restartBackoffBase
	for range restarts {
		delay *= 2
		if delay >= restartBackoffMax {
			return restartBackoffMax
		}
	}

	return delay
}
