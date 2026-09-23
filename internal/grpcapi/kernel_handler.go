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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/dicer-sh/dicer"
	"github.com/dicer-sh/dicer/internal/filestore"
	"github.com/dicer-sh/dicer/internal/kernel"
	dicerdv1 "github.com/dicer-sh/dicer/proto/dicerd/v1"
)

// kernelHandler handles kernel-related RPCs.
type kernelHandler struct {
	definitions *filestore.Manager
	kernels     *kernel.Manager
}

// ImportKernel records a kernel by URL. The binary itself is fetched lazily
// the first time an instance using it starts.
func (h *kernelHandler) ImportKernel(
	_ context.Context, req *dicerdv1.ImportKernelRequest,
) (*dicerdv1.Kernel, error) {
	if err := dicer.ValidateName(req.GetName()); err != nil {
		return nil, toStatus(err)
	}

	switch {
	case req.GetUrl() == "":
		return nil, status.Error(codes.InvalidArgument, "url is required")
	case req.GetArch() == "":
		return nil, status.Error(codes.InvalidArgument, "arch is required")
	case req.GetSha256() != "" && !isSHA256Hex(req.GetSha256()):
		return nil, status.Errorf(codes.InvalidArgument,
			"sha256 %q is not a hex-encoded SHA-256 digest", req.GetSha256())
	}

	if _, err := h.definitions.GetKernel(req.GetName()); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "kernel %q already exists", req.GetName())
	}

	now := time.Now()
	k := dicer.Kernel{
		ID:        cuid2.Generate(),
		Name:      req.GetName(),
		Arch:      req.GetArch(),
		URL:       req.GetUrl(),
		SHA256:    strings.ToLower(req.GetSha256()),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.definitions.CreateKernel(k); err != nil {
		return nil, toStatus(err)
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
		return nil, toStatus(err)
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
		return nil, toStatus(err)
	}

	return kernelToProto(k), nil
}

func (h *kernelHandler) DeleteKernel(
	_ context.Context, req *dicerdv1.DeleteKernelRequest,
) (*emptypb.Empty, error) {
	k, err := h.definitions.GetKernel(req.GetName())
	if err != nil {
		return nil, toStatus(err)
	}

	inUse := func(inst dicer.InstanceSpec) bool { return inst.KernelName == k.Name }
	if err := refuseInUse(h.definitions, fmt.Sprintf("kernel %q is in use", k.Name), inUse); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteKernel(k.Name); err != nil {
		return nil, toStatus(err)
	}

	if err := h.kernels.Delete(k.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "remove kernel binary: %v", err)
	}

	return &emptypb.Empty{}, nil
}
