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

	"github.com/konradasb/dicer"
	"github.com/konradasb/dicer/internal/humanize"
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
func writeInstanceDetails(w io.Writer, instances []dicer.Instance, recent map[string][]dicer.Event) error {
	p := paletteFor(w)
	for i, instance := range instances {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		view := instanceView(instance, recent[instance.ID], p)
		if err := view.write(w); err != nil {
			return err
		}
	}
	return nil
}

// instanceView is how inspect shows one instance.
func instanceView(instance dicer.Instance, recent []dicer.Event, p palette) *statusView {
	v := &statusView{
		headline: fmt.Sprintf("%s %s — %s",
			p.dot(string(instance.State)), p.bold(instance.Name), instance.ImageRef),
	}

	v.block(
		field{"Active", activeLines(instance, p)},
		field{"Health", healthLines(instance, p)},
		field{"Restart", oneLine(restartDetail(instance))},
		field{"Standby", oneLine(standbyDetail(instance))},
	)
	v.block(
		field{"Machine", machineLines(instance)},
		field{"Limits", oneLine(limitsDetail(instance))},
		field{"Network", networkLines(instance)},
		field{"Ports", portLines(instance.Ports)},
		field{"Mounts", mountLines(instance.Mounts)},
		field{"Env", pairLines(instance.Env)},
		field{"Labels", pairLines(instance.Labels)},
		field{"Command", commandLines(instance)},
	)

	updated := ""
	if u := instance.UpdateTime; !u.IsZero() && !u.Equal(instance.CreateTime) {
		updated = age(u)
	}
	v.block(
		field{"ID", oneLine(instance.ID)},
		field{"Created", oneLine(createdDetail(instance))},
		field{"Updated", oneLine(updated)},
	)
	v.block(field{"Events", eventLines(recent, p)})

	return v
}

