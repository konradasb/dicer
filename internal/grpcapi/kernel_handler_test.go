// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/konradasb/dicer/internal/errdefs"
	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// importKernelStream is an ImportKernel stream that sends messages and keeps
// the response.
type importKernelStream struct {
	grpc.ServerStream

	ctx      context.Context
	messages []*dicerdv1.ImportKernelRequest
	resp     *dicerdv1.Kernel
}

func (s *importKernelStream) Context() context.Context { return s.ctx }

func (s *importKernelStream) Recv() (*dicerdv1.ImportKernelRequest, error) {
	if len(s.messages) == 0 {
		return nil, io.EOF
	}
	m := s.messages[0]
	s.messages = s.messages[1:]
	return m, nil
}

func (s *importKernelStream) SendAndClose(k *dicerdv1.Kernel) error {
	s.resp = k
	return nil
}

// importKernel imports the kernel start describes, with the chunks a client
// sends after it, and returns the kernel recorded.
func importKernel(
	t *testing.T, s *Server, start *dicerdv1.ImportKernelStart, chunks ...string,
) (*dicerdv1.Kernel, error) {
	t.Helper()

	messages := []*dicerdv1.ImportKernelRequest{{Payload: &dicerdv1.ImportKernelRequest_Start{Start: start}}}
	for _, c := range chunks {
		messages = append(messages, &dicerdv1.ImportKernelRequest{Payload: &dicerdv1.ImportKernelRequest_Data{Data: []byte(c)}})
	}
	stream := &importKernelStream{ctx: t.Context(), messages: messages}
	err := s.ImportKernel(stream)
	return stream.resp, err
}

// x86Kernel returns the start of an import of an x86_64 kernel.
func x86Kernel(name, sha256 string) *dicerdv1.ImportKernelStart {
	return &dicerdv1.ImportKernelStart{Name: name, Arch: dicerdv1.Architecture_ARCHITECTURE_X86_64, Sha256: sha256}
}

// TestKernelImportedAndDeletedAreRecorded checks a kernel's import and
// deletion are recorded with its architecture, and refused requests
// are not.
func TestKernelImportedAndDeletedAreRecorded(t *testing.T) {
	s, definitions := newTestServer(t)
	recorded := &fakeRecorder{}
	s.kernelHandler.events = recorded

	k, err := importKernel(t, s, x86Kernel("k", ""), "vmlinux")
	if err != nil {
		t.Fatalf("ImportKernel: %v", err)
	}
	if _, err := importKernel(t, s, x86Kernel("k", ""), "vmlinux"); err == nil {
		t.Fatal("ImportKernel of a name taken succeeded")
	}

	if err := definitions.CreateInstance(types.InstanceSpec{ID: "i-1", Name: "web", KernelName: "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteKernel(t.Context(), &dicerdv1.DeleteKernelRequest{Name: "k"}); err == nil {
		t.Fatal("DeleteKernel of a kernel in use succeeded")
	}
	if err := definitions.DeleteInstance("web"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteKernel(t.Context(), &dicerdv1.DeleteKernelRequest{Name: "k"}); err != nil {
		t.Fatalf("DeleteKernel: %v", err)
	}

	want := []struct {
		action  events.Action
		message string
	}{
		{events.ActionImported, "Imported kernel for x86_64: 7 B, no checksum given to verify it by"},
		{events.ActionDeleted, "Deleted kernel and its copy on the host"},
	}
	if len(recorded.events) != len(want) {
		t.Fatalf("recorded %d events, want %d: %+v", len(recorded.events), len(want), recorded.events)
	}
	for i, e := range recorded.events {
		if e.Kind != events.KindKernel || e.ID != k.GetId() || e.Name != "k" || e.Action != want[i].action {
			t.Errorf("event %d = %+v, want kernel k %s", i, e, want[i].action)
		}
		if e.Message != want[i].message {
			t.Errorf("event %d message = %q, want %q", i, e.Message, want[i].message)
		}
		if e.Attributes["arch"] != "x86_64" {
			t.Errorf("event %d attributes = %v, want its architecture", i, e.Attributes)
		}
	}
}

// TestImportKernelKeepsAKernelTheClientSends checks that a kernel the client
// sends is recorded with the SHA-256 of what was sent, and is on
// the host.
func TestImportKernelKeepsAKernelTheClientSends(t *testing.T) {
	s, definitions := newTestServer(t)
	recorded := &fakeRecorder{}
	s.kernelHandler.events = recorded

	const contents = "a sent kernel"
	sum := sha256.Sum256([]byte(contents))
	digest := hex.EncodeToString(sum[:])

	k, err := importKernel(t, s, x86Kernel("sent", ""), contents[:5], contents[5:])
	if err != nil {
		t.Fatalf("ImportKernel: %v", err)
	}
	if k.GetSha256() != digest {
		t.Errorf("kernel = %+v, want the SHA-256 %s", k, digest)
	}

	stored, err := definitions.Kernel("sent")
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.kernels.Path(stored)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != contents {
		t.Errorf("the kernel kept = %q, %v; want %q", data, err, contents)
	}

	want := "Imported kernel for x86_64: 13 B, no checksum given to verify it by"
	if len(recorded.events) != 1 || recorded.events[0].Message != want {
		t.Errorf("events = %+v, want one import: %q", recorded.events, want)
	}
}

func TestImportKernelRefusesWhatItCannotKeep(t *testing.T) {
	s, definitions := newTestServer(t)
	if err := definitions.CreateKernel(types.Kernel{ID: "k-1", Name: "taken"}); err != nil {
		t.Fatal(err)
	}

	noStart := &importKernelStream{ctx: t.Context(), messages: []*dicerdv1.ImportKernelRequest{
		{Payload: &dicerdv1.ImportKernelRequest_Data{Data: []byte("x")}},
	}}
	if err := s.ImportKernel(noStart); err == nil {
		t.Error("an import with no start message succeeded")
	}

	for _, tt := range []struct {
		name  string
		start *dicerdv1.ImportKernelStart
	}{
		{"a name another kernel has", x86Kernel("taken", "")},
		{"the default kernel's name", x86Kernel(types.DefaultKernelName, "")},
		{"a checksum it fails", x86Kernel("bad", strings.Repeat("00", 32))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := importKernel(t, s, tt.start, "kernel"); err == nil {
				t.Fatal("ImportKernel succeeded")
			}
		})
	}
	if _, err := definitions.Kernel("bad"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("a kernel that failed its checksum was recorded: %v", err)
	}

	if _, err := importKernel(t, s, x86Kernel("empty", "")); !errors.Is(err, errdefs.ErrInvalidArgument) {
		t.Errorf("importing an empty kernel = %v, want errdefs.ErrInvalidArgument", err)
	}
	if _, err := definitions.Kernel("empty"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Errorf("an empty kernel was recorded: %v", err)
	}
}

// TestDefaultKernelIsReserved checks that the default kernel cannot be
// deleted, and that no kernel can be imported under its name.
func TestDefaultKernelIsReserved(t *testing.T) {
	s, definitions := newTestServer(t)
	if err := definitions.CreateKernel(types.Kernel{ID: "k-1", Name: types.DefaultKernelName}); err != nil {
		t.Fatal(err)
	}

	_, err := s.DeleteKernel(t.Context(), &dicerdv1.DeleteKernelRequest{Name: types.DefaultKernelName})
	wantClass(t, err, errdefs.ErrInvalidArgument)

	_, err = importKernel(t, s, x86Kernel(types.DefaultKernelName, ""), "vmlinux")
	wantClass(t, err, errdefs.ErrInvalidArgument)
}
