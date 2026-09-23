// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"cmp"
	"fmt"
	"strings"
	"time"
)

// The defaults an unset health check timing takes.
const (
	DefaultHealthInterval = 10 * time.Second
	DefaultHealthTimeout  = 5 * time.Second
	DefaultHealthRetries  = 3
)

// HealthCheck is how an instance's health is checked: one probe, run inside
// the guest by its agent, and when to run it.
type HealthCheck struct {
	// Exactly one probe is set, unless the check is Disabled.
	Exec []string   `yaml:"exec,omitempty" json:"exec,omitempty"`
	HTTP *HTTPProbe `yaml:"http,omitempty" json:"http,omitempty"`
	TCP  *TCPProbe  `yaml:"tcp,omitempty" json:"tcp,omitempty"`

	// Interval is the time between the end of one probe and the start of
	// the next. Timeout bounds one probe; one that overruns has failed.
	Interval time.Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	// StartPeriod is how long after a start failures do not count, for a
	// workload still coming up. A success in it counts at once. Zero means
	// none.
	StartPeriod time.Duration `yaml:"start_period,omitempty" json:"start_period,omitempty"`

	// Retries is how many failures in a row make the workload unhealthy.
	Retries int `yaml:"retries,omitempty" json:"retries,omitempty"`

	// Disabled switches health checking off, the image's included.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// HTTPProbe checks health by asking for a URL inside the guest, which is
// healthy if it answers 2xx or 3xx.
type HTTPProbe struct {
	Port int    `yaml:"port" json:"port"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

// TCPProbe checks health by opening a connection inside the guest, which is
// healthy if it is accepted.
type TCPProbe struct {
	Port int `yaml:"port" json:"port"`
}

// Validate reports whether the check is one the agent can run.
func (c HealthCheck) Validate() error {
	if c.Disabled {
		return nil
	}

	probes := 0
	if len(c.Exec) > 0 {
		probes++
	}
	if c.HTTP != nil {
		probes++
		if err := validHealthPort(c.HTTP.Port); err != nil {
			return err
		}
		if c.HTTP.Path != "" && !strings.HasPrefix(c.HTTP.Path, "/") {
			return InvalidArgument("health check path %q must begin with /", c.HTTP.Path)
		}
	}
	if c.TCP != nil {
		probes++
		if err := validHealthPort(c.TCP.Port); err != nil {
			return err
		}
	}
	if probes != 1 {
		return InvalidArgument("a health check needs exactly one probe: a command, an HTTP port or a TCP port")
	}

	switch {
	case c.Interval < 0, c.Timeout < 0, c.StartPeriod < 0:
		return InvalidArgument("health check durations cannot be negative")
	case c.Retries < 0:
		return InvalidArgument("health check retries cannot be negative")
	}

	return nil
}

// WithDefaults returns the check with its unset timings filled in.
func (c HealthCheck) WithDefaults() HealthCheck {
	if c.Interval == 0 {
		c.Interval = DefaultHealthInterval
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultHealthTimeout
	}
	if c.Retries == 0 {
		c.Retries = DefaultHealthRetries
	}

	return c
}

func validHealthPort(port int) error {
	if port < 1 || port > 65535 {
		return InvalidArgument("health check port %d is not between 1 and 65535", port)
	}

	return nil
}

// String describes the probe: "exec pg_isready", "http :3000/api/health",
// "tcp :5432".
func (c HealthCheck) String() string {
	switch {
	case c.Disabled:
		return "disabled"
	case len(c.Exec) > 0:
		return "exec " + strings.Join(c.Exec, " ")
	case c.HTTP != nil:
		return fmt.Sprintf("http :%d%s", c.HTTP.Port, cmp.Or(c.HTTP.Path, "/"))
	case c.TCP != nil:
		return fmt.Sprintf("tcp :%d", c.TCP.Port)
	default:
		return "none"
	}
}

// EffectiveHealthCheck returns the check an instance is run with: its own,
// if it has one; none, if its own is Disabled; else its image's. Nil means
// none.
func EffectiveHealthCheck(instance, image *HealthCheck) *HealthCheck {
	chosen := instance
	if chosen == nil {
		chosen = image
	}
	if chosen == nil || chosen.Disabled {
		return nil
	}

	c := chosen.WithDefaults()

	return &c
}

// HealthStatus is what an instance's health check has found.
type HealthStatus string

const (
	// HealthStarting means the check has not yet reached a verdict: the
	// workload is still in its start period, or has not been probed.
	HealthStarting HealthStatus = "starting"

	// HealthHealthy means the last probe passed.
	HealthHealthy HealthStatus = "healthy"

	// HealthUnhealthy means the check's retries have failed in a row.
	HealthUnhealthy HealthStatus = "unhealthy"
)

// HealthStatuses returns every health status.
func HealthStatuses() []HealthStatus {
	return []HealthStatus{HealthStarting, HealthHealthy, HealthUnhealthy}
}

// Health is what the health check of a running instance has found.
type Health struct {
	Status HealthStatus `json:"status"`

	// FailingStreak is how many probes in a row have failed, not counting
	// failures in the start period.
	FailingStreak int `json:"failing_streak,omitempty"`

	// LastCheck is when the last probe finished, and LastOutput what it
	// said.
	LastCheck  time.Time `json:"last_check,omitzero"`
	LastOutput string    `json:"last_output,omitempty"`
}

// NewHealth returns the health of a check that has not yet reached a
// verdict.
func NewHealth() Health { return Health{Status: HealthStarting} }
