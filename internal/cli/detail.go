// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/docker/go-units"
	"gopkg.in/yaml.v3"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/cli/printer"
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
	for i, inst := range instances {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		view := instanceView(inst, recent[inst.Spec.ID], p)
		if err := view.write(w); err != nil {
			return err
		}
	}
	return nil
}

// instanceView is how inspect shows one instance: how it is doing first,
// then what it is, then when it was made, and last what has happened to it
// lately.
func instanceView(inst dicer.Instance, recent []dicer.Event, p palette) *statusView {
	v := &statusView{
		headline: fmt.Sprintf("%s %s — %s",
			p.dot(inst.Status.State.Lower()), p.bold(inst.Spec.Name), inst.Spec.ImageRef),
	}

	v.block(
		field{"Active", activeLines(inst, p)},
		field{"Health", healthLines(inst, p)},
		field{"Restart", oneLine(restartDetail(inst))},
	)
	v.block(
		field{"Machine", machineLines(inst)},
		field{"Network", networkLines(inst)},
		field{"Ports", portLines(inst.Spec.Ports)},
		field{"Volumes", volumeLines(inst.Spec.VolumeMounts)},
		field{"Files", fileLines(inst.Spec.Files)},
		field{"Env", pairLines(inst.Spec.Env)},
		field{"Labels", pairLines(inst.Spec.Labels)},
		field{"Command", commandLines(inst)},
	)

	updated := ""
	if u := inst.Spec.UpdatedAt; !u.IsZero() && !u.Equal(inst.Spec.CreatedAt) {
		updated = age(u)
	}
	v.block(
		field{"ID", oneLine(inst.Spec.ID)},
		field{"Created", oneLine(createdDetail(inst))},
		field{"Updated", oneLine(updated)},
	)
	v.block(field{"Events", eventLines(recent, p)})

	return v
}

// activeLines describes an instance's state as systemctl's Active line does,
// with since when, and on a line of its own why it is not running if it
// should be: "running since 09:53:20, 8 minutes ago".
func activeLines(inst dicer.Instance, p palette) []string {
	status := inst.Status
	active := p.status(status.State.Lower())

	switch {
	case status.State == dicer.StateRestarting:
		active += fmt.Sprintf(" (restart %d)", status.RestartCount)
		if wait := time.Until(status.NextRestartAt); !status.NextRestartAt.IsZero() && wait >= time.Second {
			active += ", in " + units.HumanDuration(wait)
		}
	case !status.StartedAt.IsZero() && status.State.IsActive():
		active += " since " + since(status.StartedAt)
	case status.ExitCode != nil && !status.FinishedAt.IsZero():
		active += fmt.Sprintf(", exited (%d) %s", *status.ExitCode, age(status.FinishedAt))
	}

	out := []string{active}
	if e := strings.TrimSpace(status.StateError); e != "" {
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
func restartDetail(inst dicer.Instance) string {
	policy := inst.Spec.Restart.String()
	if n := inst.Status.RestartCount; n > 0 {
		policy += ", restarted " + plural(int64(n), "time") + " in a row"
	}

	return policy
}

// machineLines describes the virtual machine: what it is given, the
// hypervisor that runs it, and the kernel it boots.
func machineLines(inst dicer.Instance) []string {
	spec := inst.Spec
	resources := fmt.Sprintf("%s, %s memory, %s disk",
		plural(int64(spec.VCPUs), "vCPU"), size(spec.MemoryBytes), size(spec.DiskBytes))

	hv := string(spec.Hypervisor())
	if v := cmp.Or(inst.Status.HypervisorVersion, spec.HypervisorVersion); v != "" {
		hv += " " + v
	}
	if pid := inst.Status.HypervisorPID; pid != nil && *pid > 0 {
		hv += fmt.Sprintf(", pid %d", *pid)
	}

	kernel := "kernel " + cmp.Or(spec.KernelName, "(the default)")
	if spec.KernelArgs != "" {
		kernel += ", args " + spec.KernelArgs
	}

	return []string{resources, hv, kernel}
}

// networkLines describes an instance's place on its network: its address,
// if it has one, the network and its hostname, and its MAC address.
func networkLines(inst dicer.Instance) []string {
	hostname := cmp.Or(inst.Spec.Hostname, inst.Spec.Name)
	where := fmt.Sprintf("%s (%s)", orDash(inst.Spec.NetworkName), hostname)

	var address string
	switch ip, static := inst.Status.IP, inst.Spec.StaticIP; {
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
	if mac := inst.Status.MAC; mac != "" {
		out = append(out, "MAC "+mac)
	}

	return out
}

// commandLines is what the instance runs -- its own command, or the
// image's -- and how, if it is not left to the guest to decide.
func commandLines(inst dicer.Instance) []string {
	command := "the image's"
	if len(inst.Spec.Cmd) > 0 {
		command = shellJoin(inst.Spec.Cmd)
	}

	out := []string{command}
	if mode := inst.Spec.InitMode; mode != "" && mode != dicer.ModeAuto {
		out = append(out, "in "+string(mode)+" mode")
	}

	return out
}

// createdDetail is when the instance was defined: "2026-09-21 20:18:17, 14
// hours ago".
func createdDetail(inst dicer.Instance) string {
	created := inst.Spec.CreatedAt
	if created.IsZero() {
		return ""
	}

	return created.Local().Format(time.DateTime) + ", " + age(created)
}

func portLines(ports []dicer.PortMapping) []string {
	lines := make([]string, 0, len(ports))
	for _, p := range ports {
		lines = append(lines, formatPorts([]dicer.PortMapping{p}))
	}

	return lines
}

func volumeLines(volumes []dicer.VolumeMount) []string {
	lines := make([]string, 0, len(volumes))
	for _, v := range volumes {
		line := v.VolumeName + " on " + v.MountPath
		if v.AccessMode != "" {
			line += " (" + string(v.AccessMode) + ")"
		}
		lines = append(lines, line)
	}

	return lines
}

func fileLines(files []dicer.FileMount) []string {
	lines := make([]string, 0, len(files))
	for _, f := range files {
		lines = append(lines, "/run/secrets/"+f.Name+" from "+f.HostPath)
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

// writeRecords writes the daemon's whole record of each value, as a JSON or
// YAML array.
//
// The values are marshalled through JSON even when YAML is asked for, so that
// both name their fields the same way: the JSON tags on the domain types are
// the one spelling of the API, and yaml.Marshal would otherwise lower-case
// the Go field names instead.
func writeRecords[T any](w io.Writer, format string, values []T) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}

	var records any
	if err := json.Unmarshal(data, &records); err != nil {
		return err
	}

	return writeStructured(w, format, records)
}

// writeStructured writes v as JSON or YAML, as format asks.
func writeStructured(w io.Writer, format string, v any) error {
	if !printer.IsStructured(format) {
		return fmt.Errorf("unsupported format %q: want table, json or yaml", format)
	}

	if printer.IsYAML(format) {
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(v); err != nil {
			return err
		}
		return enc.Close()
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