// activeLines describes an instance's state like systemctl's Active line:
// "running since 09:53:20, 8 minutes ago", plus the error if any.
func activeLines(instance dicer.Instance, p palette) []string {
	state := instance.State
	active := p.status(string(state))

	started, finished, next := instance.StartTime, instance.FinishTime, instance.NextRestartTime
	switch {
	case state == dicer.InstanceStateRestarting:
		active += fmt.Sprintf(" (restart %d)", instance.RestartCount)
		if wait := time.Until(next); !next.IsZero() && wait >= time.Second {
			active += ", in " + units.HumanDuration(wait)
		}
	case !started.IsZero() && isActive(state):
		active += " since " + since(started)
	case instance.ExitCode != nil && !finished.IsZero():
		active += fmt.Sprintf(", exited (%d) %s", *instance.ExitCode, age(finished))
	}

	out := []string{active}
	if e := strings.TrimSpace(instance.StateError); e != "" {
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
func restartDetail(instance dicer.Instance) string {
	policy := restartPolicyName(instance.RestartPolicy)
	if n := instance.RestartCount; n > 0 {
		policy += ", restarted " + humanize.Count(n, "time") + " in a row"
	}

	return policy
}

// machineLines describes the virtual machine: what it is given, the
// hypervisor that runs it, and the kernel it boots.
func machineLines(instance dicer.Instance) []string {
	vcpus := humanize.Count(instance.VCPUs, "vCPU")
	if n := instance.MaxVCPUs; n > 0 {
		vcpus += fmt.Sprintf(" (up to %d)", n)
	}
	memory := humanize.Bytes(instance.MemoryBytes) + " memory"
	if n := instance.MaxMemoryBytes; n > 0 {
		memory += " (up to " + humanize.Bytes(n) + ")"
	}
	resources := vcpus + ", " + memory + ", " + humanize.Bytes(instance.DiskBytes) + " disk"

	hypervisor := string(instance.HypervisorType)
	if v := instance.HypervisorVersion; v != "" {
		hypervisor += " " + v
	}
	if pid := instance.HypervisorPID; pid > 0 {
		hypervisor += fmt.Sprintf(", pid %d", pid)
	}

	kernel := "kernel " + cmp.Or(instance.KernelName, "(the default)")
	if args := instance.KernelArgs; args != "" {
		kernel += ", args " + args
	}

	return []string{resources, hypervisor, kernel}
}

// standbyDetail describes when an instance is put on standby, if ever:
// "after 15m idle".
func standbyDetail(instance dicer.Instance) string {
	if d := instance.StandbyAfter; d > 0 {
		return "after " + humanize.Duration(d) + " idle"
	}
	return ""
}

// limitsDetail describes the rate limits an instance has, if any: "disk
// 50 MiB/s and 1000 IOPS each, upload 10 MiB/s".
func limitsDetail(instance dicer.Instance) string {
	var disk, limits []string
	if n := instance.DiskBytesPerSecond; n > 0 {
		disk = append(disk, humanize.Bytes(n)+"/s")
	}
	if n := instance.DiskIOPS; n > 0 {
		disk = append(disk, fmt.Sprintf("%d IOPS", n))
	}
	if len(disk) > 0 {
		limits = append(limits, "disk "+strings.Join(disk, " and ")+" each")
	}
	if n := instance.UploadBytesPerSecond; n > 0 {
		limits = append(limits, "upload "+humanize.Bytes(n)+"/s")
	}
	if n := instance.DownloadBytesPerSecond; n > 0 {
		limits = append(limits, "download "+humanize.Bytes(n)+"/s")
	}
	return strings.Join(limits, ", ")
}

// networkLines describes an instance's place on its network: its address,
// if it has one, the network and its hostname, and its MAC address.
func networkLines(instance dicer.Instance) []string {
	hostname := cmp.Or(instance.Hostname, instance.Name)
	where := fmt.Sprintf("%s (%s)", orDash(instance.NetworkName), hostname)

	var address string
	switch ip, static := instance.IP, instance.StaticIP; {
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
	if mac := instance.MAC; mac != "" {
		out = append(out, "MAC "+mac)
	}

	return out
}

// commandLines is what the instance runs -- its own command, or the
// image's -- and how, if it is not left to the guest to decide.
func commandLines(instance dicer.Instance) []string {
	command := "the image's"
	if len(instance.Cmd) > 0 {
		command = shellJoin(instance.Cmd)
	}

	out := []string{command}
	if mode := instance.InitMode; mode != "" && mode != dicer.InitModeAuto {
		out = append(out, "in "+string(mode)+" mode")
	}

	return out
}

// createdDetail is when the instance was defined: "2026-09-21 20:18:17, 14
// hours ago".
func createdDetail(instance dicer.Instance) string {
	created := instance.CreateTime
	if created.IsZero() {
		return ""
	}

	return created.Local().Format(time.DateTime) + ", " + age(created)
}

// portLines are an instance's published ports, one a line.
func portLines(ports []dicer.PortMapping) []string {
	lines := make([]string, 0, len(ports))
	for _, p := range ports {
		lines = append(lines, formatPorts([]dicer.PortMapping{p}))
	}

	return lines
}

// mountLines describe an instance's mounts, one a line: "volume data on
// /var/lib/data (read-only)", "file on /etc/app.conf (12 B, mode 0640)".
func mountLines(mounts []dicer.Mount) []string {
	lines := make([]string, 0, len(mounts))
	for _, m := range mounts {
		line := string(m.Type)
		if m.Source != "" {
			line += " " + m.Source
		}
		line += " on " + m.Target

		var notes []string
		if m.Type == dicer.MountTypeFile {
			// A file given no mode gets 0644 in the guest.
			mode := fmt.Sprintf("mode %04o", uint32(cmp.Or(m.Mode, 0o644)))
			notes = append(notes, humanize.Bytes(int64(len(m.Content))), mode)
		}
		if m.ReadOnly {
			notes = append(notes, "read-only")
		}
		if len(notes) > 0 {
			line += " (" + strings.Join(notes, ", ") + ")"
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
