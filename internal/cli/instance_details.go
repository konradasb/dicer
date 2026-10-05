// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"cmp"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/docker/go-units"

	"github.com/konradasb/dicer/internal/humanize"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// writeInstanceDetails writes instances as 'dicer inspect' shows them, a
// blank line between each, laid out as systemctl status lays out a unit:
//
//	● grafana — docker.io/grafana/grafana:latest
//
//	     Active: running since 09:53:20, 8 minutes ago
//	     Health: healthy, checked 6 seconds ago
//	             http :3000/api/health every 10s
//	    Restart: always
//
//	    Machine: 1 vCPU, 4 GiB memory, 10 GiB disk
//	             cloud-hypervisor v49.0.0, pid 102322
//	    ...
func writeInstanceDetails(w io.Writer, instances []*dicerdv1.Instance, recent map[string][]*dicerdv1.Event) error {
	p := paletteFor(w)
	for i, instance := range instances {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		view := instanceView(instance, recent[instance.GetId()], p)
		if err := view.write(w); err != nil {
			return err
		}
	}
	return nil
}

// instanceView is how inspect shows one instance.
func instanceView(instance *dicerdv1.Instance, recent []*dicerdv1.Event, p palette) *statusView {
	v := &statusView{
		headline: fmt.Sprintf("%s %s — %s",
			p.dot(enumName(instance.GetState())), p.bold(instance.GetName()), instance.GetImageRef()),
	}

	v.block(
		field{"Active", activeLines(instance, p)},
		field{"Health", healthLines(instance, p)},
		field{"Restart", oneLine(restartDetail(instance))},
	)
	v.block(
		field{"Machine", machineLines(instance)},
		field{"Network", networkLines(instance)},
		field{"Ports", portLines(instance.GetPorts())},
		field{"Mounts", mountLines(instance.GetMounts())},
		field{"Env", pairLines(instance.GetEnv())},
		field{"Labels", pairLines(instance.GetLabels())},
		field{"Command", commandLines(instance)},
	)

	updated := ""
	if u := timeOf(instance.GetUpdateTime()); !u.IsZero() && !u.Equal(timeOf(instance.GetCreateTime())) {
		updated = age(u)
	}
	v.block(
		field{"ID", oneLine(instance.GetId())},
		field{"Created", oneLine(createdDetail(instance))},
		field{"Updated", oneLine(updated)},
	)
	v.block(field{"Events", eventLines(recent, p)})

	return v
}

// activeLines describes an instance's state like systemctl's Active line:
// "running since 09:53:20, 8 minutes ago", plus the error if any.
func activeLines(instance *dicerdv1.Instance, p palette) []string {
	state := instance.GetState()
	active := p.status(enumName(state))

	started, finished, next := timeOf(instance.GetStartTime()), timeOf(instance.GetFinishTime()), timeOf(instance.GetNextRestartTime())
	switch {
	case state == stateRestarting:
		active += fmt.Sprintf(" (restart %d)", instance.GetRestartCount())
		if wait := time.Until(next); !next.IsZero() && wait >= time.Second {
			active += ", in " + units.HumanDuration(wait)
		}
	case !started.IsZero() && isActive(state):
		active += " since " + since(started)
	case instance.ExitCode != nil && !finished.IsZero():
		active += fmt.Sprintf(", exited (%d) %s", instance.GetExitCode(), age(finished))
	}

	out := []string{active}
	if e := strings.TrimSpace(instance.GetStateError()); e != "" {
		out = append(out, strings.ReplaceAll(e, "\n", " "))
	}

	return out
}

// since says when something began as systemctl does: the time alone if it
// was today, the date too if not, and how long ago.
func since(t time.Time) string {
	t = t.Local()
	layout := time.DateTime
	if y, m, d := t.Date(); time.Now().Year() == y && time.Now().Month() == m && time.Now().Day() == d {
		layout = time.TimeOnly
	}
	return t.Format(layout) + ", " + units.HumanDuration(time.Since(t)) + " ago"
}

