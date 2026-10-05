// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// storeDaemon is fakeInstanceDaemon with networks, volumes, kernels, images
// and snapshots, each a set of names, and the names of each that are in use,
// which it refuses to delete as the daemon does.
type storeDaemon struct {
	*fakeInstanceDaemon

	networkSet, volumeSet, kernelSet, imageSet, snapshotSet map[string]bool
	inUse                                                   map[string]bool
}

func newStoreDaemon(instances ...*dicerdv1.Instance) *storeDaemon {
	return &storeDaemon{
		fakeInstanceDaemon: newFakeInstanceDaemon(instances...),
		networkSet:         make(map[string]bool),
		volumeSet:          make(map[string]bool),
		kernelSet:          make(map[string]bool),
		imageSet:           make(map[string]bool),
		snapshotSet:        make(map[string]bool),
		inUse:              make(map[string]bool),
	}
}

// remove deletes name from set, unless it is in use and not forced.
func (d *storeDaemon) remove(kind string, set map[string]bool, name string, force bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !set[name] {
		return errdefs.NotFound("no %s %q", kind, name)
	}
	if d.inUse[name] && !force {
		return errdefs.InvalidState("%s %q is in use", kind, name)
	}
	d.record("delete " + kind + " " + name)
	delete(set, name)
	return nil
}

func (d *storeDaemon) names(set map[string]bool) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Sorted(maps.Keys(set))
}

func (d *storeDaemon) ListNetworks(context.Context, *dicerdv1.ListNetworksRequest) (*dicerdv1.ListNetworksResponse, error) {
	resp := &dicerdv1.ListNetworksResponse{}
	for _, n := range d.names(d.networkSet) {
		resp.Networks = append(resp.Networks, &dicerdv1.Network{Name: n})
	}
	return resp, nil
}

func (d *storeDaemon) DeleteNetwork(_ context.Context, req *dicerdv1.DeleteNetworkRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, d.remove("network", d.networkSet, req.GetName(), false)
}

func (d *storeDaemon) ListVolumes(context.Context, *dicerdv1.ListVolumesRequest) (*dicerdv1.ListVolumesResponse, error) {
	resp := &dicerdv1.ListVolumesResponse{}
	for _, n := range d.names(d.volumeSet) {
		resp.Volumes = append(resp.Volumes, &dicerdv1.Volume{Name: n})
	}
	return resp, nil
}

func (d *storeDaemon) DeleteVolume(_ context.Context, req *dicerdv1.DeleteVolumeRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, d.remove("volume", d.volumeSet, req.GetName(), false)
}

func (d *storeDaemon) ListKernels(context.Context, *dicerdv1.ListKernelsRequest) (*dicerdv1.ListKernelsResponse, error) {
	resp := &dicerdv1.ListKernelsResponse{}
	for _, n := range d.names(d.kernelSet) {
		resp.Kernels = append(resp.Kernels, &dicerdv1.Kernel{Name: n})
	}
	return resp, nil
}

func (d *storeDaemon) DeleteKernel(_ context.Context, req *dicerdv1.DeleteKernelRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, d.remove("kernel", d.kernelSet, req.GetName(), false)
}

func (d *storeDaemon) ListImages(context.Context, *dicerdv1.ListImagesRequest) (*dicerdv1.ListImagesResponse, error) {
	resp := &dicerdv1.ListImagesResponse{}
	for _, n := range d.names(d.imageSet) {
		resp.Images = append(resp.Images, &dicerdv1.Image{Name: n})
	}
	return resp, nil
}

func (d *storeDaemon) DeleteImage(_ context.Context, req *dicerdv1.DeleteImageRequest) (*emptypb.Empty, error) {
	err := d.remove("image", d.imageSet, req.GetRef(), req.GetForce())
	if err != nil && d.imageSet[req.GetRef()] {
		// The daemon's word for an image in use.
		return nil, errdefs.InvalidState("image %q is used by instance web", req.GetRef())
	}
	return &emptypb.Empty{}, err
}

func (d *storeDaemon) ListSnapshots(context.Context, *dicerdv1.ListSnapshotsRequest) (*dicerdv1.ListSnapshotsResponse, error) {
	resp := &dicerdv1.ListSnapshotsResponse{}
	for _, n := range d.names(d.snapshotSet) {
		resp.Snapshots = append(resp.Snapshots, &dicerdv1.Snapshot{Name: n})
	}
	return resp, nil
}

func (d *storeDaemon) DeleteSnapshot(_ context.Context, req *dicerdv1.DeleteSnapshotRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, d.remove("snapshot", d.snapshotSet, req.GetName(), false)
}

