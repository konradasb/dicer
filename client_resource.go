// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"

	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// GetImage returns one image by reference.
func (c *Client) GetImage(ctx context.Context, ref string) (Image, error) {
	resp, err := c.daemon.GetImage(ctx, &dicerdv1.GetImageRequest{Ref: ref})
	if err != nil {
		return Image{}, err
	}

	return imageFromProto(resp), nil
}

// ListImages returns every image held on the host.
func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	resp, err := c.daemon.ListImages(ctx, &dicerdv1.ListImagesRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]Image, 0, len(resp.GetImages()))
	for _, img := range resp.GetImages() {
		out = append(out, imageFromProto(img))
	}

	return out, nil
}

// DeleteImage removes an image and its boot disk. An image an instance is
// defined to boot from is refused unless force is set, and that instance
// pulls it again on its next start.
func (c *Client) DeleteImage(ctx context.Context, ref string, force bool) error {
	_, err := c.daemon.DeleteImage(ctx, &dicerdv1.DeleteImageRequest{Ref: ref, Force: force})

	return err
}

// PruneImages removes every image no instance, guest or snapshot is using.
func (c *Client) PruneImages(ctx context.Context) (PruneResult, error) {
	resp, err := c.daemon.PruneImages(ctx, &dicerdv1.PruneImagesRequest{})
	if err != nil {
		return PruneResult{}, err
	}

	out := PruneResult{
		Images:         make([]Image, 0, len(resp.GetImages())),
		ReclaimedBytes: resp.GetReclaimedBytes(),
	}
	for _, img := range resp.GetImages() {
		out.Images = append(out.Images, imageFromProto(img))
	}

	return out, nil
}

// CreateVolume provisions a volume of the given size.
func (c *Client) CreateVolume(ctx context.Context, name string, sizeBytes int64) (Volume, error) {
	resp, err := c.daemon.CreateVolume(ctx, &dicerdv1.CreateVolumeRequest{Name: name, SizeBytes: sizeBytes})
	if err != nil {
		return Volume{}, err
	}

	return volumeFromProto(resp), nil
}

// GetVolume returns one volume by name.
func (c *Client) GetVolume(ctx context.Context, name string) (Volume, error) {
	resp, err := c.daemon.GetVolume(ctx, &dicerdv1.GetVolumeRequest{Name: name})
	if err != nil {
		return Volume{}, err
	}

	return volumeFromProto(resp), nil
}

// ListVolumes returns every volume on the host.
func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	resp, err := c.daemon.ListVolumes(ctx, &dicerdv1.ListVolumesRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]Volume, 0, len(resp.GetVolumes()))
	for _, v := range resp.GetVolumes() {
		out = append(out, volumeFromProto(v))
	}

	return out, nil
}

// DeleteVolume removes a volume and the data on it. One an instance mounts is
// refused.
func (c *Client) DeleteVolume(ctx context.Context, name string) error {
	_, err := c.daemon.DeleteVolume(ctx, &dicerdv1.DeleteVolumeRequest{Name: name})

	return err
}

// CreateNetwork defines a network. Only Name and Subnet are required: the
// gateway defaults to the subnet's first address, and the bridge is the
// daemon's to name.
func (c *Client) CreateNetwork(ctx context.Context, n Network) (Network, error) {
	resp, err := c.daemon.CreateNetwork(ctx, &dicerdv1.CreateNetworkRequest{
		Name:        n.Name,
		Subnet:      n.Subnet,
		Gateway:     n.Gateway,
		Mtu:         int32(n.MTU),
		Nameservers: n.Nameservers,
		Isolated:    n.Isolated,
	})
	if err != nil {
		return Network{}, err
	}

	return networkFromProto(resp), nil
}

// GetNetwork returns one network by name.
func (c *Client) GetNetwork(ctx context.Context, name string) (Network, error) {
	resp, err := c.daemon.GetNetwork(ctx, &dicerdv1.GetNetworkRequest{Name: name})
	if err != nil {
		return Network{}, err
	}

	return networkFromProto(resp), nil
}

// ListNetworks returns every network on the host.
func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	resp, err := c.daemon.ListNetworks(ctx, &dicerdv1.ListNetworksRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]Network, 0, len(resp.GetNetworks()))
	for _, n := range resp.GetNetworks() {
		out = append(out, networkFromProto(n))
	}

	return out, nil
}

// DeleteNetwork removes a network. One an instance is attached to is refused.
func (c *Client) DeleteNetwork(ctx context.Context, name string) error {
	_, err := c.daemon.DeleteNetwork(ctx, &dicerdv1.DeleteNetworkRequest{Name: name})

	return err
}

