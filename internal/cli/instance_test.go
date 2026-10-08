// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/konradasb/dicer"
)

// runBuild parses argv through a real create command and returns the create
// it would make, exercising the flag plumbing rather than bypassing it.
func runBuild(t *testing.T, argv ...string) (instanceCreate, error) {
	t.Helper()

	cmd := newInstanceCreateCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	if err := cmd.Flags().Parse(argv); err != nil {
		t.Fatalf("parse flags %v: %v", argv, err)
	}

	return buildCreate(cmd, cmd.Flags().Args())
}

func TestBuildCreateRequestRateLimits(t *testing.T) {
	got, err := runBuild(t, "web", "--disk-rate", "50MiB", "--disk-iops", "1000",
		"--upload-rate", "1MiB/s", "--download-rate", "2MiB")
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}
	if got.spec.DiskBytesPerSecond != 50<<20 || got.spec.DiskIOPS != 1000 ||
		got.spec.UploadBytesPerSecond != 1<<20 || got.spec.DownloadBytesPerSecond != 2<<20 {
		t.Errorf("spec = %+v, want the limits given", got.spec)
	}
}

func TestBuildCreateRequestFromFlagsOnly(t *testing.T) {
	got, err := runBuild(t,
		"web", "--image", "alpine:3.21", "--kernel", "k1",
		"--network", "default", "--vcpus", "2", "--memory", "1GiB", "--disk", "5GiB")
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}

	if got.spec.Name != "web" {
		t.Errorf("name = %q, want web", got.spec.Name)
	}
	if got.spec.VCPUs != 2 {
		t.Errorf("vcpus = %d, want 2", got.spec.VCPUs)
	}
	if got.spec.MemoryBytes != 1<<30 {
		t.Errorf("memory = %d, want %d", got.spec.MemoryBytes, 1<<30)
	}
	if got.spec.DiskBytes != 5<<30 {
		t.Errorf("disk = %d, want %d", got.spec.DiskBytes, 5<<30)
	}
}

func TestBuildCreateRequestRequiresName(t *testing.T) {
	_, err := runBuild(t, "--image", "alpine", "--kernel", "k", "--network", "default")
	if err == nil {
		t.Fatal("expected an error when no name is given")
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

func TestBuildCreateRequestStartFlag(t *testing.T) {
	got, err := runBuild(t, "web", "--image", "alpine", "--kernel", "k", "--network", "default", "--start")
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}
	if !got.opts.Start {
		t.Error("--start was not carried into the request")
	}

	got, err = runBuild(t, "web", "--image", "alpine", "--kernel", "k", "--network", "default")
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}
	if got.opts.Start {
		t.Error("start should default to false: create records, it does not boot")
	}
}

// TestBuildCreateRequestMountFlags checks each type of mount is parsed, and
// that a file mount carries the contents and mode of the file on this
// machine rather than its path.
func TestBuildCreateRequestMountFlags(t *testing.T) {
	appConf := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(appConf, []byte("k=v"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := runBuild(t, "web",
		"--mount", "type=tmpfs,target=/scratch",
		"--mount", "source=data,target=/var/lib/data,readonly",
		"--mount", "type=file,source="+appConf+",target=/etc/app.conf,ro")
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}

	want := []dicer.Mount{
		{Type: dicer.MountTypeTmpfs, Target: "/scratch"},
		{Type: dicer.MountTypeVolume, Source: "data", Target: "/var/lib/data", ReadOnly: true},
		{Type: dicer.MountTypeFile, Target: "/etc/app.conf", ReadOnly: true, Content: []byte("k=v"), Mode: 0o640},
	}
	if !reflect.DeepEqual(got.spec.Mounts, want) {
		t.Errorf("mounts = %+v, want %+v", got.spec.Mounts, want)
	}
}

func TestBuildCreateRequestRejectsBadMounts(t *testing.T) {
	for _, argv := range [][]string{
		{"--mount", "type=volume,source=data"},
		{"--mount", "type=volume,target=/x,color=red"},
		{"--mount", "type=file,target=/x"},
		{"--mount", "type=file,source=" + t.TempDir() + ",target=/x"},
		{"--mount", "type=file,source=/does/not/exist,target=/x"},
	} {
		if _, err := runBuild(t, append([]string{"web"}, argv...)...); err == nil {
			t.Errorf("%v should be rejected", argv)
		}
	}
}

func TestBuildCreateRequestCommandAfterDash(t *testing.T) {
	got, err := runBuild(t, "web", "--image", "alpine", "--", "sh", "-c", "echo 'hello world'")
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}

	want := []string{"sh", "-c", "echo 'hello world'"}
	if !slices.Equal(got.spec.Cmd, want) {
		t.Errorf("cmd = %q, want %q: arguments must pass through unsplit", got.spec.Cmd, want)
	}
	if got.spec.Name != "web" {
		t.Errorf("name = %q, want web", got.spec.Name)
	}
}

func TestBuildCreateRequestPublish(t *testing.T) {
	req, err := runBuild(t, "web", "-p", "8080:80", "--publish", "10.0.0.1:53:53/udp")
	if err != nil {
		t.Fatalf("buildCreate: %v", err)
	}

	got := req.spec.Ports
	if len(got) != 2 ||
		got[0].HostPort != 8080 || got[0].GuestPort != 80 || got[0].Protocol != "" ||
		got[1].HostIP != "10.0.0.1" || got[1].Protocol != dicer.ProtocolUDP {
		t.Errorf("ports = %v, want 8080:80 and 10.0.0.1:53:53/udp", got)
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
		{HostPort: 8080, GuestPort: 80},
		{HostIP: "10.0.0.1", HostPort: 53, GuestPort: 53, Protocol: dicer.ProtocolUDP},
	})
	if want := "8080->80/tcp, 10.0.0.1:53->53/udp"; got != want {
		t.Errorf("formatPorts = %q, want %q", got, want)
	}
}

func TestInitModeFlag(t *testing.T) {
	got, err := runBuild(t, "web", "--image", "debian", "--init-mode", "systemd")
	if err != nil {
		t.Fatal(err)
	}
	if got.spec.InitMode != dicer.InitModeSystemd {
		t.Errorf("--init-mode: init mode = %q, want systemd", got.spec.InitMode)
	}

	// Left out, it is the daemon's to default.
	if got, _ := runBuild(t, "web", "--image", "debian"); got.spec.InitMode != "" {
		t.Errorf("no --init-mode: init mode = %q, want it unset", got.spec.InitMode)
	}
}

func TestCommandLinesShowTheInitModeOnlyWhenChosen(t *testing.T) {
	for mode, want := range map[dicer.InitMode][]string{
		"":                    {"the image's"},
		dicer.InitModeAuto:    {"the image's"},
		dicer.InitModeSystemd: {"the image's", "in systemd mode"},
	} {
		got := commandLines(dicer.Instance{InstanceSpec: dicer.InstanceSpec{InitMode: mode}})
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
		t.Fatalf("buildCreate: %v", err)
	}
	if !got.spec.RemoveOnExit {
		t.Error("--rm was not carried into the definition")
	}

	if got, _ := runBuild(t, "web", "--image", "alpine"); got.spec.RemoveOnExit {
		t.Error("an instance that did not ask to be deleted says it did")
	}
}