// restartDetail describes an instance's restart policy, and how many
// restarts in a row it has had: "on-failure:5, restarted 2 times in a row".
func restartDetail(instance *dicerdv1.Instance) string {
	policy := restartPolicyName(instance.GetRestartPolicy())
	if n := instance.GetRestartCount(); n > 0 {
		policy += ", restarted " + humanize.Count(n, "time") + " in a row"
	}

	return policy
}

// machineLines describes the virtual machine: what it is given, the
// hypervisor that runs it, and the kernel it boots.
func machineLines(instance *dicerdv1.Instance) []string {
	resources := fmt.Sprintf("%s, %s memory, %s disk",
		humanize.Count(instance.GetVcpus(), "vCPU"), humanize.Bytes(instance.GetMemoryBytes()), humanize.Bytes(instance.GetDiskBytes()))

	hypervisor := enumName(instance.GetHypervisorType())
	if v := instance.GetHypervisorVersion(); v != "" {
		hypervisor += " " + v
	}
	if pid := instance.GetHypervisorPid(); pid > 0 {
		hypervisor += fmt.Sprintf(", pid %d", pid)
	}

	kernel := "kernel " + cmp.Or(instance.GetKernelName(), "(the default)")
	if args := instance.GetKernelArgs(); args != "" {
		kernel += ", args " + args
	}

	return []string{resources, hypervisor, kernel}
}

// networkLines describes an instance's place on its network: its address,
// if it has one, the network and its hostname, and its MAC address.
func networkLines(instance *dicerdv1.Instance) []string {
	hostname := cmp.Or(instance.GetHostname(), instance.GetName())
	where := fmt.Sprintf("%s (%s)", orDash(instance.GetNetworkName()), hostname)

	var address string
	switch ip, static := instance.GetIp(), instance.GetStaticIp(); {
	case ip != "" && static != "":
		address = ip + " (static) on " + where
	case ip != "":
		address = ip + " on " + where
	case static != "":
		address = static + " (static, when running) on " + where
	default:
		address = where
	}

	out := []string{address}
	if mac := instance.GetMac(); mac != "" {
		out = append(out, "MAC "+mac)
	}

	return out
}

// commandLines is what the instance runs -- its own command, or the
// image's -- and how, if it is not left to the guest to decide.
func commandLines(instance *dicerdv1.Instance) []string {
	command := "the image's"
	if len(instance.GetCmd()) > 0 {
		command = shellJoin(instance.GetCmd())
	}

	out := []string{command}
	if mode := instance.GetInitMode(); mode != dicerdv1.InitMode_INIT_MODE_UNSPECIFIED && mode != dicerdv1.InitMode_INIT_MODE_AUTO {
		out = append(out, "in "+enumName(mode)+" mode")
	}

	return out
}

// createdDetail is when the instance was defined: "2026-09-21 20:18:17, 14
// hours ago".
func createdDetail(instance *dicerdv1.Instance) string {
	created := timeOf(instance.GetCreateTime())
	if created.IsZero() {
		return ""
	}

	return created.Local().Format(time.DateTime) + ", " + age(created)
}

// portLines are an instance's published ports, one a line.
func portLines(ports []*dicerdv1.PortMapping) []string {
	lines := make([]string, 0, len(ports))
	for _, p := range ports {
		lines = append(lines, formatPorts([]*dicerdv1.PortMapping{p}))
	}

	return lines
}

// mountLines describe an instance's mounts, one a line: "volume data on
// /var/lib/data (read-only)".
func mountLines(mounts []*dicerdv1.Mount) []string {
	lines := make([]string, 0, len(mounts))
	for _, m := range mounts {
		line := enumName(m.GetType())
		if m.GetSource() != "" {
			line += " " + m.GetSource()
		}
		line += " on " + m.GetTarget()
		if m.GetReadOnly() {
			line += " (read-only)"
		}
		lines = append(lines, line)
	}

	return lines
}

// pairLines renders a map as KEY=VALUE lines, sorted by key.
func pairLines(m map[string]string) []string {
	lines := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		lines = append(lines, k+"="+m[k])
	}
	return lines
}

// shellJoin renders a command so it could be pasted into a shell: arguments
// with spaces or quotes in them are single-quoted.
func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		if a != "" && !strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~") {
			quoted = append(quoted, a)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(quoted, " ")
}
