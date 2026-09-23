// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// buildCreateRequest merges a YAML definition with command-line flags. The
// merge is guarded by flags.Changed so that cobra's defaults do not silently
// overwrite what the file said -- a wrong guard loses user input without any
// error, so the precedence rules are worth pinning down.

// runBuild parses argv through a real create command and returns the request
// it would send, exercising the flag plumbing rather than bypassing it.
func runBuild(t *testing.T, argv ...string) (*dicerdv1.CreateInstanceRequest, error) {
	t.Helper()

	cmd := newInstanceCreateCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	if err := cmd.Flags().Parse(argv); err != nil {
		t.Fatalf("parse flags %v: %v", argv, err)
	}

	return buildCreateRequest(cmd, cmd.Flags().Args())
}

func TestBuildCreateRequestFromFlagsOnly(t *testing.T) {
	got, err := runBuild(t,
		"web", "--image", "alpine:3.21", "--kernel", "k1",
		"--network", "default", "--vcpus", "2", "--memory", "1GiB", "--disk", "5GiB")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	if got.GetName() != "web" {
		t.Errorf("name = %q, want web", got.GetName())
	}
	if got.GetVcpus() != 2 {
		t.Errorf("vcpus = %d, want 2", got.GetVcpus())
	}
	if got.GetMemoryBytes() != 1<<30 {
		t.Errorf("memory = %d, want %d", got.GetMemoryBytes(), 1<<30)
	}
	if got.GetDiskBytes() != 5<<30 {
		t.Errorf("disk = %d, want %d", got.GetDiskBytes(), 5<<30)
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
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if !got.GetStart() {
		t.Error("--start was not carried into the request")
	}

	got, err = runBuild(t, "web", "--image", "alpine", "--kernel", "k", "--network", "default")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}
	if got.GetStart() {
		t.Error("start should default to false: create records, it does not boot")
	}
}

func TestBuildCreateRequestMountFlags(t *testing.T) {
	got, err := runBuild(t, "web",
		"--mount", "type=tmpfs,target=/scratch",
		"--mount", "source=data,target=/var/lib/data,readonly",
		"--mount", "type=file,source=/etc/app.conf,target=/etc/app.conf")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	want := []*dicerdv1.Mount{
		{Type: dicerdv1.MountType_MOUNT_TYPE_TMPFS, Target: "/scratch"},
		{Type: dicerdv1.MountType_MOUNT_TYPE_VOLUME, Source: "data", Target: "/var/lib/data", ReadOnly: true},
		{Type: dicerdv1.MountType_MOUNT_TYPE_FILE, Source: "/etc/app.conf", Target: "/etc/app.conf"},
	}
	if !slices.EqualFunc(got.GetMounts(), want, equalMessages) {
		t.Errorf("mounts = %+v, want %+v", got.GetMounts(), want)
	}
}

func TestBuildCreateRequestRejectsBadMounts(t *testing.T) {
	for _, argv := range [][]string{
		{"--mount", "type=volume,source=data"},
		{"--mount", "type=volume,target=/x,color=red"},
	} {
		if _, err := runBuild(t, append([]string{"web"}, argv...)...); err == nil {
			t.Errorf("%v should be rejected", argv)
		}
	}
}

func TestBuildCreateRequestCommandAfterDash(t *testing.T) {
	got, err := runBuild(t, "web", "--image", "alpine", "--", "sh", "-c", "echo 'hello world'")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	want := []string{"sh", "-c", "echo 'hello world'"}
	if !slices.Equal(got.GetCmd(), want) {
		t.Errorf("cmd = %q, want %q: arguments must pass through unsplit", got.GetCmd(), want)
	}
	if got.GetName() != "web" {
		t.Errorf("name = %q, want web", got.GetName())
	}
}

func TestBuildCreateRequestPublish(t *testing.T) {
	req, err := runBuild(t, "web", "-p", "8080:80", "--publish", "10.0.0.1:53:53/udp")
	if err != nil {
		t.Fatalf("buildCreateRequest: %v", err)
	}

	got := req.GetPorts()
	if len(got) != 2 ||
		got[0].GetHostPort() != 8080 || got[0].GetGuestPort() != 80 || got[0].GetProtocol() != 0 ||
		got[1].GetHostIp() != "10.0.0.1" || got[1].GetProtocol() != dicerdv1.Protocol_PROTOCOL_UDP {
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
	got := formatPorts([]*dicerdv1.PortMapping{
		{HostPort: 8080, GuestPort: 80},
		{HostIp: "10.0.0.1", HostPort: 53, GuestPort: 53, Protocol: dicerdv1.Protocol_PROTOCOL_UDP},
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
	if got.GetInitMode() != dicerdv1.InitMode_INIT_MODE_SYSTEMD {
		t.Errorf("--init-mode: init mode = %q, want systemd", got.GetInitMode())
	}

	// Left out, it is the daemon's to default.
	if got, _ := runBuild(t, "web", "--image", "debian"); got.GetInitMode() != dicerdv1.InitMode_INIT_MODE_UNSPECIFIED {
		t.Errorf("no --init-mode: init mode = %q, want it unset", got.GetInitMode())
	}
}

func TestCommandLinesShowTheInitModeOnlyWhenChosen(t *testing.T) {
	for mode, want := range map[dicerdv1.InitMode][]string{
		dicerdv1.InitMode_INIT_MODE_UNSPECIFIED: {"the image's"},
		dicerdv1.InitMode_INIT_MODE_AUTO:        {"the image's"},
		dicerdv1.InitMode_INIT_MODE_SYSTEMD:     {"the image's", "in systemd mode"},
	} {
		got := commandLines(&dicerdv1.Instance{InitMode: mode})
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
	if !got.GetRemoveOnExit() {
		t.Error("--rm was not carried into the definition")
	}

	if got, _ := runBuild(t, "web", "--image", "alpine"); got.GetRemoveOnExit() {
		t.Error("an instance that did not ask to be deleted says it did")
	}
}

// equalMessages reports whether two messages are equal, for slices.EqualFunc.
func equalMessages[M proto.Message](a, b M) bool { return proto.Equal(a, b) }
