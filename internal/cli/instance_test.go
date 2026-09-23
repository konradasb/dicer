// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dicer-sh/dicer"
)

// buildCreateRequest merges a YAML definition with command-line flags. The
// merge is guarded by flags.Changed so that cobra's defaults do not silently
// overwrite what the file said -- a wrong guard loses user input without any
// error, so the precedence rules are worth pinning down.

// runBuild parses argv through a real create command and returns the request
// it would send, exercising the flag plumbing rather than bypassing it.
func runBuild(t *testing.T, argv ...string) (dicer.InstanceSpec, error) {
	t.Helper()

	spec, _, err := runBuildStart(t, argv...)

	return spec, err
}

// runBuildStart is runBuild when the test is about --start.
func runBuildStart(t *testing.T, argv ...string) (dicer.InstanceSpec, bool, error) {
	t.Helper()

	cmd := newInstanceCreateCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	if err := cmd.Flags().Parse(argv); err != nil {
		t.Fatalf("parse flags %v: %v", argv, err)
	}

	return buildCreateRequest(cmd, cmd.Flags().Args())
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "vm.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	return path
}

const fullSpec = `
name: from-file
image: docker.io/library/alpine:3.21
kernel: k-from-file
vcpus: 4
memory: 2GiB
disk: 20GiB
network: net-from-file
hostname: host-from-file
restart: on-failure:5
`

func TestBuildCreateRequestFromFlagsOnly(t *testing.T) {
	got, err := runBuild(t,
		"web", "--image", "alpine:3.21", "--kernel", "k1",
		"--network", "default", "--vcpus", "2", "--memory", "1GiB", "--disk", "5GiB")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	if got.Name != "web" {
		t.Errorf("name = %q, want web", got.Name)
	}
	if got.VCPUs != 2 {
		t.Errorf("vcpus = %d, want 2", got.VCPUs)
	}
	if got.MemoryBytes != 1<<30 {
		t.Errorf("memory = %d, want %d", got.MemoryBytes, 1<<30)
	}
	if got.DiskBytes != 5<<30 {
		t.Errorf("disk = %d, want %d", got.DiskBytes, 5<<30)
	}
}

func TestBuildCreateRequestFromFileOnly(t *testing.T) {
	got, err := runBuild(t, "--file", writeSpec(t, fullSpec))
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"name", got.Name, "from-file"},
		{"image", got.ImageRef, "docker.io/library/alpine:3.21"},
		{"kernel", got.KernelName, "k-from-file"},
		{"network", got.NetworkName, "net-from-file"},
		{"hostname", got.Hostname, "host-from-file"},
		{"vcpus", got.VCPUs, 4},
		{"memory", got.MemoryBytes, int64(2) << 30},
		{"disk", got.DiskBytes, int64(20) << 30},
		{"restart mode", string(got.Restart.Mode), "on-failure"},
		{"restart retries", got.Restart.MaxRetries, 5},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
}

// TestBuildCreateRequestFileDefaultsSurviveFlags checks that flags left at
// their defaults do not override the file: cobra reports a default-valued
// flag as unset, and the merge must leave the file's value alone.
func TestBuildCreateRequestFileDefaultsSurviveFlags(t *testing.T) {
	got, err := runBuild(t, "--file", writeSpec(t, fullSpec))
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	// --vcpus defaults to 1, --memory to 512MiB, --disk to 10GiB. None were
	// given on the command line, so the file's 4 / 2GiB / 20GiB must stand.
	if got.VCPUs != 4 {
		t.Errorf("vcpus = %d, want 4 (cobra's default clobbered the file)", got.VCPUs)
	}
	if got.MemoryBytes != 2<<30 {
		t.Errorf("memory = %d, want %d (cobra's default clobbered the file)",
			got.MemoryBytes, 2<<30)
	}
	if got.DiskBytes != 20<<30 {
		t.Errorf("disk = %d, want %d (cobra's default clobbered the file)",
			got.DiskBytes, 20<<30)
	}
}

