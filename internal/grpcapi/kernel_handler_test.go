// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package grpcapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/konradasb/dicer/internal/events"
	"github.com/konradasb/dicer/internal/types"
	dicerdv1 "github.com/konradasb/dicer/proto/dicerd/v1"
)

// TestKernelImportedAndDeletedAreRecorded checks a kernel's import and
// deletion are recorded with its URL and architecture, and refused requests
// are not.
func TestKernelImportedAndDeletedAreRecorded(t *testing.T) {
	s, definitions := newTestServer(t)
	recorded := &fakeRecorder{}
	s.kernelHandler.events = recorded

	req := &dicerdv1.ImportKernelRequest{
		Name: "k", Url: "https://example.invalid/vmlinux", Arch: dicerdv1.Architecture_ARCHITECTURE_X86_64,
	}
	k, err := s.ImportKernel(t.Context(), req)
	if err != nil {
		t.Fatalf("ImportKernel: %v", err)
	}
	if _, err := s.ImportKernel(t.Context(), req); err == nil {
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
		{events.ActionImported, "Imported kernel for x86_64 from https://example.invalid/vmlinux, " +
			"to be fetched when an instance first starts with it; no checksum to verify it by"},
		{events.ActionDeleted, "Deleted kernel, never fetched"},
	}
	if len(recorded.events) != len(want) {
		t.Fatalf("recorded %+v, want %d events", recorded.events, len(want))
	}
	for i, e := range recorded.events {
		if e.Kind != events.KindKernel || e.ID != k.GetId() || e.Name != "k" || e.Action != want[i].action {
			t.Errorf("event %d = %+v, want kernel k %s", i, e, want[i].action)
		}
		if e.Message != want[i].message {
			t.Errorf("event %d message = %q, want %q", i, e.Message, want[i].message)
		}
		if e.Attributes["url"] != req.GetUrl() || e.Attributes["arch"] != "x86_64" {
			t.Errorf("event %d attributes = %v, want its URL and architecture", i, e.Attributes)
		}
	}
}

// TestDeletingAFetchedKernelSaysItsCopyWentToo checks the deleted event of a
// kernel fetched to the host says its copy was removed with it.
func TestDeletingAFetchedKernelSaysItsCopyWentToo(t *testing.T) {
	s, definitions := newTestServer(t)
	recorded := &fakeRecorder{}
	s.kernelHandler.events = recorded

	source := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(source, []byte("vmlinux"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportKernel(t.Context(), &dicerdv1.ImportKernelRequest{
		Name: "k", Url: source, Arch: dicerdv1.Architecture_ARCHITECTURE_X86_64,
	}); err != nil {
		t.Fatalf("ImportKernel: %v", err)
	}
	k, err := definitions.Kernel("k")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.kernels.Path(t.Context(), k); err != nil {
		t.Fatalf("fetch the kernel: %v", err)
	}

	if _, err := s.DeleteKernel(t.Context(), &dicerdv1.DeleteKernelRequest{Name: "k"}); err != nil {
		t.Fatalf("DeleteKernel: %v", err)
	}

	last := recorded.events[len(recorded.events)-1]
	if last.Action != events.ActionDeleted || last.Message != "Deleted kernel and its fetched copy" {
		t.Errorf("last event = %+v, want the kernel deleted with its copy", last)
	}
}