func TestDeleteAllInstances(t *testing.T) {
	d := newStoreDaemon(fakeInstances()...) // web running, db stopped, cache paused
	serveFakeDaemon(t, d)

	// Without -f, the running one is refused; the rest go all the same.
	out, err := run(t, "rm", "--all")
	if err == nil {
		t.Fatalf("rm --all with a running instance succeeded:\n%s", out)
	}
	if !strings.Contains(out, "stop it first or use -f") {
		t.Errorf("output = %q, want the running instance's refusal explained", out)
	}
	if got := d.instanceNames(); !slices.Equal(got, []string{"web"}) {
		t.Errorf("left = %q, want only the running instance", got)
	}

	if out, err := run(t, "instance", "delete", "-A", "-f"); err != nil {
		t.Fatalf("instance delete -A -f: %v\n%s", err, out)
	}
	if got := d.instanceNames(); len(got) != 0 {
		t.Errorf("left = %q, want none", got)
	}

	out, err = run(t, "rm", "-A", "-y")
	if err != nil || !strings.Contains(out, "There are no instances to delete") {
		t.Errorf("rm -A with none = %q, %v; want it to say there are none", out, err)
	}
}

func TestDeleteAllNeedsNamesOrAll(t *testing.T) {
	serveFakeDaemon(t, newStoreDaemon(fakeInstances()...))

	for _, args := range [][]string{{"rm", "web", "--all"}, {"network", "rm", "default", "-A"}, {"snapshot", "rm", "snap", "-A"}} {
		if _, err := run(t, args...); err == nil || !strings.Contains(err.Error(), "names or --all, not both") {
			t.Errorf("%q = %v, want names and --all refused together", args, err)
		}
	}
	for _, args := range [][]string{
		{"remote", "rm", "prod", "--all"},
	} {
		if _, err := run(t, args...); err == nil {
			t.Errorf("%q succeeded, want too many arguments for --all refused", args)
		}
	}
	for _, args := range [][]string{{"rm"}, {"volume", "rm"}, {"snapshot", "delete"}} {
		if _, err := run(t, args...); err == nil {
			t.Errorf("%q succeeded, want it to need names or --all", args)
		}
	}
}

func TestDeleteAllResources(t *testing.T) {
	d := newStoreDaemon()
	for _, n := range []string{"default", "backend", "lan"} {
		d.networkSet[n] = true
	}
	for _, n := range []string{"data", "cache"} {
		d.volumeSet[n] = true
	}
	for _, n := range []string{"linux-6.18", "linux-6.12"} {
		d.kernelSet[n] = true
	}
	for _, n := range []string{"web-before", "db-before"} {
		d.snapshotSet[n] = true
	}
	d.inUse["default"] = true
	d.inUse["linux-6.18"] = true
	serveFakeDaemon(t, d)

	tests := []struct {
		args []string
		set  map[string]bool
		left []string
	}{
		{[]string{"network", "rm", "--all"}, d.networkSet, []string{"default"}},
		{[]string{"volume", "delete", "-A"}, d.volumeSet, nil},
		{[]string{"kernel", "rm", "-A", "-y"}, d.kernelSet, []string{"linux-6.18"}},
		{[]string{"snapshot", "rm", "--all"}, d.snapshotSet, nil},
	}
	for _, tt := range tests {
		out, err := run(t, tt.args...)
		if wantErr := len(tt.left) > 0; (err != nil) != wantErr {
			t.Errorf("%q = %v, want an error %v:\n%s", tt.args, err, wantErr, out)
		}
		if got := d.names(tt.set); !slices.Equal(got, tt.left) {
			t.Errorf("%q left %q, want %q: those in use", tt.args, got, tt.left)
		}
	}
}

func TestDeleteAllImages(t *testing.T) {
	d := newStoreDaemon()
	d.imageSet["nginx:1.27"] = true
	d.imageSet["postgres:17"] = true
	d.inUse["nginx:1.27"] = true
	serveFakeDaemon(t, d)

	out, err := run(t, "rmi", "--all")
	if err == nil || !strings.Contains(out, "use -f to delete it anyway") {
		t.Errorf("rmi --all with one in use = %v, want it refused with a hint:\n%s", err, out)
	}
	if got := d.names(d.imageSet); !slices.Equal(got, []string{"nginx:1.27"}) {
		t.Errorf("left %q, want the one in use", got)
	}

	if out, err := run(t, "image", "rm", "-A", "-f"); err != nil {
		t.Fatalf("image rm -A -f: %v\n%s", err, out)
	}
	if got := d.names(d.imageSet); len(got) != 0 {
		t.Errorf("left %q, want none", got)
	}
}

func TestDeleteAllRemotes(t *testing.T) {
	isolateConfig(t)
	for _, r := range []string{"prod", "staging"} {
		if out, err := run(t, "remote", "create", r, "unix:///run/"+r+"/dicer.sock"); err != nil {
			t.Fatalf("remote create %s: %v\n%s", r, err, out)
		}
	}

	out, err := run(t, "remote", "rm", "--all")
	if err != nil {
		t.Fatalf("remote rm --all: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Remote prod deleted") || !strings.Contains(out, "Remote staging deleted") {
		t.Errorf("output = %q, want both remotes deleted", out)
	}

	// The built-in one is left, and nothing else is.
	out, err = run(t, "remote", "list", "-q")
	if err != nil || strings.TrimSpace(out) != "local" {
		t.Errorf("remote list = %q, %v; want only local", out, err)
	}
}

// instanceNames returns the names of d's instances.
func (d *fakeInstanceDaemon) instanceNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Sorted(maps.Keys(d.instances))
}
