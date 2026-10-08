// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package dicer

import (
	"context"
	"errors"
	"io"
	"time"

	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// Kernels are the calls about guest kernels, reached as Client.Kernels.
type Kernels struct {
	api dicerdv1.DaemonServiceClient
}

// Kernel is a guest kernel instances can boot.
type Kernel struct {
	ID string `json:"id,omitzero"`

	KernelSpec

	CreateTime time.Time `json:"create_time,omitzero"`
	UpdateTime time.Time `json:"update_time,omitzero"`
}

// KernelSpec is what a kernel is.
type KernelSpec struct {
	Name         string       `json:"name,omitzero"`
	Architecture Architecture `json:"architecture,omitzero"`

	// SHA256 is the SHA-256 of the kernel, hex-encoded. An import verifies
	// the kernel against it when it is set, and sets it when it is not.
	SHA256 string `json:"sha256,omitzero"`
}

// Architecture is the CPU architecture a kernel is built for.
type Architecture string

// The architectures.
const (
	ArchitectureX86_64  Architecture = "x86_64"
	ArchitectureAArch64 Architecture = "aarch64"
)

var architectures = enum[Architecture, dicerdv1.Architecture]{"architecture", map[Architecture]dicerdv1.Architecture{
	ArchitectureX86_64:  dicerdv1.Architecture_ARCHITECTURE_X86_64,
	ArchitectureAArch64: dicerdv1.Architecture_ARCHITECTURE_AARCH64,
}}

// Import puts the kernel read from r on the daemon's host, and returns once
// it is there. A kernel can be at most 512 MiB. It is verified against
// spec.SHA256 if that is set, and given the SHA-256 of what was sent if not.
func (s *Kernels) Import(ctx context.Context, spec KernelSpec, r io.Reader) (Kernel, error) {
	start, err := importKernelStart(spec)
	if err != nil {
		return Kernel{}, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := s.api.ImportKernel(ctx)
	if err != nil {
		return Kernel{}, fromStatus(err)
	}
	send := func(req *dicerdv1.ImportKernelRequest) error {
		if err := stream.Send(req); errors.Is(err, io.EOF) {
			// The daemon ended the call, and says why when it is closed.
			_, err = stream.CloseAndRecv()
			return fromStatus(err)
		} else if err != nil {
			return fromStatus(err)
		}
		return nil
	}

	if err := send(&dicerdv1.ImportKernelRequest{Payload: &dicerdv1.ImportKernelRequest_Start{Start: start}}); err != nil {
		return Kernel{}, err
	}
	err = sendChunks(r, func(chunk []byte) error {
		return send(&dicerdv1.ImportKernelRequest{Payload: &dicerdv1.ImportKernelRequest_Data{Data: chunk}})
	})
	if err != nil {
		return Kernel{}, err
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return Kernel{}, fromStatus(err)
	}
	return kernelFromProto(resp), nil
}

// List returns every kernel, the default one among them.
func (s *Kernels) List(ctx context.Context) ([]Kernel, error) {
	resp, err := s.api.ListKernels(ctx, &dicerdv1.ListKernelsRequest{})
	if err != nil {
		return nil, fromStatus(err)
	}
	return convertAll(resp.GetKernels(), kernelFromProto), nil
}

// Get returns one kernel, or ErrNotFound.
func (s *Kernels) Get(ctx context.Context, name string) (Kernel, error) {
	resp, err := s.api.GetKernel(ctx, &dicerdv1.GetKernelRequest{Name: name})
	if err != nil {
		return Kernel{}, fromStatus(err)
	}
	return kernelFromProto(resp), nil
}

// Delete removes a kernel that no instance references. The default kernel
// cannot be deleted.
func (s *Kernels) Delete(ctx context.Context, name string) error {
	_, err := s.api.DeleteKernel(ctx, &dicerdv1.DeleteKernelRequest{Name: name})
	return fromStatus(err)
}

// importKernelStart returns the message that starts an import of the
// kernel spec describes.
func importKernelStart(spec KernelSpec) (*dicerdv1.ImportKernelStart, error) {
	arch, err := architectures.toProto(spec.Architecture)
	if err != nil {
		return nil, err
	}

	return &dicerdv1.ImportKernelStart{Name: spec.Name, Arch: arch, Sha256: spec.SHA256}, nil
}

// kernelFromProto returns the kernel p describes.
func kernelFromProto(p *dicerdv1.Kernel) Kernel {
	return Kernel{
		ID: p.GetId(),
		KernelSpec: KernelSpec{
			Name:         p.GetName(),
			Architecture: architectures.fromProto(p.GetArch()),
			SHA256:       p.GetSha256(),
		},
		CreateTime: timeFromProto(p.GetCreateTime()),
		UpdateTime: timeFromProto(p.GetUpdateTime()),
	}
}
