// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// imageHandler handles image-related RPCs.
type imageHandler struct {
	definitions *filestore.Manager
	instances   *vm.Manager
	images      *image.Manager
}

// PullImage pulls an image, streaming progress and finally the image.
func (h *imageHandler) PullImage(
	req *dicerdv1.PullImageRequest,
	stream grpc.ServerStreamingServer[dicerdv1.PullImageProgress],
) error {
	if req.GetRef() == "" {
		return errdefs.InvalidArgument("ref is required")
	}

	// Progress is reported on this goroutine.
	onProgress := func(p types.PullProgress) {
		_ = stream.Send(&dicerdv1.PullImageProgress{
			Stage:           pullStages.toProto(p.Stage),
			DownloadedBytes: p.DownloadedBytes,
			TotalBytes:      p.TotalBytes,
		})
	}

	img, err := h.images.Pull(stream.Context(), req.GetRef(), onProgress)
	if err != nil {
		return imageStatus(err)
	}

	return stream.Send(&dicerdv1.PullImageProgress{
		Stage: dicerdv1.PullStage_PULL_STAGE_UNSPECIFIED,
		Image: imageToProto(img),
	})
}

func (h *imageHandler) ListImages(
	_ context.Context, _ *dicerdv1.ListImagesRequest,
) (*dicerdv1.ListImagesResponse, error) {
	images := h.images.List()

	resp := &dicerdv1.ListImagesResponse{
		Images: make([]*dicerdv1.Image, 0, len(images)),
	}
	for _, img := range images {
		resp.Images = append(resp.Images, imageToProto(img))
	}

	return resp, nil
}

// GetImage returns an image this host holds without contacting a registry.
func (h *imageHandler) GetImage(
	_ context.Context, req *dicerdv1.GetImageRequest,
) (*dicerdv1.Image, error) {
	img, err := h.images.Get(req.GetRef())
	if err != nil {
		return nil, imageStatus(err)
	}

	return imageToProto(img), nil
}

// DeleteImage removes an image, refusing one that is in use.
func (h *imageHandler) DeleteImage(
	_ context.Context, req *dicerdv1.DeleteImageRequest,
) (*emptypb.Empty, error) {
	img, err := h.images.Get(req.GetRef())
	if err != nil {
		return nil, imageStatus(err)
	}

	if !req.GetForce() {
		users, err := h.instancesUsing(img.Digest)
		if err != nil {
			return nil, err
		}
		if len(users) > 0 {
			return nil, errdefs.InvalidState(
				"image %q is in use by instance %q", req.GetRef(), users[0])
		}

		needed, err := h.instances.ImagesInUse()
		if err != nil {
			return nil, err
		}
		if _, ok := needed[img.Digest]; ok {
			return nil, errdefs.InvalidState(
				"image %q is the root disk of a running instance, or of a snapshot", req.GetRef())
		}
	}

	if err := h.images.Delete(req.GetRef()); err != nil {
		return nil, imageStatus(err)
	}

	return &emptypb.Empty{}, nil
}

// PruneImages removes the images nothing is defined to boot from.
func (h *imageHandler) PruneImages(
	_ context.Context, _ *dicerdv1.PruneImagesRequest,
) (*dicerdv1.PruneImagesResponse, error) {
	keep, err := h.imagesInUse()
	if err != nil {
		return nil, err
	}

	result, err := h.images.Prune(keep)
	if err != nil {
		return nil, err
	}

	resp := &dicerdv1.PruneImagesResponse{
		Images:         make([]*dicerdv1.Image, 0, len(result.Images)),
		ReclaimedBytes: result.ReclaimedBytes,
	}
	for _, img := range result.Images {
		resp.Images = append(resp.Images, imageToProto(&img))
	}

	return resp, nil
}

// imagesInUse is the set of image digests a prune keeps. See
// vm.Manager.ImagesInUse.
func (h *imageHandler) imagesInUse() (map[string]struct{}, error) {
	keep, err := h.instances.ImagesInUse()
	if err != nil {
		return nil, err
	}
	return keep, nil
}

// instancesUsing names the instances defined to boot from an image.
func (h *imageHandler) instancesUsing(digest string) ([]string, error) {
	instances, err := h.definitions.ListInstances()
	if err != nil {
		return nil, err
	}

	var users []string
	for _, inst := range instances {
		img, err := h.images.Get(inst.ImageRef)
		if err == nil && img.Digest == digest {
			users = append(users, inst.Name)
		}
	}

	return users, nil
}

// imageStatus converts image errors to statuses.
func imageStatus(err error) error {
	if errors.Is(err, image.ErrInvalidReference) {
		return errdefs.InvalidArgument("%v", err)
	}
	return err
}
