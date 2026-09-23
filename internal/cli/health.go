// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dicer-sh/dicer"
)

// healthCheckFile is a health check as a spec file's healthcheck: block, and
// the flags, give it. Exactly one of Cmd, HTTP and TCP says how to probe,
// unless Disabled.
type healthCheckFile struct {
	// Cmd is run by /bin/sh in the guest, as docker run's --health-cmd is.
	Cmd string `yaml:"cmd,omitempty"`
	// HTTP is PORT[/path], got on the guest's loopback address.
	HTTP string `yaml:"http,omitempty"`
	// TCP is a port on the guest's loopback address.
	TCP int `yaml:"tcp,omitempty"`

	Interval    time.Duration `yaml:"interval,omitempty"`
	Timeout     time.Duration `yaml:"timeout,omitempty"`
	StartPeriod time.Duration `yaml:"start_period,omitempty"`
	Retries     int32         `yaml:"retries,omitempty"`

	// Disabled switches health checking off, the image's included.
	Disabled bool `yaml:"disabled,omitempty"`
}

// healthFlags are the flags that set a health check: docker run's, and
// --health-http and --health-tcp for images with no shell to run a command.
var healthFlags = []string{
	"health-cmd", "health-http", "health-tcp",
	"health-interval", "health-timeout", "health-start-period", "health-retries",
	"no-healthcheck",
}

func addHealthFlags(flags *pflag.FlagSet) {
	flags.String("health-cmd", "", "Command to check health with, run by /bin/sh in the guest")
	flags.String("health-http", "", "Check health with an HTTP GET in the guest, as PORT[/path]; 2xx or 3xx is healthy")
	flags.Int("health-tcp", 0, "Check health by connecting to a TCP port in the guest")
	flags.Duration("health-interval", 0, "Time between health checks (default 10s)")
	flags.Duration("health-timeout", 0, "Time a health check may take (default 5s)")
	flags.Duration("health-start-period", 0, "Time after a start in which failed checks do not count (default none)")
	flags.Int32("health-retries", 0, "Failed checks in a row that make the instance unhealthy (default 3)")
	flags.Bool("no-healthcheck", false, "Check no health, not even as the image says to")
}

// healthCheckFromFlags returns the health check the flags give, or nil if
// no health flag was given. A check given at all is given whole: the flags
// replace any check a spec file or the instance had, rather than editing it.
func healthCheckFromFlags(cmd *cobra.Command) (*dicer.HealthCheck, error) {
	flags := cmd.Flags()
	if !slices.ContainsFunc(healthFlags, flags.Changed) {
		return nil, nil //nolint:nilnil // no health flag given is not an error
	}

	var f healthCheckFile
	f.Cmd, _ = flags.GetString("health-cmd")
	f.HTTP, _ = flags.GetString("health-http")
	f.TCP, _ = flags.GetInt("health-tcp")
	f.Interval, _ = flags.GetDuration("health-interval")
	f.Timeout, _ = flags.GetDuration("health-timeout")
	f.StartPeriod, _ = flags.GetDuration("health-start-period")
	f.Retries, _ = flags.GetInt32("health-retries")
	f.Disabled, _ = flags.GetBool("no-healthcheck")

	return f.check()
}

// check converts what was given into a health check, checking what the
// daemon cannot: that it is said in one way. The daemon checks the rest.
func (f healthCheckFile) check() (*dicer.HealthCheck, error) {
	probes := 0
	for _, set := range []bool{f.Cmd != "", f.HTTP != "", f.TCP != 0} {
		if set {
			probes++
		}
	}

	if f.Disabled {
		if probes > 0 || f.Interval != 0 || f.Timeout != 0 || f.StartPeriod != 0 || f.Retries != 0 {
			return nil, errors.New("--no-healthcheck cannot be given with other health check settings")
		}
		return &dicer.HealthCheck{Disabled: true}, nil
	}
	if probes != 1 {
		return nil, errors.New("a health check needs exactly one of --health-cmd, --health-http and --health-tcp")
	}

	c := &dicer.HealthCheck{
		Interval:    f.Interval,
		Timeout:     f.Timeout,
		StartPeriod: f.StartPeriod,
		Retries:     int(f.Retries),
	}
	switch {
	case f.Cmd != "":
		c.Exec = []string{"/bin/sh", "-c", f.Cmd}
	case f.HTTP != "":
		port, path, err := parseHTTPTarget(f.HTTP)
		if err != nil {
			return nil, err
		}
		c.HTTP = &dicer.HTTPProbe{Port: port, Path: path}
	default:
		c.TCP = &dicer.TCPProbe{Port: f.TCP}
	}

	return c, nil
}

