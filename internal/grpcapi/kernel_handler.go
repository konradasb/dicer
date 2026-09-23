// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/nrednav/cuid2"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/naming"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// kernelHandler handles kernel-related RPCs.
type kernelHandler struct {
	definitions *filestore.Manager
	kernels     *kernel.Manager
}

// ImportKernel records a kernel by URL. It is downloaded on first use.
func (h *kernelHandler) ImportKernel(
	_ context.Context, req *dicerdv1.ImportKernelRequest,
) (*dicerdv1.Kernel, error) {
	if err := naming.Validate(req.GetName()); err != nil {
		return nil, err
	}

	switch {
	case req.GetUrl() == "":
		return nil, errdefs.InvalidArgument("url is required")
	case req.GetArch() == dicerdv1.Architecture_ARCHITECTURE_UNSPECIFIED:
		return nil, errdefs.InvalidArgument("arch is required")
	case req.GetSha256() != "" && !isSHA256Hex(req.GetSha256()):
		return nil, errdefs.InvalidArgument(
			"sha256 %q is not a hex-encoded SHA-256 digest", req.GetSha256())
	}

	arch, err := architectures.fromProto(req.GetArch())
	if err != nil {
		return nil, err
	}

	if _, err := h.definitions.GetKernel(req.GetName()); err == nil {
		return nil, errdefs.Exists("kernel %q already exists", req.GetName())
	}

	now := time.Now()
	k := types.Kernel{
		ID:        cuid2.Generate(),
		Name:      req.GetName(),
		Arch:      arch,
		URL:       req.GetUrl(),
		SHA256:    strings.ToLower(req.GetSha256()),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.definitions.CreateKernel(k); err != nil {
		return nil, err
	}

	return kernelToProto(k), nil
}

// isSHA256Hex reports whether s is a hex-encoded SHA-256 digest.
func isSHA256Hex(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}

func (h *kernelHandler) ListKernels(
	_ context.Context, _ *dicerdv1.ListKernelsRequest,
) (*dicerdv1.ListKernelsResponse, error) {
	kernels, err := h.definitions.ListKernels()
	if err != nil {
		return nil, err
	}

	resp := &dicerdv1.ListKernelsResponse{
		Kernels: make([]*dicerdv1.Kernel, 0, len(kernels)),
	}
	for _, k := range kernels {
		resp.Kernels = append(resp.Kernels, kernelToProto(k))
	}

	return resp, nil
}

func (h *kernelHandler) GetKernel(
	_ context.Context, req *dicerdv1.GetKernelRequest,
) (*dicerdv1.Kernel, error) {
	k, err := h.definitions.GetKernel(req.GetName())
	if err != nil {
		return nil, err
	}

	return kernelToProto(k), nil
}

func (h *kernelHandler) DeleteKernel(
	_ context.Context, req *dicerdv1.DeleteKernelRequest,
) (*emptypb.Empty, error) {
	k, err := h.definitions.GetKernel(req.GetName())
	if err != nil {
		return nil, err
	}

	inUse := func(inst types.InstanceSpec) bool { return inst.KernelName == k.Name }
	if err := refuseInUse(h.definitions, fmt.Sprintf("kernel %q is in use", k.Name), inUse); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteKernel(k.Name); err != nil {
		return nil, err
	}

	if err := h.kernels.Delete(k.ID); err != nil {
		return nil, fmt.Errorf("remove kernel binary: %w", err)
	}

	return &emptypb.Empty{}, nil
}
