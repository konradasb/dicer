// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dicer-sh/dicer"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

func TestInstanceToProtoRuntimeFields(t *testing.T) {
	inst := dicer.InstanceSpec{
		ID:          "id-web",
		Name:        "web",
		ImageRef:    "docker.io/library/alpine:3.21",
		KernelName:  "k1",
		NetworkName: "default",
		VCPUs:       2,
		MemoryBytes: 2 << 30,
		DiskBytes:   20 << 30,
		CreatedAt:   time.Now(),
	}

	t.Run("stopped instance carries no runtime detail", func(t *testing.T) {
		got := instanceToProto(dicer.Instance{Spec: inst, Status: dicer.InstanceStatus{State: dicer.StateStopped}})

		if got.GetState() != "Stopped" {
			t.Errorf("state = %q, want Stopped", got.GetState())
		}
		if got.GetIp() != "" {
			t.Errorf("IP = %q, want empty for a stopped instance", got.GetIp())
		}
		if got.GetHypervisorPid() != 0 {
			t.Errorf("pid = %d, want 0 for a stopped instance", got.GetHypervisorPid())
		}
		if got.GetStartTime() != nil {
			t.Error("startedAt should be unset for a stopped instance")
		}
	})

	t.Run("running instance carries pid and address", func(t *testing.T) {
		pid := 4242
		rt := dicer.InstanceStatus{
			State:             dicer.StateRunning,
			HypervisorPID:     &pid,
			HypervisorVersion: "v49.0.0",
			StartedAt:         time.Now(),
		}
		rt.IP, rt.MAC = "10.0.0.5", "02:00:00:00:00:01"

		got := instanceToProto(dicer.Instance{Spec: inst, Status: rt})

		if got.GetHypervisorPid() != int64(pid) {
			t.Errorf("pid = %d, want %d", got.GetHypervisorPid(), pid)
		}
		if got.GetIp() != "10.0.0.5" || got.GetMac() != "02:00:00:00:00:01" {
			t.Errorf("address = %s/%s, want 10.0.0.5/02:00:00:00:00:01", got.GetIp(), got.GetMac())
		}
		// The running VMM's version wins over whatever the definition pinned.
		if got.GetHypervisorVersion() != "v49.0.0" {
			t.Errorf("hypervisor version = %q, want the running one", got.GetHypervisorVersion())
		}
	})

	t.Run("sizes are carried as bytes", func(t *testing.T) {
		got := instanceToProto(dicer.Instance{Spec: inst, Status: dicer.InstanceStatus{State: dicer.StateStopped}})

		if got.GetMemoryBytes() != 2<<30 {
			t.Errorf("memory = %d, want %d", got.GetMemoryBytes(), 2<<30)
		}
		if got.GetDiskBytes() != 20<<30 {
			t.Errorf("disk = %d, want %d", got.GetDiskBytes(), 20<<30)
		}
	})
}

func TestInstanceToProtoMounts(t *testing.T) {
	inst := dicer.InstanceSpec{
		ID:   "id-web",
		Name: "web",
		VolumeMounts: []dicer.VolumeMount{
			{VolumeName: "data", MountPath: "/var/lib/data", AccessMode: dicer.AccessModeReadWriteOnce},
		},
		Files: []dicer.FileMount{
			{Name: "db-password", HostPath: "/etc/dicer/db-password"},
		},
		Ports: []dicer.PortMapping{{HostPort: 8080, GuestPort: 80}},
	}

	got := instanceToProto(dicer.Instance{Spec: inst, Status: dicer.InstanceStatus{State: dicer.StateStopped}})

	if len(got.GetVolumes()) != 1 || got.GetVolumes()[0].GetVolumeName() != "data" {
		t.Errorf("volumes = %+v, want one named data", got.GetVolumes())
	}
	if len(got.GetFiles()) != 1 || got.GetFiles()[0].GetHostPath() != "/etc/dicer/db-password" {
		t.Errorf("files = %+v, want one host path", got.GetFiles())
	}
	if len(got.GetPorts()) != 1 || got.GetPorts()[0].GetProtocol() != dicer.ProtocolTCP {
		t.Errorf("ports = %+v, want one with the protocol defaulted to tcp", got.GetPorts())
	}
}

func TestNetworkToProtoUsage(t *testing.T) {
	n := dicer.Network{
		ID: "net-1", Name: "default",
		Subnet: "10.0.0.0/24", Gateway: "10.0.0.1", Bridge: "dicer-default",
	}

	// /24 has 256 addresses; network, broadcast and gateway are reserved.
	got := networkToProto(n, 3)

	if got.GetTotalIps() != 253 {
		t.Errorf("total = %d, want 253", got.GetTotalIps())
	}
	if got.GetFreeIps() != 250 {
		t.Errorf("free = %d, want 250 with three allocated", got.GetFreeIps())
	}
}