// parseHTTPTarget parses PORT[/path]: "3000/api/health", "8080".
func parseHTTPTarget(s string) (int, string, error) {
	portText, path, _ := strings.Cut(s, "/")
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return 0, "", fmt.Errorf("invalid HTTP health check %q: want PORT[/path]", s)
	}
	if path != "" {
		path = "/" + path
	}

	return int(port), path, nil
}

// healthSuffix is what docker ps adds to an instance's status for its
// health: " (healthy)", " (health: starting)". Nothing if it is not being
// checked.
func healthSuffix(inst dicer.Instance) string {
	if inst.Status.Health == nil {
		return ""
	}

	switch status := inst.Status.Health.Status; status {
	case "":
		return ""
	case dicer.HealthStarting:
		return " (health: starting)"
	default:
		return " (" + string(status) + ")"
	}
}

// healthCheckLines describes a health check: the probe and how often it
// runs, then its other timing -- "http :3000/api/health every 10s",
// "timeout 5s, 3 retries". Unset timings are left out, to be taken as the
// defaults.
func healthCheckLines(c *dicer.HealthCheck) []string {
	if c == nil {
		return nil
	}
	if c.Disabled {
		return []string{"disabled"}
	}

	var probe string
	switch {
	case len(c.Exec) > 0:
		command := c.Exec
		if len(command) == 3 && command[0] == "/bin/sh" && command[1] == "-c" {
			command = command[2:]
		}
		probe = "exec " + shellJoin(command)
	case c.HTTP != nil:
		probe = fmt.Sprintf("http :%d%s", c.HTTP.Port, cmp.Or(c.HTTP.Path, "/"))
	case c.TCP != nil:
		probe = fmt.Sprintf("tcp :%d", c.TCP.Port)
	default:
		return nil
	}
	if c.Interval != 0 {
		probe += " every " + c.Interval.String()
	}

	var timing []string
	if c.Timeout != 0 {
		timing = append(timing, "timeout "+c.Timeout.String())
	}
	if c.StartPeriod != 0 {
		timing = append(timing, "start period "+c.StartPeriod.String())
	}
	switch n := c.Retries; n {
	case 0:
	case 1:
		timing = append(timing, "1 retry")
	default:
		timing = append(timing, fmt.Sprintf("%d retries", n))
	}

	if len(timing) == 0 {
		return []string{probe}
	}
	return []string{probe, strings.Join(timing, ", ")}
}

// healthLines describes an instance's health for inspect: what its check has
// found and what the last probe said if it was not healthy, then the check,
// if it is being checked; else the check it is configured with. None if it
// has no check of its own and is not being checked.
func healthLines(inst dicer.Instance, p palette) []string {
	h := inst.Status.Health
	if h == nil {
		return healthCheckLines(inst.Spec.HealthCheck)
	}

	verdict := p.status(string(h.Status))
	if !h.LastCheck.IsZero() {
		verdict += ", checked " + age(h.LastCheck)
	}
	if n := h.FailingStreak; n > 0 {
		verdict += fmt.Sprintf(", %s failed in a row", plural(int64(n), "check"))
	}

	out := []string{verdict}
	if last := firstLine(h.LastOutput); last != "" && h.Status != dicer.HealthHealthy {
		out = append(out, "last probe: "+last)
	}

	return append(out, healthCheckLines(inst.Status.HealthCheck)...)
}