func TestBuildCreateRequestFlagsOverrideFile(t *testing.T) {
	got, err := runBuild(t,
		"--file", writeSpec(t, fullSpec),
		"--vcpus", "8", "--memory", "4GiB", "--kernel", "k-from-flag")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	if got.VCPUs != 8 {
		t.Errorf("vcpus = %d, want 8 from the flag", got.VCPUs)
	}
	if got.MemoryBytes != 4<<30 {
		t.Errorf("memory = %d, want %d from the flag", got.MemoryBytes, 4<<30)
	}
	if got.KernelName != "k-from-flag" {
		t.Errorf("kernel = %q, want k-from-flag", got.KernelName)
	}
	// Untouched fields still come from the file.
	if got.DiskBytes != 20<<30 {
		t.Errorf("disk = %d, want the file's value", got.DiskBytes)
	}
}

func TestBuildCreateRequestPositionalNameOverridesFile(t *testing.T) {
	got, err := runBuild(t, "renamed", "--file", writeSpec(t, fullSpec))
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	if got.Name != "renamed" {
		t.Errorf("name = %q, want the positional argument to win", got.Name)
	}
}

func TestBuildCreateRequestRequiresName(t *testing.T) {
	_, err := runBuild(t, "--image", "alpine", "--kernel", "k", "--network", "default")
	if err == nil {
		t.Fatal("expected an error when no name is given as argument or in a file")
	}
}

func TestBuildCreateRequestRejectsBadSizes(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{"memory", []string{"web", "--memory", "not-a-size"}},
		{"disk", []string{"web", "--disk", "not-a-size"}},
		{"disk below minimum", []string{"web", "--disk", "1KiB"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := runBuild(t, tt.argv...); err == nil {
				t.Errorf("expected an error for %v", tt.argv)
			}
		})
	}
}

func TestBuildCreateRequestMissingFile(t *testing.T) {
	if _, err := runBuild(t, "--file", "/nonexistent/vm.yaml"); err == nil {
		t.Error("expected an error for a missing definition file")
	}
}

func TestBuildCreateRequestMalformedFile(t *testing.T) {
	if _, err := runBuild(t, "--file", writeSpec(t, "{{{not yaml")); err == nil {
		t.Error("expected an error for a malformed definition file")
	}
}

func TestBuildCreateRequestStartFlag(t *testing.T) {
	_, start, err := runBuildStart(t, "web", "--image", "alpine", "--kernel", "k",
		"--network", "default", "--start")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if !start {
		t.Error("--start was not carried into the request")
	}

	_, start, err = runBuildStart(t, "web", "--image", "alpine", "--kernel", "k", "--network", "default")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if start {
		t.Error("start should default to false: create records, it does not boot")
	}
}

func TestParseVolumeFlags(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		wantErr bool
		mode    dicer.VolumeAccessMode
	}{
		{name: "mode left to the daemon", in: []string{"data:/var/lib/data"}, mode: ""},
		{name: "explicit mode", in: []string{"data:/var/lib/data:ReadOnlyMany"}, mode: "ReadOnlyMany"},
		{name: "missing mount path", in: []string{"data"}, wantErr: true},
		{name: "too many fields", in: []string{"a:b:c:d"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVolumeFlags(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %v", tt.in)
				}

				return
			}
			if err != nil {
				t.Fatalf("parseVolumeFlags(%v): %v", tt.in, err)
			}
			if len(got) != 1 || got[0].AccessMode != tt.mode {
				t.Errorf("got %+v, want one mount with mode %s", got, tt.mode)
			}
		})
	}
}

