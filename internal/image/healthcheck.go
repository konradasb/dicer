// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package image

import (
	"fmt"

	gcr "github.com/google/go-containerregistry/pkg/v1"

	"github.com/dicer-sh/dicer"
)

// healthCheckFromDocker converts the HEALTHCHECK an image declares. Nil means
// the image declares none.
//
// Docker's test is ["CMD", argv...], ["CMD-SHELL", command], or ["NONE"],
// which disables a check a base image declared. Its unset timings take
// Dicer's defaults rather than Docker's, like any other check.
func healthCheckFromDocker(hc *gcr.HealthConfig) (*dicer.HealthCheck, error) {
	if hc == nil || len(hc.Test) == 0 {
		return nil, nil //nolint:nilnil // an image without a HEALTHCHECK is not an error
	}

	c := &dicer.HealthCheck{
		Interval:    hc.Interval,
		Timeout:     hc.Timeout,
		StartPeriod: hc.StartPeriod,
		Retries:     hc.Retries,
	}

	switch kind, args := hc.Test[0], hc.Test[1:]; kind {
	case "NONE":
		return &dicer.HealthCheck{Disabled: true}, nil
	case "CMD":
		c.Exec = args
	case "CMD-SHELL":
		if len(args) != 1 {
			return nil, fmt.Errorf("HEALTHCHECK CMD-SHELL takes one command, not %d", len(args))
		}
		c.Exec = []string{"/bin/sh", "-c", args[0]}
	default:
		return nil, fmt.Errorf("unsupported HEALTHCHECK test %q", kind)
	}

	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("HEALTHCHECK: %w", err)
	}

	return c, nil
}
