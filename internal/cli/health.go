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
	"google.golang.org/protobuf/types/known/durationpb"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// healthCheckFlags is a health check as the flags give it.
type healthCheckFlags struct {
	// Cmd is run by /bin/sh in the guest, as docker run's --health-cmd is.
	Cmd string
	// HTTP is PORT[/path], got on the guest's loopback address.
	HTTP string
	// TCP is a port on the guest's loopback address.
	TCP int

	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int32

	// Disabled switches health checking off, the image's included.
	Disabled bool
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
// none were given. It replaces any existing check whole.
func healthCheckFromFlags(cmd *cobra.Command) (*dicerdv1.HealthCheck, error) {
	flags := cmd.Flags()
	if !slices.ContainsFunc(healthFlags, flags.Changed) {
		return nil, nil //nolint:nilnil // no health flag given is not an error
	}

	var f healthCheckFlags
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
func (f healthCheckFlags) check() (*dicerdv1.HealthCheck, error) {
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
		return &dicerdv1.HealthCheck{Disabled: true}, nil
	}
	if probes != 1 {
		return nil, errors.New("a health check needs exactly one of --health-cmd, --health-http and --health-tcp")
	}

	c := &dicerdv1.HealthCheck{
		Interval:    duration(f.Interval),
		Timeout:     duration(f.Timeout),
		StartPeriod: duration(f.StartPeriod),
		Retries:     f.Retries,
	}
	switch {
	case f.Cmd != "":
		c.Probe = &dicerdv1.HealthCheck_Exec{Exec: &dicerdv1.HealthCheckExec{Command: []string{"/bin/sh", "-c", f.Cmd}}}
	case f.HTTP != "":
		port, path, err := parseHTTPTarget(f.HTTP)
		if err != nil {
			return nil, err
		}
		c.Probe = &dicerdv1.HealthCheck_Http{Http: &dicerdv1.HealthCheckHTTP{Port: port, Path: path}}
	default:
		c.Probe = &dicerdv1.HealthCheck_Tcp{Tcp: &dicerdv1.HealthCheckTCP{Port: uint32(f.TCP)}}
	}

	return c, nil
}

// parseHTTPTarget parses PORT[/path]: "3000/api/health", "8080".
func parseHTTPTarget(s string) (uint32, string, error) {
	portText, path, _ := strings.Cut(s, "/")
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return 0, "", fmt.Errorf("invalid HTTP health check %q: want PORT[/path]", s)
	}
	if path != "" {
		path = "/" + path
	}

	return uint32(port), path, nil
}

// healthSuffix returns a status suffix like " (healthy)", or "".
func healthSuffix(inst *dicerdv1.Instance) string {
	switch status := inst.GetHealth().GetStatus(); status {
	case dicerdv1.HealthStatus_HEALTH_STATUS_UNSPECIFIED:
		return ""
	case healthStarting:
		return " (health: starting)"
	default:
		return " (" + enumName(status) + ")"
	}
}

// healthCheckLines describes a health check: the probe and how often it
// runs, then its other timing -- "http :3000/api/health every 10s",
// "timeout 5s, 3 retries". Unset timings are left out, to be taken as the
// defaults.
func healthCheckLines(c *dicerdv1.HealthCheck) []string {
	if c == nil {
		return nil
	}
	if c.GetDisabled() {
		return []string{"disabled"}
	}

	var probe string
	switch p := c.GetProbe().(type) {
	case *dicerdv1.HealthCheck_Exec:
		command := p.Exec.GetCommand()
		if len(command) == 3 && command[0] == "/bin/sh" && command[1] == "-c" {
			command = command[2:]
		}
		probe = "exec " + shellJoin(command)
	case *dicerdv1.HealthCheck_Http:
		probe = fmt.Sprintf("http :%d%s", p.Http.GetPort(), cmp.Or(p.Http.GetPath(), "/"))
	case *dicerdv1.HealthCheck_Tcp:
		probe = fmt.Sprintf("tcp :%d", p.Tcp.GetPort())
	default:
		return nil
	}
	if d := c.GetInterval().AsDuration(); d != 0 {
		probe += " every " + d.String()
	}

	var timing []string
	if d := c.GetTimeout().AsDuration(); d != 0 {
		timing = append(timing, "timeout "+d.String())
	}
	if d := c.GetStartPeriod().AsDuration(); d != 0 {
		timing = append(timing, "start period "+d.String())
	}
	switch n := c.GetRetries(); n {
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

// healthLines describes an instance's health and check for inspect.
func healthLines(inst *dicerdv1.Instance, p palette) []string {
	h := inst.GetHealth()
	if h == nil {
		return healthCheckLines(inst.GetHealthCheck())
	}

	verdict := p.status(enumName(h.GetStatus()))
	if last := timeOf(h.GetLastCheckTime()); !last.IsZero() {
		verdict += ", checked " + age(last)
	}
	if n := h.GetFailingStreak(); n > 0 {
		verdict += fmt.Sprintf(", %s failed in a row", plural(int64(n), "check"))
	}

	out := []string{verdict}
	if last := firstLine(h.GetLastOutput()); last != "" && h.GetStatus() != healthHealthy {
		out = append(out, "last probe: "+last)
	}

	return append(out, healthCheckLines(h.GetCheck())...)
}

// duration returns d as the API takes it: unset for zero.
func duration(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}
