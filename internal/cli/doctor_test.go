// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// doctorDaemon is a fake daemon whose host checks find results.
type doctorDaemon struct {
	*fakeInstanceDaemon

	results []*dicerdv1.HostCheckResult

	mu  sync.Mutex
	req *dicerdv1.CheckHostRequest
}

func (d *doctorDaemon) CheckHost(
	req *dicerdv1.CheckHostRequest, stream grpc.ServerStreamingServer[dicerdv1.HostCheckResult],
) error {
	d.mu.Lock()
	d.req = proto.CloneOf(req)
	d.mu.Unlock()
	for _, r := range d.results {
		if !req.GetTestInstance() && r.GetGroup() == dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_INSTANCES {
			continue
		}
		if err := stream.Send(r); err != nil {
			return err
		}
	}
	return nil
}

// request returns the last CheckHost request.
func (d *doctorDaemon) request() *dicerdv1.CheckHostRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.req
}

// The groups and statuses, short.
const (
	groupHost      = dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_HOST
	groupInstances = dicerdv1.HostCheckGroup_HOST_CHECK_GROUP_INSTANCES
	statusOK       = dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_OK
	statusWarning  = dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_WARNING
	statusFailed   = dicerdv1.HostCheckStatus_HOST_CHECK_STATUS_FAILED
)

// newDoctorDaemon returns a doctorDaemon whose host carries Cloud
// Hypervisor and Firecracker, and passes every check.
func newDoctorDaemon() *doctorDaemon {
	d := &doctorDaemon{fakeInstanceDaemon: newFakeInstanceDaemon()}
	d.host.Hypervisors = []*dicerdv1.HypervisorInfo{
		{Type: dicerdv1.HypervisorType_HYPERVISOR_TYPE_CLOUD_HYPERVISOR, Versions: []string{"v53.0.0"}, IsDefault: true},
		{Type: dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER, Versions: []string{"v1.17.0"}},
	}
	d.results = []*dicerdv1.HostCheckResult{
		{Group: groupHost, Name: "kvm", Status: statusOK, Detail: "/dev/kvm is usable"},
		{Group: groupHost, Name: "ip_forwarding", Status: statusOK, Detail: "IPv4 forwarding is on"},
		{Group: groupInstances, Name: "cloud-hypervisor", Status: statusOK, Detail: "booted, ran a command and stopped in 1.1s"},
		{Group: groupInstances, Name: "internet", Status: statusOK, Detail: "a test instance reached the internet"},
	}
	return d
}

func TestDoctorOnAHealthyHost(t *testing.T) {
	d := newDoctorDaemon()
	serveFakeDaemon(t, d)

	out, err := run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor = %v\n%s", err, out)
	}
	for _, want := range []string{
		"Host\n", "✓ KVM                              /dev/kvm is usable\n",
		"\nInstances\n",
		"✓ Boot (cloud-hypervisor v53.0.0)  booted, ran a command and stopped in 1.1s\n",
		"✓ Internet access                  a test instance reached the internet\n",
		"No problems found.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !d.request().GetTestInstance() {
		t.Error("doctor did not ask for test instances")
	}
}

func TestDoctorPassesItsOptions(t *testing.T) {
	d := newDoctorDaemon()
	serveFakeDaemon(t, d)

	out, err := run(t, "doctor", "--image", "registry.example.com/busybox:1.37", "--timeout", "30s", "--keep",
		"--hypervisor-type", "firecracker", "--hypervisor-version", "v1.17.0")
	if err != nil {
		t.Fatal(err)
	}
	req := d.request()
	if req.GetTestInstanceImage() != "registry.example.com/busybox:1.37" ||
		req.GetTestInstanceTimeout().AsDuration() != 30*time.Second || !req.GetKeepFailedTestInstance() ||
		req.GetTestInstanceHypervisor() != dicerdv1.HypervisorType_HYPERVISOR_TYPE_FIRECRACKER ||
		req.GetTestInstanceHypervisorVersion() != "v1.17.0" {
		t.Errorf("request = %v, want the flags' image, timeout, keep and hypervisor", req)
	}
	if !strings.Contains(out, "Boot (firecracker v1.17.0)") {
		t.Errorf("output does not name the hypervisor asked for:\n%s", out)
	}
}