// TestAllocationToProtoDerivesTAP pins the design decision that the TAP name
// is computed from the instance ID rather than stored.
func TestAllocationToProtoDerivesTAP(t *testing.T) {
	got := allocationToProto(dicer.NetworkAllocation{InstanceID: "id-web", IP: "10.0.0.5"}, "web")

	if got.GetInstanceName() != "web" {
		t.Errorf("instance name = %q, want web", got.GetInstanceName())
	}
	if got.GetTapDevice() == "" {
		t.Error("TAP device should be derived, not left empty")
	}
	// Derivation must be stable: recovery depends on recomputing this name.
	again := allocationToProto(dicer.NetworkAllocation{InstanceID: "id-web", IP: "10.0.0.5"}, "web")
	if got.GetTapDevice() != again.GetTapDevice() {
		t.Errorf("TAP name is not deterministic: %q then %q",
			got.GetTapDevice(), again.GetTapDevice())
	}
}

func TestFileMounts(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(realFile, []byte("s3cret"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	t.Run("accepts a real absolute path", func(t *testing.T) {
		got, err := fileMounts([]*dicerdv1.FileMount{{Name: "secret", HostPath: realFile}})
		if err != nil {
			t.Fatalf("fileMounts: %v", err)
		}
		if len(got) != 1 || got[0].HostPath != realFile {
			t.Errorf("got %+v, want one mount for %s", got, realFile)
		}
	})

	t.Run("rejects names that would escape /run/secrets", func(t *testing.T) {
		for _, name := range []string{"../escape", "sub/dir", ".", "..", ""} {
			_, err := fileMounts([]*dicerdv1.FileMount{{Name: name, HostPath: realFile}})
			if err == nil {
				t.Errorf("name %q should be rejected: it becomes a filename in the guest", name)
			}
		}
	})

	t.Run("rejects relative and missing host paths", func(t *testing.T) {
		cases := []*dicerdv1.FileMount{
			{Name: "secret", HostPath: "relative/path"},
			{Name: "secret", HostPath: ""},
			{Name: "secret", HostPath: filepath.Join(dir, "does-not-exist")},
			{Name: "secret", HostPath: dir}, // a directory, not a file
		}
		for _, c := range cases {
			if _, err := fileMounts([]*dicerdv1.FileMount{c}); err == nil {
				t.Errorf("host path %q should be rejected", c.GetHostPath())
			}
		}
	})

	t.Run("rejects duplicate names", func(t *testing.T) {
		_, err := fileMounts([]*dicerdv1.FileMount{
			{Name: "secret", HostPath: realFile},
			{Name: "secret", HostPath: realFile},
		})
		if err == nil {
			t.Error("two mounts with the same guest filename should be rejected")
		}
	})

	t.Run("empty input yields no mounts", func(t *testing.T) {
		got, err := fileMounts(nil)
		if err != nil || got != nil {
			t.Errorf("fileMounts(nil) = %v, %v; want nil, nil", got, err)
		}
	})
}

func TestPortMappings(t *testing.T) {
	t.Run("defaults the protocol to tcp", func(t *testing.T) {
		got, err := portMappings([]*dicerdv1.PortMapping{{HostPort: 8080, GuestPort: 80}})
		if err != nil {
			t.Fatalf("portMappings: %v", err)
		}
		want := dicer.PortMapping{HostPort: 8080, GuestPort: 80, Protocol: dicer.ProtocolTCP}
		if len(got) != 1 || got[0] != want {
			t.Errorf("got %+v, want [%+v]", got, want)
		}
	})

	t.Run("rejects invalid mappings", func(t *testing.T) {
		cases := map[string][]*dicerdv1.PortMapping{
			"port out of range": {{HostPort: 65536 + 80, GuestPort: 80}},
			"missing port":      {{HostPort: 8080}},
			"loopback":          {{HostIp: "127.0.0.1", HostPort: 8080, GuestPort: 80}},
			"overlapping":       {{HostPort: 8080, GuestPort: 80}, {HostPort: 8080, GuestPort: 81}},
		}
		for name, in := range cases {
			if _, err := portMappings(in); status.Code(err) != codes.InvalidArgument {
				t.Errorf("%s: err = %v, want InvalidArgument", name, err)
			}
		}
	})

	t.Run("empty input yields no mappings", func(t *testing.T) {
		got, err := portMappings(nil)
		if err != nil || got != nil {
			t.Errorf("portMappings(nil) = %v, %v; want nil, nil", got, err)
		}
	})
}

func TestInitModeFromProto(t *testing.T) {
	for in, want := range map[string]dicer.InitMode{"": dicer.ModeAuto, "exec": dicer.ModeExec, "systemd": dicer.ModeSystemd} {
		if got, err := initModeFromProto(in); err != nil || got != want {
			t.Errorf("initModeFromProto(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := initModeFromProto("openrc"); status.Code(err) != codes.InvalidArgument {
		t.Errorf("initModeFromProto(openrc) = %v, want InvalidArgument", err)
	}

	// An instance that leaves it to the guest says so.
	if got := instanceToProto(dicer.Instance{Status: dicer.InstanceStatus{State: dicer.StateStopped}}).GetInitMode(); got != "auto" {
		t.Errorf("init mode of an instance with none = %q, want auto", got)
	}
}
