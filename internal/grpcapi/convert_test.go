// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"testing"
	"time"

	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

func TestInstanceToProtoStatusFields(t *testing.T) {
	instance := types.InstanceSpec{
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

	t.Run("stopped instance carries no status detail", func(t *testing.T) {
		got := instanceToProto(types.Instance{Spec: instance, Status: types.InstanceStatus{State: types.InstanceStateStopped}})

		if got.GetState() != dicerdv1.InstanceState_INSTANCE_STATE_STOPPED {
			t.Errorf("state = %v, want stopped", got.GetState())
		}
		if got.GetIp() != "" {
			t.Errorf("IP = %q, want empty for a stopped instance", got.GetIp())
		}
		if got.GetHypervisorPid() != 0 {
			t.Errorf("pid = %d, want 0 for a stopped instance", got.GetHypervisorPid())
		}
		if got.GetStartTime() != nil {
			t.Errorf("start time = %v, want unset for a stopped instance", got.GetStartTime())
		}
	})

	t.Run("running instance carries pid and address", func(t *testing.T) {
		pid := 4242
		status := types.InstanceStatus{
			State:             types.InstanceStateRunning,
			VMMPID:            &pid,
			HypervisorVersion: "v49.0.0",
			StartedAt:         time.Now(),
		}
		status.IP, status.MAC = "10.0.0.5", "02:00:00:00:00:01"

		got := instanceToProto(types.Instance{Spec: instance, Status: status})

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
		got := instanceToProto(types.Instance{Spec: instance, Status: types.InstanceStatus{State: types.InstanceStateStopped}})

		if got.GetMemoryBytes() != 2<<30 {
			t.Errorf("memory = %d, want %d", got.GetMemoryBytes(), 2<<30)
		}
		if got.GetDiskBytes() != 20<<30 {
			t.Errorf("disk = %d, want %d", got.GetDiskBytes(), 20<<30)
		}
	})
}

func TestInstanceToProtoMounts(t *testing.T) {
	instance := types.InstanceSpec{
		ID:   "id-web",
		Name: "web",
		Mounts: []types.Mount{
			{Type: types.MountTypeVolume, Source: "data", Target: "/var/lib/data"},
			{Type: types.MountTypeFile, Source: "/etc/dicer/db-password", Target: "/run/secrets/db-password", ReadOnly: true},
		},
		Ports: []types.PortMapping{{HostPort: 8080, GuestPort: 80}},
	}

	got := instanceToProto(types.Instance{Spec: instance, Status: types.InstanceStatus{State: types.InstanceStateStopped}})

	mounts := got.GetMounts()
	if len(mounts) != 2 {
		t.Fatalf("mounts = %+v, want two", mounts)
	}
	if m := mounts[0]; m.GetType() != dicerdv1.MountType_MOUNT_TYPE_VOLUME || m.GetSource() != "data" || m.GetReadOnly() {
		t.Errorf("mounts[0] = %+v, want the volume data, read-write", m)
	}
	if m := mounts[1]; m.GetType() != dicerdv1.MountType_MOUNT_TYPE_FILE || m.GetTarget() != "/run/secrets/db-password" || !m.GetReadOnly() {
		t.Errorf("mounts[1] = %+v, want the host file, read-only under /run/secrets", m)
	}
	if len(got.GetPorts()) != 1 || got.GetPorts()[0].GetProtocol() != dicerdv1.Protocol_PROTOCOL_TCP {
		t.Errorf("ports = %+v, want one with the protocol defaulted to tcp", got.GetPorts())
	}
}

func TestNetworkToProtoUsage(t *testing.T) {
	n := types.Network{
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
	got := allocationToProto(types.NetworkAllocation{InstanceID: "id-web", IP: "10.0.0.5"}, "web")

	if got.GetInstanceName() != "web" {
		t.Errorf("instance name = %q, want web", got.GetInstanceName())
	}
	if got.GetTapDevice() == "" {
		t.Error("TAP device should be derived, not left empty")
	}
	// Derivation must be stable: recovery depends on recomputing this name.
	again := allocationToProto(types.NetworkAllocation{InstanceID: "id-web", IP: "10.0.0.5"}, "web")
	if got.GetTapDevice() != again.GetTapDevice() {
		t.Errorf("TAP name is not deterministic: %q then %q",
			got.GetTapDevice(), again.GetTapDevice())
	}
}

// An instance that leaves the init mode to the guest says it is auto.
func TestInstanceInitModeDefaultsToAuto(t *testing.T) {
	if got := instanceToProto(types.Instance{Status: types.InstanceStatus{State: types.InstanceStateStopped}}).GetInitMode(); got != dicerdv1.InitMode_INIT_MODE_AUTO {
		t.Errorf("init mode of an instance with none = %v, want auto", got)
	}
}

// An instance created without a hypervisor says which one it runs on.
func TestInstanceHypervisorTypeDefaultsToCloudHypervisor(t *testing.T) {
	got := instanceToProto(types.Instance{Status: types.InstanceStatus{State: types.InstanceStateStopped}}).GetHypervisorType()
	if got != dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR {
		t.Errorf("hypervisor type of an instance with none = %v, want Cloud Hypervisor", got)
	}
}