func TestDoctorReportsAFailedCheckWithItsHint(t *testing.T) {
	d := newDoctorDaemon()
	d.results[0] = &dicerdv1.HostCheckResult{
		Group: groupHost, Name: "kvm", Status: statusFailed,
		Detail: "/dev/kvm does not exist", Hint: "turn virtualisation on in the firmware",
	}
	serveFakeDaemon(t, d)

	out, err := run(t, "doctor", "--host-only")
	if err == nil || err.Error() != "1 problem found" {
		t.Errorf("doctor = %v, want 1 problem found", err)
	}
	for _, want := range []string{
		"✗ KVM            /dev/kvm does not exist\n",
		"\n                   turn virtualisation on in the firmware\n", "1 problem found.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if d.request().GetTestInstance() || strings.Contains(out, "\nInstances\n") {
		t.Errorf("--host-only asked for test instances:\n%s", out)
	}
}

func TestDoctorShowsAFailedTestInstancesConsole(t *testing.T) {
	d := newDoctorDaemon()
	d.results = append(d.results[:2], &dicerdv1.HostCheckResult{
		Group: groupInstances, Name: "cloud-hypervisor", Status: statusFailed,
		Detail:  "ended without running its command: exit code 1",
		Console: []string{"Run /init as init process", "Kernel panic - not syncing"},
	})
	serveFakeDaemon(t, d)

	out, err := run(t, "doctor")
	if err == nil || err.Error() != "1 problem found" {
		t.Errorf("doctor = %v, want 1 problem found", err)
	}
	for _, want := range []string{
		"✗ Boot (cloud-hypervisor v53.0.0)  ended without running its command",
		"\n                                     Kernel panic - not syncing\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDoctorWarnsWithoutFailing(t *testing.T) {
	d := newDoctorDaemon()
	d.results[len(d.results)-1] = &dicerdv1.HostCheckResult{
		Group: groupInstances, Name: "internet", Status: statusWarning,
		Detail: "a test instance could not reach http://example.com/",
	}
	serveFakeDaemon(t, d)

	out, err := run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor = %v, want a warning only\n%s", err, out)
	}
	if !strings.Contains(out, "! Internet access") || !strings.Contains(out, "No problems, 1 warning.") {
		t.Errorf("output does not warn of a test instance offline:\n%s", out)
	}
}

func TestDoctorAsJSON(t *testing.T) {
	d := newDoctorDaemon()
	serveFakeDaemon(t, d)

	out, err := run(t, "doctor", "--format", "json")
	if err != nil {
		t.Fatalf("doctor = %v\n%s", err, out)
	}
	var report struct {
		Results []struct {
			Group  string `json:"group"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(report.Results) != len(d.results) {
		t.Fatalf("results = %+v, want %d", report.Results, len(d.results))
	}
	if r := report.Results[2]; r.Group != "instances" || r.Name != "cloud-hypervisor" || r.Status != "ok" {
		t.Errorf("third result = %+v, want the cloud-hypervisor test instance, ok", r)
	}
}

func TestDoctorOnADaemonTooOldToCheckItsHost(t *testing.T) {
	d := newFakeInstanceDaemon()
	serveFakeDaemon(t, d)

	_, err := run(t, "doctor", "--host-only")
	if err == nil || !strings.Contains(err.Error(), "too old to check its host") {
		t.Errorf("doctor = %v, want the daemon called too old", err)
	}
}

func TestDoctorRefusesAnUnknownHypervisor(t *testing.T) {
	serveFakeDaemon(t, newDoctorDaemon())

	if _, err := run(t, "doctor", "--hypervisor-type", "qemu"); err == nil {
		t.Error("doctor --hypervisor-type qemu succeeded")
	}
}
