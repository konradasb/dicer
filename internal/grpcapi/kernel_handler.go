// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nrednav/cuid2"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/kernel"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// kernelHandler handles kernel-related RPCs.
type kernelHandler struct {
	definitions *filestore.Manager
	kernels     *kernel.Manager
	events      recorder
}

// ImportKernel records a kernel by URL. It is downloaded on first use.
func (h *kernelHandler) ImportKernel(
	_ context.Context, req *dicerdv1.ImportKernelRequest,
) (*dicerdv1.Kernel, error) {
	arch, err := architectures.fromProto(req.GetArch())
	if err != nil {
		return nil, err
	}

	now := time.Now()
	k := types.Kernel{
		ID:           cuid2.Generate(),
		Name:         req.GetName(),
		Architecture: arch,
		URL:          req.GetUrl(),
		SHA256:       strings.ToLower(req.GetSha256()),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := k.Validate(); err != nil {
		return nil, err
	}
	if _, err := h.definitions.Kernel(k.Name); err == nil {
		return nil, errdefs.Exists("kernel %q already exists", k.Name)
	}
	if err := h.definitions.CreateKernel(k); err != nil {
		return nil, err
	}

	message := fmt.Sprintf("Imported kernel for %s from %s, to be fetched when an instance first starts with it", k.Architecture, k.URL)
	if k.SHA256 == "" {
		message += "; no checksum to verify it by"
	}
	h.record(k, events.ActionImported, message)

	return kernelToProto(k), nil
}

// ListKernels lists the imported kernels, sorted by name.
func (h *kernelHandler) ListKernels(
	_ context.Context, _ *dicerdv1.ListKernelsRequest,
) (*dicerdv1.ListKernelsResponse, error) {
	kernels := h.definitions.Kernels()

	resp := &dicerdv1.ListKernelsResponse{
		Kernels: make([]*dicerdv1.Kernel, 0, len(kernels)),
	}
	for _, k := range kernels {
		resp.Kernels = append(resp.Kernels, kernelToProto(k))
	}

	return resp, nil
}

// GetKernel returns an imported kernel.
func (h *kernelHandler) GetKernel(
	_ context.Context, req *dicerdv1.GetKernelRequest,
) (*dicerdv1.Kernel, error) {
	k, err := h.definitions.Kernel(req.GetName())
	if err != nil {
		return nil, err
	}

	return kernelToProto(k), nil
}

// DeleteKernel removes a kernel and its fetched copy, refusing one an
// instance uses.
func (h *kernelHandler) DeleteKernel(
	_ context.Context, req *dicerdv1.DeleteKernelRequest,
) (*emptypb.Empty, error) {
	k, err := h.definitions.Kernel(req.GetName())
	if err != nil {
		return nil, err
	}

	inUse := func(instance types.InstanceSpec) bool { return instance.KernelName == k.Name }
	if err := refuseInUse(h.definitions, fmt.Sprintf("kernel %q is in use", k.Name), inUse); err != nil {
		return nil, err
	}

	message := "Deleted kernel and its fetched copy"
	if h.kernels.DiskBytes(k.ID) == 0 {
		message = "Deleted kernel, never fetched"
	}
	if err := h.definitions.DeleteKernel(k.Name); err != nil {
		return nil, err
	}
	h.record(k, events.ActionDeleted, message)

	if err := h.kernels.Delete(k.ID); err != nil {
		return nil, fmt.Errorf("remove kernel binary: %w", err)
	}

	return &emptypb.Empty{}, nil
}

// record records that action happened to k, with its URL and architecture
// among the attributes.
func (h *kernelHandler) record(k types.Kernel, action events.Action, message string) {
	h.events.Record(events.Event{
		Kind:       events.KindKernel,
		ID:         k.ID,
		Name:       k.Name,
		Action:     action,
		Message:    message,
		Attributes: map[string]string{"url": k.URL, "arch": k.Architecture},
	})
}