// ListNetworkAllocations returns the addresses held on a network, by the
// instances holding them.
func (c *Client) ListNetworkAllocations(ctx context.Context, name string) ([]NetworkAllocation, error) {
	resp, err := c.daemon.ListNetworkAllocations(ctx, &dicerdv1.ListNetworkAllocationsRequest{Name: name})
	if err != nil {
		return nil, err
	}

	out := make([]NetworkAllocation, 0, len(resp.GetAllocations()))
	for _, a := range resp.GetAllocations() {
		out = append(out, allocationFromProto(a))
	}

	return out, nil
}

// ImportKernel downloads a kernel and keeps it on the host. A SHA256 on the
// kernel is verified against what was downloaded when it is set.
func (c *Client) ImportKernel(ctx context.Context, k Kernel) (Kernel, error) {
	resp, err := c.daemon.ImportKernel(ctx, &dicerdv1.ImportKernelRequest{
		Name:   k.Name,
		Url:    k.URL,
		Arch:   k.Arch,
		Sha256: k.SHA256,
	})
	if err != nil {
		return Kernel{}, err
	}

	return kernelFromProto(resp), nil
}

// GetKernel returns one kernel by name.
func (c *Client) GetKernel(ctx context.Context, name string) (Kernel, error) {
	resp, err := c.daemon.GetKernel(ctx, &dicerdv1.GetKernelRequest{Name: name})
	if err != nil {
		return Kernel{}, err
	}

	return kernelFromProto(resp), nil
}

// ListKernels returns every kernel on the host.
func (c *Client) ListKernels(ctx context.Context) ([]Kernel, error) {
	resp, err := c.daemon.ListKernels(ctx, &dicerdv1.ListKernelsRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]Kernel, 0, len(resp.GetKernels()))
	for _, k := range resp.GetKernels() {
		out = append(out, kernelFromProto(k))
	}

	return out, nil
}

// DeleteKernel removes a kernel. One an instance is defined to boot is
// refused.
func (c *Client) DeleteKernel(ctx context.Context, name string) error {
	_, err := c.daemon.DeleteKernel(ctx, &dicerdv1.DeleteKernelRequest{Name: name})

	return err
}

// CreateSnapshot freezes a running or paused instance to disk. An empty name
// is filled in from the time it is taken.
func (c *Client) CreateSnapshot(ctx context.Context, instance, name string) (Snapshot, error) {
	resp, err := c.daemon.CreateSnapshot(ctx, &dicerdv1.CreateSnapshotRequest{Instance: instance, Name: name})
	if err != nil {
		return Snapshot{}, err
	}

	return snapshotFromProto(resp), nil
}

// GetSnapshot returns one of an instance's snapshots.
func (c *Client) GetSnapshot(ctx context.Context, instance, name string) (Snapshot, error) {
	resp, err := c.daemon.GetSnapshot(ctx, &dicerdv1.GetSnapshotRequest{Instance: instance, Name: name})
	if err != nil {
		return Snapshot{}, err
	}

	return snapshotFromProto(resp), nil
}

// ListSnapshots returns an instance's snapshots.
func (c *Client) ListSnapshots(ctx context.Context, instance string) ([]Snapshot, error) {
	resp, err := c.daemon.ListSnapshots(ctx, &dicerdv1.ListSnapshotsRequest{Instance: instance})
	if err != nil {
		return nil, err
	}

	out := make([]Snapshot, 0, len(resp.GetSnapshots()))
	for _, s := range resp.GetSnapshots() {
		out = append(out, snapshotFromProto(s))
	}

	return out, nil
}

// DeleteSnapshot removes one of an instance's snapshots.
func (c *Client) DeleteSnapshot(ctx context.Context, instance, name string) error {
	_, err := c.daemon.DeleteSnapshot(ctx, &dicerdv1.DeleteSnapshotRequest{Instance: instance, Name: name})

	return err
}

// RestoreSnapshot puts an instance back to where a snapshot was taken: its
// guest resumes from that memory, over that disk.
func (c *Client) RestoreSnapshot(ctx context.Context, instance, name string) (Instance, error) {
	resp, err := c.daemon.RestoreSnapshot(ctx, &dicerdv1.RestoreSnapshotRequest{Instance: instance, Name: name})
	if err != nil {
		return Instance{}, err
	}

	return instanceFromProto(resp), nil
}

// GetHostInfo returns what the daemon says about itself and its host.
func (c *Client) GetHostInfo(ctx context.Context) (HostInfo, error) {
	resp, err := c.daemon.GetHostInfo(ctx, &dicerdv1.GetHostInfoRequest{})
	if err != nil {
		return HostInfo{}, err
	}

	return hostInfoFromProto(resp), nil
}

// GetResources returns what the host has, and what its instances hold of it.
func (c *Client) GetResources(ctx context.Context) (HostResources, error) {
	resp, err := c.daemon.GetResources(ctx, &dicerdv1.GetResourcesRequest{})
	if err != nil {
		return HostResources{}, err
	}

	return hostResourcesFromProto(resp), nil
}
