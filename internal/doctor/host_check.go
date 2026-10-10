// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package doctor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/konradasb/dicer/internal/humanize"
)

// lowDisk is the free space under which the data directory's disk is
// warned about: an image or two, and the overlay disks guests fill.
const lowDisk = 2 << 30

// hostResults checks the host, in order, and returns what each check found.
func (d *Doctor) hostResults(ctx context.Context) []Result {
	results := []Result{d.kvm(ctx), d.ipForwarding(), d.firewall(ctx), d.tools(), d.uplink(), d.disk()}
	for i := range results {
		results[i].Group = GroupHost
	}
	return results
}

// kvm checks that /dev/kvm can be used, and says whether the host is itself
// a virtual machine, which needs nested virtualisation for it.
func (d *Doctor) kvm(ctx context.Context) Result {
	r := Result{Name: "kvm"}
	vm := d.virtualMachine(ctx)

	if _, err := os.Stat(d.kvmPath); err != nil {
		r.Status, r.Detail = StatusFailed, d.kvmPath+" does not exist"
		r.Hint = d.missingKVMHint(vm)
		return r
	}
	if err := d.openRDWR(d.kvmPath); err != nil {
		r.Status, r.Detail = StatusFailed, fmt.Sprintf("%s cannot be opened: %v", d.kvmPath, err)
		r.Hint = "check that the daemon runs as root, and that nothing else holds KVM exclusively"
		return r
	}

	r.Status, r.Detail = StatusOK, d.kvmPath+" is usable"
	if vm != "" {
		r.Detail += fmt.Sprintf(", in a virtual machine (%s) with nested virtualisation", vm)
	}
	return r
}

// missingKVMHint says how to get KVM on a host without it, which is a
// virtual machine of the kind vm names, or none if vm is empty.
func (d *Doctor) missingKVMHint(vm string) string {
	switch {
	case vm == "apple":
		return "this is a virtual machine on a Mac, which has KVM only on Apple M3 or later, " +
			"with macOS 15 or later, and nested virtualisation turned on"
	case vm != "":
		return fmt.Sprintf("this is a virtual machine (%s) without nested virtualisation: "+
			"turn it on where the machine is defined, or run Dicer on bare metal", vm)
	case d.goarch == "amd64":
		return "turn virtualisation (VT-x or AMD-V) on in the firmware, then load the kvm_intel or kvm_amd module"
	default:
		return "load the kvm module; if it does not load, the CPU or firmware does not offer virtualisation"
	}
}

// virtualMachine returns the kind of virtual machine the host is, as
// systemd-detect-virt names it, or "" for bare metal or if it cannot tell.
func (d *Doctor) virtualMachine(ctx context.Context) string {
	out, err := d.run(ctx, "systemd-detect-virt", "--vm")
	if vm := strings.TrimSpace(out); err == nil && vm != "none" {
		return vm
	}
	return ""
}

// ipForwarding checks that the host forwards IPv4, which guests' traffic
// to anywhere but the host needs.
func (d *Doctor) ipForwarding() Result {
	r := Result{Name: "ip_forwarding"}

	data, err := d.readFile(d.ipForwardPath)
	switch {
	case err != nil:
		r.Status, r.Detail = StatusFailed, fmt.Sprintf("cannot read %s: %v", d.ipForwardPath, err)
	case strings.TrimSpace(string(data)) != "1":
		r.Status, r.Detail = StatusFailed, "IPv4 forwarding is off, so guests cannot reach anything but the host"
		r.Hint = "turn it on with sysctl -w net.ipv4.ip_forward=1, and keep it on with a file in /etc/sysctl.d"
	default:
		r.Status, r.Detail = StatusOK, "IPv4 forwarding is on"
	}
	return r
}

// firewall checks that iptables works, and that firewalld, where it runs,
// has the dicer zone.
func (d *Doctor) firewall(ctx context.Context) Result {
	r := Result{Name: "firewall"}

	version, err := d.run(ctx, "iptables", "--version")
	if err != nil {
		r.Status, r.Detail = StatusFailed, "iptables cannot be run: "+firstLine(version, err)
		r.Hint = "install iptables: the daemon sets guests' NAT and isolation up with it"
		return r
	}
	if out, err := d.run(ctx, "iptables", "-w", "-n", "-L", "FORWARD"); err != nil {
		r.Status, r.Detail = StatusFailed, "iptables cannot read the rules: "+firstLine(out, err)
		r.Hint = "check that the daemon runs as root, and that the kernel has its netfilter modules"
		return r
	}
	r.Status, r.Detail = StatusOK, "iptables works"
	if backend := iptablesBackend(version); backend != "" {
		r.Detail = fmt.Sprintf("iptables (%s) works", backend)
	}

	if state, err := d.run(ctx, "firewall-cmd", "--state"); err != nil || strings.TrimSpace(state) != "running" {
		return r
	}
	if _, err := d.run(ctx, "firewall-cmd", "--info-zone=dicer"); err != nil {
		r.Status = StatusWarning
		r.Detail += ", but firewalld runs without the dicer zone, so it may drop guests' traffic"
		r.Hint = "install the zone, as the package does: copy build/package/firewalld-zone.xml " +
			"to /etc/firewalld/zones/dicer.xml, then run firewall-cmd --reload"
		return r
	}
	r.Detail += ", and firewalld has the dicer zone"
	return r
}

