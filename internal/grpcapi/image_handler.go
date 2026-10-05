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
	imagepkg "github.com/konradasb/dicer/internal/image"
	"github.com/konradasb/dicer/internal/types"
	"github.com/konradasb/dicer/internal/vm"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// imageHandler handles image-related RPCs.
type imageHandler struct {
	definitions *filestore.Manager
	instances   *vm.Manager
	images      *imagepkg.Manager
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

	image, err := h.images.Pull(stream.Context(), req.GetRef(), onProgress)
	if err != nil {
		return imageError(err)
	}

	return stream.Send(&dicerdv1.PullImageProgress{
		Stage: dicerdv1.PullStage_PULL_STAGE_UNSPECIFIED,
		Image: imageToProto(image),
	})
}

// ListImages lists the images this host holds.
func (h *imageHandler) ListImages(
	_ context.Context, _ *dicerdv1.ListImagesRequest,
) (*dicerdv1.ListImagesResponse, error) {
	images := h.images.List()

	resp := &dicerdv1.ListImagesResponse{
		Images: make([]*dicerdv1.Image, 0, len(images)),
	}
	for _, image := range images {
		resp.Images = append(resp.Images, imageToProto(image))
	}

	return resp, nil
}

// GetImage returns an image this host holds without contacting a registry.
func (h *imageHandler) GetImage(
	_ context.Context, req *dicerdv1.GetImageRequest,
) (*dicerdv1.Image, error) {
	image, err := h.images.Image(req.GetRef())
	if err != nil {
		return nil, imageError(err)
	}

	return imageToProto(image), nil
}

// DeleteImage removes an image, refusing one that is in use.
func (h *imageHandler) DeleteImage(
	_ context.Context, req *dicerdv1.DeleteImageRequest,
) (*emptypb.Empty, error) {
	image, err := h.images.Image(req.GetRef())
	if err != nil {
		return nil, imageError(err)
	}

	if !req.GetForce() {
		if users := h.instancesUsing(image.Digest); len(users) > 0 {
			return nil, errdefs.InvalidState(
				"image %q is in use by instance %q", req.GetRef(), users[0])
		}

		inUse, err := h.instances.ImagesInUse()
		if err != nil {
			return nil, err
		}
		if _, ok := inUse[image.Digest]; ok {
			return nil, errdefs.InvalidState(
				"image %q is the root disk of a running instance, or of a snapshot", req.GetRef())
		}
	}

	if err := h.images.Delete(req.GetRef()); err != nil {
		return nil, imageError(err)
	}

	return &emptypb.Empty{}, nil
}

// PruneImages removes the images nothing is defined to boot from.
func (h *imageHandler) PruneImages(
	_ context.Context, _ *dicerdv1.PruneImagesRequest,
) (*dicerdv1.PruneImagesResponse, error) {
	keep, err := h.instances.ImagesInUse()
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
	for _, image := range result.Images {
		resp.Images = append(resp.Images, imageToProto(&image))
	}

	return resp, nil
}

// instancesUsing names the instances defined to boot from the image with
// the given digest, sorted.
func (h *imageHandler) instancesUsing(digest string) []string {
	var users []string
	for _, instance := range h.definitions.Instances() {
		image, err := h.images.Image(instance.ImageRef)
		if err == nil && image.Digest == digest {
			users = append(users, instance.Name)
		}
	}
	return users
}

// imageError returns err, an image reference that cannot be parsed made an
// invalid argument.
func imageError(err error) error {
	if errors.Is(err, imagepkg.ErrInvalidReference) {
		return errdefs.InvalidArgument("%v", err)
	}
	return err
}