func TestParseFileFlags(t *testing.T) {
	got, err := parseFileFlags([]string{"db-password=/etc/dicer/db-password"})
	if err != nil {
		t.Fatalf("parseFileFlags: %v", err)
	}
	if len(got) != 1 || got[0].Name != "db-password" ||
		got[0].HostPath != "/etc/dicer/db-password" {
		t.Errorf("got %+v, want one mount name=path", got)
	}

	for _, bad := range []string{"no-equals-sign", "=/path", "name="} {
		if _, err := parseFileFlags([]string{bad}); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

func TestBuildCreateRequestCommandAfterDash(t *testing.T) {
	got, err := runBuild(t, "web", "--image", "alpine", "--", "sh", "-c", "echo 'hello world'")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	want := []string{"sh", "-c", "echo 'hello world'"}
	if !slices.Equal(got.Cmd, want) {
		t.Errorf("cmd = %q, want %q: arguments must pass through unsplit", got.Cmd, want)
	}
	if got.Name != "web" {
		t.Errorf("name = %q, want web", got.Name)
	}
}

func TestBuildCreateRequestFromStdin(t *testing.T) {
	cmd := newInstanceCreateCommand()
	cmd.SetIn(strings.NewReader(fullSpec))
	if err := cmd.Flags().Parse([]string{"--file", "-"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	got, _, err := buildCreateRequest(cmd, cmd.Flags().Args())
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if got.Name != "from-file" {
		t.Errorf("name = %q, want the name from stdin", got.Name)
	}
}

func TestBuildCreateRequestPublish(t *testing.T) {
	req, err := runBuild(t, "web", "-p", "8080:80", "--publish", "10.0.0.1:53:53/udp")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	got := req.Ports
	if len(got) != 2 ||
		got[0].HostPort != 8080 || got[0].GuestPort != 80 || got[0].Protocol != "" ||
		got[1].HostIP != "10.0.0.1" || got[1].Protocol != "udp" {
		t.Errorf("ports = %v, want 8080:80 and 10.0.0.1:53:53/udp", got)
	}
}

func TestBuildCreateRequestPortsFromFileAndFlags(t *testing.T) {
	path := writeSpec(t, fullSpec+"ports: [\"8080:80\", \"8443:443\"]\n")

	req, err := runBuild(t, "-f", path)
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if len(req.Ports) != 2 {
		t.Errorf("ports = %v, want the file's two", req.Ports)
	}

	// A flag replaces the file's list rather than adding to it, as for
	// every other list.
	req, err = runBuild(t, "-f", path, "-p", "9090:90")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if len(req.Ports) != 1 || req.Ports[0].HostPort != 9090 {
		t.Errorf("ports = %v, want only the flag's", req.Ports)
	}
}

func TestBuildCreateRequestRejectsBadPorts(t *testing.T) {
	for _, bad := range []string{"80", "a:80", "8080:0", "1:2:3:4"} {
		if _, err := runBuild(t, "web", "-p", bad); err == nil {
			t.Errorf("--publish %q should be rejected", bad)
		}
	}
}

func TestFormatPorts(t *testing.T) {
	got := formatPorts([]dicer.PortMapping{
		{HostPort: 8080, GuestPort: 80, Protocol: "tcp"},
		{HostIP: "10.0.0.1", HostPort: 53, GuestPort: 53, Protocol: "udp"},
	})
	if want := "8080->80/tcp, 10.0.0.1:53->53/udp"; got != want {
		t.Errorf("formatPorts = %q, want %q", got, want)
	}
}

func TestInitModeFlagAndSpec(t *testing.T) {
	got, err := runBuild(t, "web", "--image", "debian", "--init-mode", "systemd")
	if err != nil {
		t.Fatal(err)
	}
	if got.InitMode != dicer.ModeSystemd {
		t.Errorf("--init-mode: init mode = %q, want systemd", got.InitMode)
	}

	got, err = runBuild(t, "--file", writeSpec(t, "name: web\nimage: debian\ninit_mode: exec\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.InitMode != dicer.ModeExec {
		t.Errorf("init_mode: init mode = %q, want exec", got.InitMode)
	}

	// Left out, it is the daemon's to default.
	if got, _ := runBuild(t, "web", "--image", "debian"); got.InitMode != "" {
		t.Errorf("no --init-mode: init mode = %q, want it unset", got.InitMode)
	}
}

func TestCommandLinesShowTheInitModeOnlyWhenChosen(t *testing.T) {
	for mode, want := range map[dicer.InitMode][]string{
		"":                {"the image's"},
		dicer.ModeAuto:    {"the image's"},
		dicer.ModeSystemd: {"the image's", "in systemd mode"},
	} {
		got := commandLines(dicer.Instance{Spec: dicer.InstanceSpec{InitMode: mode}})
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("init mode %q: %q, want %q", mode, got, want)
		}
	}
}

// --rm is part of the instance's definition, not of the request that made
// it: the daemon does the deleting, so it must be recorded.
func TestBuildCreateRequestRemoveOnExit(t *testing.T) {
	got, err := runBuild(t, "web", "--image", "alpine", "--rm")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if !got.RemoveOnExit {
		t.Error("--rm was not carried into the definition")
	}

	if got, _ := runBuild(t, "web", "--image", "alpine"); got.RemoveOnExit {
		t.Error("an instance that did not ask to be deleted says it did")
	}

	// A definition file can say it too.
	got, err = runBuild(t, "--file", writeSpec(t, "name: web\nimage: alpine\nremove_on_exit: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.RemoveOnExit {
		t.Error("remove_on_exit in a file was not read")
	}
}
