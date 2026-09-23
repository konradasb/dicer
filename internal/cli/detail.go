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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	"github.com/konradasb/dicer/internal/cli/printer"
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
	for i, inst := range instances {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		view := instanceView(inst, recent[inst.GetId()], p)
		if err := view.write(w); err != nil {
			return err
		}
	}
	return nil
}

// instanceView is how inspect shows one instance.
func instanceView(inst *dicerdv1.Instance, recent []*dicerdv1.Event, p palette) *statusView {
	v := &statusView{
		headline: fmt.Sprintf("%s %s — %s",
			p.dot(enumName(inst.GetState())), p.bold(inst.GetName()), inst.GetImageRef()),
	}

	v.block(
		field{"Active", activeLines(inst, p)},
		field{"Health", healthLines(inst, p)},
		field{"Restart", oneLine(restartDetail(inst))},
	)
	v.block(
		field{"Machine", machineLines(inst)},
		field{"Network", networkLines(inst)},
		field{"Ports", portLines(inst.GetPorts())},
		field{"Mounts", mountLines(inst.GetMounts())},
		field{"Env", pairLines(inst.GetEnv())},
		field{"Labels", pairLines(inst.GetLabels())},
		field{"Command", commandLines(inst)},
	)

	updated := ""
	if u := timeOf(inst.GetUpdateTime()); !u.IsZero() && !u.Equal(timeOf(inst.GetCreateTime())) {
		updated = age(u)
	}
	v.block(
		field{"ID", oneLine(inst.GetId())},
		field{"Created", oneLine(createdDetail(inst))},
		field{"Updated", oneLine(updated)},
	)
	v.block(field{"Events", eventLines(recent, p)})

	return v
}

// activeLines describes an instance's state like systemctl's Active line:
// "running since 09:53:20, 8 minutes ago", plus the error if any.
func activeLines(inst *dicerdv1.Instance, p palette) []string {
	state := inst.GetState()
	active := p.status(enumName(state))

	started, finished, next := timeOf(inst.GetStartTime()), timeOf(inst.GetFinishTime()), timeOf(inst.GetNextRestartTime())
	switch {
	case state == stateRestarting:
		active += fmt.Sprintf(" (restart %d)", inst.GetRestartCount())
		if wait := time.Until(next); !next.IsZero() && wait >= time.Second {
			active += ", in " + units.HumanDuration(wait)
		}
	case !started.IsZero() && isActive(state):
		active += " since " + since(started)
	case inst.ExitCode != nil && !finished.IsZero():
		active += fmt.Sprintf(", exited (%d) %s", inst.GetExitCode(), age(finished))
	}

	out := []string{active}
	if e := strings.TrimSpace(inst.GetStateError()); e != "" {
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
func restartDetail(inst *dicerdv1.Instance) string {
	policy := restartPolicyName(inst.GetRestartPolicy())
	if n := inst.GetRestartCount(); n > 0 {
		policy += ", restarted " + plural(int64(n), "time") + " in a row"
	}

	return policy
}

// machineLines describes the virtual machine: what it is given, the
// hypervisor that runs it, and the kernel it boots.
func machineLines(inst *dicerdv1.Instance) []string {
	resources := fmt.Sprintf("%s, %s memory, %s disk",
		plural(int64(inst.GetVcpus()), "vCPU"), size(inst.GetMemoryBytes()), size(inst.GetDiskBytes()))

	hv := enumName(inst.GetHypervisorType())
	if v := inst.GetHypervisorVersion(); v != "" {
		hv += " " + v
	}
	if pid := inst.GetHypervisorPid(); pid > 0 {
		hv += fmt.Sprintf(", pid %d", pid)
	}

	kernel := "kernel " + cmp.Or(inst.GetKernelName(), "(the default)")
	if args := inst.GetKernelArgs(); args != "" {
		kernel += ", args " + args
	}

	return []string{resources, hv, kernel}
}

// networkLines describes an instance's place on its network: its address,
// if it has one, the network and its hostname, and its MAC address.
func networkLines(inst *dicerdv1.Instance) []string {
	hostname := cmp.Or(inst.GetHostname(), inst.GetName())
	where := fmt.Sprintf("%s (%s)", orDash(inst.GetNetworkName()), hostname)

	var address string
	switch ip, static := inst.GetIp(), inst.GetStaticIp(); {
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
	if mac := inst.GetMac(); mac != "" {
		out = append(out, "MAC "+mac)
	}

	return out
}

// commandLines is what the instance runs -- its own command, or the
// image's -- and how, if it is not left to the guest to decide.
func commandLines(inst *dicerdv1.Instance) []string {
	command := "the image's"
	if len(inst.GetCmd()) > 0 {
		command = shellJoin(inst.GetCmd())
	}

	out := []string{command}
	if mode := inst.GetInitMode(); mode != dicerdv1.InitMode_INIT_MODE_UNSPECIFIED && mode != dicerdv1.InitMode_INIT_MODE_AUTO {
		out = append(out, "in "+enumName(mode)+" mode")
	}

	return out
}

// createdDetail is when the instance was defined: "2026-09-21 20:18:17, 14
// hours ago".
func createdDetail(inst *dicerdv1.Instance) string {
	created := timeOf(inst.GetCreateTime())
	if created.IsZero() {
		return ""
	}

	return created.Local().Format(time.DateTime) + ", " + age(created)
}

func portLines(ports []*dicerdv1.PortMapping) []string {
	lines := make([]string, 0, len(ports))
	for _, p := range ports {
		lines = append(lines, formatPorts([]*dicerdv1.PortMapping{p}))
	}

	return lines
}

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

// writeRecords writes messages as a JSON or YAML array, each as record
// renders it.
func writeRecords[M proto.Message](w io.Writer, format string, messages []M) error {
	records := make([]any, 0, len(messages))
	for _, m := range messages {
		r, err := record(m)
		if err != nil {
			return err
		}
		records = append(records, r)
	}

	return writeStructured(w, format, records)
}

// record returns a message as JSON and YAML output show it: its fields as
// the API names them, and its enums by name.
func record(m proto.Message) (any, error) {
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return nil, err
	}

	var r any
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return r, nil
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
