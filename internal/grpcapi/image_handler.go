// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/image"
	"github.com/dicer-sh/dicer/internal/vm"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// imageHandler handles image-related RPCs.
type imageHandler struct {
	definitions *filestore.Manager
	instances   *vm.Manager
	images      *image.Manager
}

// PullImage pulls an image, reporting progress as it goes, and finishes with
// the image itself. An image this host already holds is returned at once.
func (h *imageHandler) PullImage(
	req *dicerdv1.PullImageRequest,
	stream grpc.ServerStreamingServer[dicerdv1.PullImageProgress],
) error {
	if req.GetRef() == "" {
		return status.Error(codes.InvalidArgument, "ref is required")
	}

	// Progress arrives from the goroutine doing the pull, which is this
	// one, so sending from here needs no synchronisation.
	onProgress := func(p dicer.PullProgress) {
		_ = stream.Send(&dicerdv1.PullImageProgress{
			Stage:           pullStage(p.Stage),
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

// pullStage maps a stage onto the API's enum.
func pullStage(stage dicer.PullStage) dicerdv1.PullStage {
	switch stage {
	case dicer.StageResolving:
		return dicerdv1.PullStage_PULL_STAGE_RESOLVING
	case dicer.StageDownloading:
		return dicerdv1.PullStage_PULL_STAGE_DOWNLOADING
	case dicer.StageUnpacking:
		return dicerdv1.PullStage_PULL_STAGE_UNPACKING
	case dicer.StageConverting:
		return dicerdv1.PullStage_PULL_STAGE_CONVERTING
	default:
		return dicerdv1.PullStage_PULL_STAGE_UNSPECIFIED
	}
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

// GetImage returns an image this host holds. Unlike PullImage it never
// contacts a registry.
func (h *imageHandler) GetImage(
	_ context.Context, req *dicerdv1.GetImageRequest,
) (*dicerdv1.Image, error) {
	img, err := h.images.Get(req.GetRef())
	if err != nil {
		return nil, imageStatus(err)
	}

	return imageToProto(img), nil
}

// DeleteImage removes an image, refusing one an instance is defined to boot
// from, so that a delete cannot quietly break a stopped instance, and one a
// running guest or a snapshot needs.
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
			return nil, status.Errorf(codes.FailedPrecondition,
				"image %q is in use by instance %q", req.GetRef(), users[0])
		}

		needed, err := h.instances.ImagesInUse()
		if err != nil {
			return nil, toStatus(err)
		}
		if _, ok := needed[img.Digest]; ok {
			return nil, status.Errorf(codes.FailedPrecondition,
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
		return nil, toStatus(err)
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
		return nil, toStatus(err)
	}
	return keep, nil
}

// instancesUsing names the instances defined to boot from an image.
func (h *imageHandler) instancesUsing(digest string) ([]string, error) {
	instances, err := h.definitions.ListInstances()
	if err != nil {
		return nil, toStatus(err)
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

// imageStatus maps image errors onto status codes, on top of the shared
// classes toStatus handles.
func imageStatus(err error) error {
	if errors.Is(err, image.ErrInvalidReference) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return toStatus(err)
}