// iptablesBackend returns the backend iptables --version names, such as
// nf_tables, or "".
func iptablesBackend(version string) string {
	_, rest, ok := strings.Cut(version, "(")
	if !ok {
		return ""
	}
	backend, _, ok := strings.Cut(rest, ")")
	if !ok {
		return ""
	}
	return backend
}

// tools checks that the programs the daemon runs to build guests' disks
// are installed.
func (d *Doctor) tools() Result {
	r := Result{Name: "tools"}

	var missing, packages []string
	for _, tool := range []struct{ name, pkg string }{
		{"mkfs.erofs", "erofs-utils"},
		{"mke2fs", "e2fsprogs"},
	} {
		if _, err := d.lookPath(tool.name); err != nil {
			missing = append(missing, tool.name)
			packages = append(packages, tool.pkg)
		}
	}

	if len(missing) > 0 {
		r.Status, r.Detail = StatusFailed, strings.Join(missing, " and ")+" cannot be found, so no image can be converted"
		r.Hint = "install " + strings.Join(packages, " and ")
		return r
	}
	r.Status, r.Detail = StatusOK, "mkfs.erofs and mke2fs are installed"
	return r
}

// uplink checks that guests' traffic has an interface to leave by: the one
// the configuration names, or the default route's.
func (d *Doctor) uplink() Result {
	r := Result{Name: "uplink"}

	names, err := d.interfaces()
	if err != nil {
		r.Status, r.Detail = StatusFailed, "cannot list the host's interfaces: "+err.Error()
		return r
	}

	if name := d.cfg.UplinkInterface; name != "" {
		if !slices.Contains(names, name) {
			r.Status = StatusFailed
			r.Detail = fmt.Sprintf("network.uplink_interface names %s, which this host does not have", name)
			r.Hint = "set it to one of " + strings.Join(names, ", ") + ", or leave it unset to use the default route's"
			return r
		}
		r.Status, r.Detail = StatusOK, name+", as network.uplink_interface names"
		return r
	}

	name, err := d.defaultRouteInterface()
	if err != nil || name == "" {
		r.Status, r.Detail = StatusFailed, "the host has no default route, so guests' traffic has nowhere to leave by"
		r.Hint = "give the host a default route, or name the interface in network.uplink_interface"
		return r
	}
	r.Status, r.Detail = StatusOK, name+", by the default route"
	return r
}

// defaultRouteInterface returns the interface of the IPv4 default route,
// from the kernel's routing table, or "" if there is none.
func (d *Doctor) defaultRouteInterface() (string, error) {
	data, err := d.readFile(d.routesPath)
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Scan() // the header
	for scanner.Scan() {
		// Iface, Destination, Gateway, Flags, RefCnt, Use, Metric, Mask, ...
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 8 && fields[1] == "00000000" && fields[7] == "00000000" {
			return fields[0], nil
		}
	}
	return "", scanner.Err()
}

// disk checks how much the data directory's disk can still take.
func (d *Doctor) disk() Result {
	r := Result{Name: "disk"}

	free, err := d.freeBytes(d.cfg.DataDir)
	if err != nil {
		r.Status, r.Detail = StatusFailed, fmt.Sprintf("cannot read %s's disk: %v", d.cfg.DataDir, err)
		return r
	}

	r.Detail = fmt.Sprintf("%s free in %s", humanize.Bytes(free), d.cfg.DataDir)
	if free < lowDisk {
		r.Status = StatusWarning
		r.Hint = "free some space: dicer image prune removes images no instance uses"
		return r
	}
	r.Status = StatusOK
	return r
}

// firstLine returns the first line of out, or of err if out is empty.
func firstLine(out string, err error) string {
	if line, _, _ := strings.Cut(strings.TrimSpace(out), "\n"); line != "" {
		return line
	}
	return err.Error()
}

// openRDWR opens path for reading and writing, and closes it.
func openRDWR(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// run runs a program and returns what it printed.
func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// interfaceNames returns the names of the host's network interfaces.
func interfaceNames() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		names = append(names, iface.Name)
	}
	return names, nil
}
