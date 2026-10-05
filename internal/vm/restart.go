// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package vm

import (
	"time"

	"github.com/konradasb/dicer/internal/types"
)

// Restart backoff: the delay starts at restartBackoffBase and doubles with
// each restart in a row, up to restartBackoffMax. A run lasting
// restartBackoffReset resets the count.
const (
	restartBackoffBase  = time.Second
	restartBackoffMax   = 5 * time.Minute
	restartBackoffReset = 10 * time.Minute
)

// restartDecision is what the restart policy decided about an instance that
// ended.
type restartDecision struct {
	// restart is whether to start it again, after delay.
	restart bool
	delay   time.Duration

	// restarts is the restart count to record.
	restarts int

	// gaveUp is whether an on-failure instance has used up its retries.
	gaveUp bool
}

// decideRestart applies a restart policy to an instance that ended after
// ranFor with restarts restarts in a row so far.
func decideRestart(policy types.RestartPolicy, exit Exit, restarts int, ranFor time.Duration) restartDecision {
	if ranFor >= restartBackoffReset {
		restarts = 0
	}

	wanted := false
	switch policy.Mode {
	case types.RestartModeAlways, types.RestartModeUnlessStopped:
		wanted = true
	case types.RestartModeOnFailure:
		wanted = !exit.Clean()
	case "", types.RestartModeNo:
	}

	if !wanted {
		return restartDecision{restarts: restarts}
	}

	// Validate allows MaxRetries only for on-failure.
	if policy.MaxRetries > 0 && restarts >= policy.MaxRetries {
		return restartDecision{restarts: restarts, gaveUp: true}
	}

	return restartDecision{restart: true, delay: backoff(restarts), restarts: restarts + 1}
}

// backoff returns the delay before the next restart.
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
