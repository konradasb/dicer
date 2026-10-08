// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nrednav/cuid2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/filestore"
	"github.com/konradasb/dicer/internal/humanize"
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

// ImportKernel puts a kernel the client sends on the host, and records it
// once it is there.
func (h *kernelHandler) ImportKernel(
	stream grpc.ClientStreamingServer[dicerdv1.ImportKernelRequest, dicerdv1.Kernel],
) error {
	req, err := stream.Recv()
	if err != nil {
		return errdefs.InvalidArgument("receive start message: %v", err)
	}
	start := req.GetStart()
	if start == nil {
		return errdefs.InvalidArgument("first message must be an ImportKernelStart")
	}
	arch, err := architectures.fromProto(start.GetArch())
	if err != nil {
		return err
	}

	now := time.Now()
	k := types.Kernel{
		ID:           cuid2.Generate(),
		Name:         start.GetName(),
		Architecture: arch,
		SHA256:       strings.ToLower(start.GetSha256()),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.checkNewKernel(k); err != nil {
		return err
	}

	// The SHA-256 is kept whether or not the client gave one, so that the
	// kernel is checked each time an instance boots it.
	k.SHA256, err = h.kernels.Import(k.ID, &importKernelReader{stream: stream}, k.SHA256)
	if err != nil {
		_ = h.kernels.Delete(k.ID)
		return err
	}
	if err := h.definitions.CreateKernel(k); err != nil {
		_ = h.kernels.Delete(k.ID)
		return err
	}

	verified := "checksum verified"
	if start.GetSha256() == "" {
		verified = "no checksum given to verify it by"
	}
	h.record(k, events.ActionImported, fmt.Sprintf("Imported kernel for %s: %s, %s",
		k.Architecture, humanize.Bytes(h.kernels.DiskBytes(k.ID)), verified))

	return stream.SendAndClose(kernelToProto(k))
}

// importKernelReader reads the kernel an ImportKernel stream carries after
// its start message.
type importKernelReader struct {
	stream grpc.ClientStreamingServer[dicerdv1.ImportKernelRequest, dicerdv1.Kernel]
	chunk  []byte
}

// Read reads the next of the kernel, and io.EOF once the client has sent it
// all.
func (r *importKernelReader) Read(p []byte) (int, error) {
	for len(r.chunk) == 0 {
		req, err := r.stream.Recv()
		if err != nil {
			return 0, err
		}
		r.chunk = req.GetData()
	}

	n := copy(p, r.chunk)
	r.chunk = r.chunk[n:]
	return n, nil
}

// checkNewKernel returns an error unless k is valid and can be added: it
// does not take the default kernel's name, or another kernel's.
func (h *kernelHandler) checkNewKernel(k types.Kernel) error {
	if err := k.Validate(); err != nil {
		return err
	}
	if k.Name == types.DefaultKernelName {
		return errdefs.InvalidArgument("%q is the default kernel's name: import the kernel under another", k.Name)
	}
	if _, err := h.definitions.Kernel(k.Name); err == nil {
		return errdefs.Exists("kernel %q already exists", k.Name)
	}
	return nil
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

// DeleteKernel removes a kernel and its copy on the host, refusing the default
// kernel and one an instance uses.
func (h *kernelHandler) DeleteKernel(
	_ context.Context, req *dicerdv1.DeleteKernelRequest,
) (*emptypb.Empty, error) {
	k, err := h.definitions.Kernel(req.GetName())
	if err != nil {
		return nil, err
	}
	if k.Name == types.DefaultKernelName {
		return nil, errdefs.InvalidArgument("the default kernel cannot be deleted")
	}

	inUse := func(instance types.InstanceSpec) bool { return instance.KernelName == k.Name }
	if err := refuseInUse(h.definitions, fmt.Sprintf("kernel %q is in use", k.Name), inUse); err != nil {
		return nil, err
	}

	if err := h.definitions.DeleteKernel(k.Name); err != nil {
		return nil, err
	}
	h.record(k, events.ActionDeleted, "Deleted kernel and its copy on the host")

	if err := h.kernels.Delete(k.ID); err != nil {
		return nil, fmt.Errorf("remove kernel binary: %w", err)
	}

	return &emptypb.Empty{}, nil
}

// record records that action happened to k, with its architecture among
// the attributes.
func (h *kernelHandler) record(k types.Kernel, action events.Action, message string) {
	h.events.Record(events.Event{
		Kind:       events.KindKernel,
		ID:         k.ID,
		Name:       k.Name,
		Action:     action,
		Message:    message,
		Attributes: map[string]string{"arch": k.Architecture},
	})
}
